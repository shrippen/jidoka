//go:build !release

package main

import (
	"path/filepath"
	"testing"
	"time"

	"jidoka/internal/db"
	"jidoka/internal/demo"
	"jidoka/internal/services/secrets"
)

func TestDemoNow(t *testing.T) {
	real := time.Date(2026, 11, 11, 14, 30, 5, 0, time.UTC)

	got, err := demoNow(real, "2026-03-02")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 3, 2, 14, 30, 5, 0, time.UTC); !got.Equal(want) {
		t.Errorf("fixed day: %v, want %v", got, want)
	}

	if got, _ := demoNow(real, ""); !got.Equal(real) {
		t.Errorf("no DEMO_TODAY: %v", got)
	}
	if _, err := demoNow(real, "yesterday"); err == nil {
		t.Error("bad DEMO_TODAY accepted")
	}
}

func TestDemoCommandIsKnown(t *testing.T) {
	if _, ok := extraCommands[demoName]; !ok {
		t.Fatal("demo is not registered")
	}
	if code := runDemo([]string{"fr"}); code != exitUsage {
		t.Errorf("unknown language: exit %d", code)
	}
}

// The folder Prepare writes opens with the key and salt jidoka serve
// derives, so the sealed secrets can be read.
func TestDemoOpensLikeServe(t *testing.T) {
	t.Setenv(envKey, demoKey)

	dir := t.TempDir()
	if _, err := demo.Prepare(dir, "en", time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envData, dir)
	if err := initKey(dir); err != nil {
		t.Fatal(err)
	}

	conn, err := db.Open(filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	values, missing, err := secrets.New(conn).Resolve([]string{"KIMAI_TOKEN"})
	if err != nil || len(missing) > 0 || values["KIMAI_TOKEN"] == "" {
		t.Fatalf("resolve: %v %v %v", values, missing, err)
	}
}
