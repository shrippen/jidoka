// Command jidoka-shim wraps one job script (drivers/proc). It is its own
// binary without file capabilities, so a job that runs it gains nothing.
package main

import (
	"os"

	"jidoka/internal/drivers/proc"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != proc.ShimArg {
		os.Exit(2)
	}

	os.Exit(proc.Shim(os.Args[2:]))
}
