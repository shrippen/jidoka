package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/crypto"
	"jidoka/internal/model"
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

// A restart with the repo reachable again closes the disturbance the last
// run left open; else a later outage would stay silent.
func TestStartClosesRepoDisturbance(t *testing.T) {
	t.Setenv(envData, t.TempDir())
	t.Setenv(envJobs, t.TempDir())
	t.Setenv(envKey, "")

	boot := func(repo *fakeRepo) *app {
		a, err := build()
		if err != nil {
			t.Fatal(err)
		}
		a.jobs.UseRepo(repo)
		if err := a.start(); err != nil {
			t.Fatal(err)
		}
		return a
	}

	a := boot(&fakeRepo{})
	if d, _ := alerts.NewDBStore(a.db).Disturbance(alerts.KeyRepo); d == nil {
		t.Fatal("failed fetch opened no disturbance")
	}
	a.db.Close()

	a = boot(&fakeRepo{ok: true})
	defer a.db.Close()
	if d, _ := alerts.NewDBStore(a.db).Disturbance(alerts.KeyRepo); d != nil {
		t.Errorf("still open after a good start: %+v", d)
	}
}

// A jobs folder that cannot be read is no jidoka.toml problem, yet it is
// trouble: neither start nor reload may close it as fixed.
func TestMissingJobsDirStaysOpen(t *testing.T) {
	t.Setenv(envData, t.TempDir())
	t.Setenv(envJobs, filepath.Join(t.TempDir(), "gone"))
	t.Setenv(envKey, "")

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
		t.Fatal("start closed the disturbance")
	}
	if _, err := a.jobs.Reload(); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Disturbance(alerts.KeyGlobal); d == nil {
		t.Error("reload closed the disturbance")
	}
}

// An unreadable jobs folder says nothing about which jobs exist: trouble of
// a job stays open until a good run, also after the folder is back.
func TestUnreadableDirKeepsRunTrouble(t *testing.T) {
	dir := t.TempDir()
	jobs := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(filepath.Join(jobs, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(jobs, "a", "job.toml"), []byte("run = \"run.sh\"\nschedule = \"0 6 * * *\"\n"), 0o644)
	os.WriteFile(filepath.Join(jobs, "a", "run.sh"), []byte("#!/bin/sh\n"), 0o755)

	t.Setenv(envData, t.TempDir())
	t.Setenv(envJobs, jobs)
	t.Setenv(envKey, "")

	a, err := build()
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	if err := a.start(); err != nil {
		t.Fatal(err)
	}

	store := alerts.NewDBStore(a.db)
	err = store.SaveDisturbance(alerts.Disturbance{
		Job: "a", Event: model.EvFail, Since: time.Now(), Sent: map[int]time.Time{},
	})
	if err != nil {
		t.Fatal(err)
	}

	gone := filepath.Join(dir, "away")
	if err := os.Rename(jobs, gone); err != nil {
		t.Fatal(err)
	}
	if _, err := a.jobs.Reload(); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Disturbance("a"); d == nil {
		t.Fatal("closed while the folder was unreadable")
	}

	if err := os.Rename(gone, jobs); err != nil {
		t.Fatal(err)
	}
	if _, err := a.jobs.Reload(); err != nil {
		t.Fatal(err)
	}
	if d, _ := store.Disturbance("a"); d == nil {
		t.Error("closed by the reload; only a good run ends it")
	}
}

// B8: a jobs folder without jidoka.toml starts, shows the error and
// reports it over KeyGlobal.
func TestStartWithoutSettingsReports(t *testing.T) {
	t.Setenv(envData, t.TempDir())
	t.Setenv(envJobs, t.TempDir())
	t.Setenv(envKey, "")

	a, err := build()
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	if err := a.start(); err != nil {
		t.Fatal(err)
	}

	if a.jobs.Current().SettingsErr == nil {
		t.Error("no settings error")
	}
	if d, _ := alerts.NewDBStore(a.db).Disturbance(alerts.KeyGlobal); d == nil {
		t.Error("no disturbance under KeyGlobal")
	}
}

// jidoka run tries single jobs; it needs no jidoka.toml.
func TestRunLocalWithoutSettings(t *testing.T) {
	dir := write(t, map[string]string{
		"a/job.toml": "run = \"x.sh\"\nschedule = \"0 6 * * *\"\n",
		"a/x.sh":     "#!/bin/sh\necho hello\n",
	})
	if err := os.Chmod(filepath.Join(dir, "a", "x.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	if code := runLocal([]string{dir, "a"}); code != exitOK {
		t.Errorf("exit %d", code)
	}
}
