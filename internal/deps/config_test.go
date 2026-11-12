package deps

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

const testConfig = `
# monorepo layout
[projects]
bff = ["apps/bff"]   # the backend
web = [
  "/apps/web/",
  "packages/ui",
]

[deps]
"apps/web" = ["proto/web"]
"proto/web" = ["tools/codegen"]

[later-version-table]
x = 1
`

func TestParseConfig(t *testing.T) {
	c, err := ParseConfig([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string][]string{"bff": {"apps/bff"}, "web": {"apps/web", "packages/ui"}}; !reflect.DeepEqual(c.Projects, want) {
		t.Errorf("projects = %v, want %v", c.Projects, want)
	}
	if want := map[string][]string{"apps/web": {"proto/web"}, "proto/web": {"tools/codegen"}}; !reflect.DeepEqual(c.Deps, want) {
		t.Errorf("deps = %v, want %v", c.Deps, want)
	}

	for _, bad := range []string{
		"[projects]\nx = \"a\"",         // not a list
		"[projects]\nx = []",            // empty
		"[projects]\nx = [\".\"]",       // the root
		"[projects]\nx = [\"../a\"]",    // outside
		"[projects]\nx = [\".gion/a\"]", // gion's dir
		"[projects]\nx = [\"a\"]\nx = [\"b\"]",
		"[projects]\n\"a/b\" = [\"a\"]", // a name, not a path
		"[deps]\n\".\" = [\"a\"]",
	} {
		if _, err := ParseConfig([]byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestProjectPaths(t *testing.T) {
	c, _ := ParseConfig([]byte(testConfig))
	got, err := c.ProjectPaths([]string{"bff", "web"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"apps/bff", "apps/web", "packages/ui"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := c.ProjectPaths([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "defined: bff, web") {
		t.Errorf("unknown project: err = %v", err)
	}
	if _, err := (Config{}).ProjectPaths([]string{"bff"}); err == nil || !strings.Contains(err.Error(), ConfigFile) {
		t.Errorf("no config: err = %v", err)
	}
}

// Rules apply however their path joins (as a start path, inside one, or as
// a manifest's dependency) and chain with manifests both ways.
func TestResolveConfigDeps(t *testing.T) {
	src := memSource{
		ConfigFile:                   testConfig,
		"apps/web/package.json":      `{"name": "web"}`,
		"proto/web/a.proto":          ``,
		"tools/codegen/package.json": `{"dependencies": {"lib": "file:../../libs/lib"}}`,
		"libs/lib/package.json":      `{"name": "lib"}`,
		"apps/site/package.json":     `{"dependencies": {"web": "file:../web"}}`,
	}
	check(t, resolve(t, src, nil, []string{"apps/web"}),
		"proto/web <- .gion/projects.toml: apps/web",
		"tools/codegen <- .gion/projects.toml: proto/web",
		`libs/lib <- tools/codegen/package.json: "lib": "file:../../libs/lib"`,
	)
	check(t, resolve(t, src, nil, []string{"apps"}),
		"proto/web <- .gion/projects.toml: apps/web",
		"tools/codegen <- .gion/projects.toml: proto/web",
		`libs/lib <- tools/codegen/package.json: "lib": "file:../../libs/lib"`,
	)
	check(t, resolve(t, src, nil, []string{"apps/site"}),
		`apps/web <- apps/site/package.json: "web": "file:../web"`,
		"proto/web <- .gion/projects.toml: apps/web",
		"tools/codegen <- .gion/projects.toml: proto/web",
		`libs/lib <- tools/codegen/package.json: "lib": "file:../../libs/lib"`,
	)
	// The workspace has proto (and got its rules when it joined): nothing new.
	check(t, resolve(t, src, []string{"proto"}, []string{"apps/web"}))
}

// A rule from a workspace's copy of the config names that copy as its reason.
func TestResolveLocalConfigName(t *testing.T) {
	src := WithConfig(memSource{"a/x": "", "b/y": ""}, "./trial.toml", []byte("[deps]\n\"a\" = [\"b\"]\n"))
	found, err := Resolve(context.Background(), src, nil, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Path != "b" || found[0].Manifest != "./trial.toml" {
		t.Fatalf("found = %+v", found)
	}
}
