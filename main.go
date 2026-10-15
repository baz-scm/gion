// Command gion is a thin git client for always-online agents and CI: it
// holds only what a task needs and pulls files, trees and commits from the
// remote on demand, over the standard git wire protocol.
package main

import (
	"os"

	"github.com/baz-scm/gion/internal/cli"
)

// Set by the release build (-ldflags -X). `go install` builds fall back to
// the module version from the build info.
var (
	version = ""
	commit  = ""
)

func main() {
	cli.SetVersion(version, commit)
	os.Exit(cli.Main("gion", os.Args[1:]))
}
