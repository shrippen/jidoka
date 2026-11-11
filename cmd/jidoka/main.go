// Command jidoka runs, validates and maintains Jidoka.
package main

import (
	"fmt"
	"os"

	"jidoka/internal/drivers/proc"
)

const (
	exitOK       = 0
	exitInvalid  = 1
	exitUsage    = 2
	validateName = "validate"
)

func main() {
	// The shim wraps every job (drivers/proc); it must not start anything else.
	if len(os.Args) > 1 && os.Args[1] == proc.ShimArg {
		os.Exit(proc.Shim(os.Args[2:]))
	}

	os.Exit(run(os.Args[1:]))
}

// run dispatches the subcommand and returns the exit code.
func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jidoka serve|validate|run|rotate-key|healthcheck")
		return exitUsage
	}

	switch args[0] {
	case validateName:
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: jidoka validate <dir>")
			return exitUsage
		}
		return validate(args[1], os.Stdout, os.Stderr)
	case "serve", "run", "rotate-key", "healthcheck":
		fmt.Fprintf(os.Stderr, "jidoka %s: not implemented\n", args[0])
		return exitUsage
	}

	fmt.Fprintf(os.Stderr, "jidoka: unknown command %q\n", args[0])

	return exitUsage
}
