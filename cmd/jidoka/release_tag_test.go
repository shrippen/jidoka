//go:build release

package main

import "testing"

func TestNoDemoCommand(t *testing.T) {
	if code := run([]string{"demo"}); code != exitUsage {
		t.Errorf("exit %d, want %d", code, exitUsage)
	}
	if len(extraCommands) != 0 {
		t.Errorf("extra commands in a release build: %d", len(extraCommands))
	}
}
