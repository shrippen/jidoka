package main

// extraCommands are subcommands that only some builds have; their files
// register themselves in init.
var extraCommands = map[string]func(args []string) int{}
