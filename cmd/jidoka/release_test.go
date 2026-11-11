package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A release build has no demo command, and scripts/release-check.sh
// finds no demo data in it.
func TestReleaseBuildHasNoDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}

	bin := filepath.Join(t.TempDir(), "jidoka-release")
	build := exec.Command("go", "build", "-tags", "release", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "demo").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unknown command") {
		t.Errorf("demo command: %v, %q", err, out)
	}

	script := filepath.Join("..", "..", "scripts", "release-check.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skip("no release-check.sh")
	}
	if out, err := exec.Command("sh", script, bin).CombinedOutput(); err != nil {
		t.Errorf("release-check: %v\n%s", err, out)
	}
}
