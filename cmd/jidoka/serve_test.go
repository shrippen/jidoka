package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/services/alerts"
)

// The first start writes the salt next to the database; without it every
// start would derive the key from an empty salt.
func TestInitKeyWritesSalt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envKey, "test-master-key")

	if err := initKey(dir); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, dbFile+".salt"))
	if err != nil {
		t.Fatalf("salt not written next to %s: %v", dbFile, err)
	}
	if len(raw) == 0 {
		t.Fatal("empty salt")
	}

	// A second start reads the same salt, so the key opens old secrets.
	sealed, err := crypto.Encrypt("wert", crypto.PurposeSecret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initKey(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := crypto.Decrypt(sealed, crypto.PurposeSecret); err != nil || got != "wert" {
		t.Fatalf("second start: %q, %v", got, err)
	}
}

// Repairing only jidoka.toml changes no job, so the reload diff is
// empty; the disturbance of the broken file must still close (B8, O9).
func TestSettingsRepairCloses(t *testing.T) {
	data, jobsDir := t.TempDir(), t.TempDir()
	t.Setenv(envData, data)
	t.Setenv(envJobs, jobsDir)
	t.Setenv(envKey, "")

	settings := filepath.Join(jobsDir, "jidoka.toml")
	if err := os.WriteFile(settings, []byte("zone = \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := build()
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	if err := a.start(); err != nil {
		t.Fatal(err)
	}

	store := alerts.NewDBStore(a.db)
	if d, _ := store.Disturbance(alerts.KeyGlobal); d == nil {
		t.Fatal("broken jidoka.toml opened no disturbance")
	}

	if err := os.WriteFile(settings, []byte("zone = \"Europe/Berlin\"\n"+"[[profiles.standard]]\non = [\"fail\"]\nchannels = [\"mail\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.jobs.Reload(); err != nil {
		t.Fatal(err)
	}

	if d, _ := store.Disturbance(alerts.KeyGlobal); d != nil {
		t.Fatalf("still disturbed after the repair: %+v", d)
	}
}

// fakeRepo fails until ok is set.
type fakeRepo struct {
	calls int
	ok    bool
}

func (f *fakeRepo) Sync(context.Context) error {
	f.calls++
	if f.ok {
		return nil
	}
	return errors.New("gitea down")
}

// J1: a failed fetch is tried again every repoRetry and the success
// closes the disturbance; a healthy repo is not fetched in between.
func TestRepoRetryClosesDisturbance(t *testing.T) {
	t.Setenv(envData, t.TempDir())
	t.Setenv(envJobs, t.TempDir())
	t.Setenv(envKey, "")

	a, err := build()
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()

	repo := &fakeRepo{}
	a.jobs.UseRepo(repo)
	if err := a.start(); err != nil {
		t.Fatal(err)
	}

	store := alerts.NewDBStore(a.db)
	if d, _ := store.Disturbance(alerts.KeyRepo); d == nil {
		t.Fatal("failed fetch opened no disturbance")
	}
	if repo.calls != 1 {
		t.Fatalf("calls after start: %d", repo.calls)
	}

	t0 := time.Now()
	a.retryRepo(t0)
	a.retryRepo(t0.Add(repoRetry - time.Second))
	if repo.calls != 1 {
		t.Fatalf("retried too early: %d", repo.calls)
	}

	a.retryRepo(t0.Add(repoRetry))
	if repo.calls != 2 || a.jobs.Current().RepoErr == nil {
		t.Fatalf("calls %d, RepoErr %v", repo.calls, a.jobs.Current().RepoErr)
	}

	repo.ok = true
	a.retryRepo(t0.Add(2 * repoRetry))
	if repo.calls != 3 || a.jobs.Current().RepoErr != nil {
		t.Fatalf("calls %d, RepoErr %v", repo.calls, a.jobs.Current().RepoErr)
	}
	if d, _ := store.Disturbance(alerts.KeyRepo); d != nil {
		t.Errorf("still disturbed: %+v", d)
	}

	// Healthy: no more fetching.
	a.retryRepo(t0.Add(10 * repoRetry))
	a.retryRepo(t0.Add(20 * repoRetry))
	if repo.calls != 3 {
		t.Errorf("fetched while healthy: %d", repo.calls)
	}
}
