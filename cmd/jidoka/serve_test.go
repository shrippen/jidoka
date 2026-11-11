package main

import (
	"os"
	"path/filepath"
	"testing"

	"jidoka/internal/crypto"
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
