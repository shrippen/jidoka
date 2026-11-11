package main

import (
	"os"
	"testing"

	"jidoka/internal/drivers/proc"
)

// The test binary doubles as the shim, like main does, so a test may run
// a job with runLocal.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == proc.ShimArg {
		os.Exit(proc.Shim(os.Args[2:]))
	}
	os.Exit(m.Run())
}
