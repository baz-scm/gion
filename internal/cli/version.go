package cli

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
)

var buildVersion, buildCommit string

// SetVersion records the release version and commit, if the build set them.
func SetVersion(version, commit string) {
	buildVersion, buildCommit = version, commit
}

// Version returns the release version, else the module version from
// `go install`, else "dev".
func Version() string {
	if buildVersion != "" {
		return buildVersion
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func versionCmd(_ context.Context, _ []string) error {
	c := buildCommit
	if c == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					c = s.Value
				}
			}
		}
	}
	if len(c) > 12 {
		c = c[:12]
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	if c == "" {
		// `go install …@vX` builds from the module proxy, which records no
		// commit; the version alone identifies it.
		fmt.Printf("gion %s (%s)\n", Version(), platform)
		return nil
	}
	fmt.Printf("gion %s (%s, %s)\n", Version(), c, platform)
	return nil
}
