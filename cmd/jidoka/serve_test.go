package main

import (
	"os"
	"path/filepath"
	"testing"

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
