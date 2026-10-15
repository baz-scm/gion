// Command gion is a thin git client for always-online agents and CI: it
// holds only what a task needs and pulls files, trees and commits from the
// remote on demand, over the standard git wire protocol.
package main

import (
	"os"

	"github.com/baz-scm/gion/internal/cli"
)

func main() { os.Exit(cli.Main("gion", os.Args[1:])) }
