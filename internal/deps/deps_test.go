package deps

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

// memSource is a repo held as path -> contents.
type memSource map[string]string

func (m memSource) Stat(_ context.Context, p string) (Kind, error) {
	if _, ok := m[p]; ok {
		return File, nil
	}
	for f := range m {
		if p == "" || strings.HasPrefix(f, p+"/") {
			return Dir, nil
		}
	}
	return Missing, nil
}

func (m memSource) ReadFile(_ context.Context, p string) ([]byte, error) {
	if s, ok := m[p]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("no such file " + p)
}

func (m memSource) ReadDir(_ context.Context, dir string) ([]Entry, error) {
	seen := map[string]bool{}
	var out []Entry
	prefix := dir + "/"
	if dir == "" {
		prefix = ""
	}
	for f := range m {
		rest, ok := strings.CutPrefix(f, prefix)
		if !ok {
			continue
		}
		name, _, isDir := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			out = append(out, Entry{Name: name, Dir: isDir})
		}
	}
	return out, nil
}

func (m memSource) Files(_ context.Context, p string) ([]string, error) {
	var out []string
	for f := range m {
		if f == p || p == "" || strings.HasPrefix(f, p+"/") {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (memSource) Prefetch(context.Context, []string) error { return nil }

// resolve returns "path <- manifest: spec" lines.
func resolve(t *testing.T, src memSource, have, start []string) []string {
	t.Helper()
	found, err := Resolve(context.Background(), src, have, start)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range found {
		out = append(out, f.Path+" <- "+f.Manifest+": "+f.Spec)
	}
	sort.Strings(out)
	return out
}

func check(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestNPM(t *testing.T) {
	src := memSource{
		"package.json":                    `{"private": true, "workspaces": ["packages/*"]}`,
		"package-lock.json":               `{}`,
		"bff/package.json":                `{"name": "bff", "dependencies": {"@baz/shared-fixer": "file:../shared/fixer", "@baz/ui": "workspace:*", "left-pad": "^1.0.0"}}`,
		"bff/src/index.ts":                ``,
		"shared/fixer/package.json":       `{"name": "@baz/shared-fixer", "dependencies": {"@baz/core": "^2.0.0"}}`,
		"packages/ui/package.json":        `{"name": "@baz/ui"}`,
		"packages/core/package.json":      `{"name": "@baz/core"}`,
		"packages/unused/package.json":    `{"name": "@baz/unused"}`,
		"bff/node_modules/x/package.json": `{"dependencies": {"y": "file:../../../packages/unused"}}`,
	}
	check(t, resolve(t, src, nil, []string{"bff"}),
		`packages/core <- shared/fixer/package.json: "@baz/core": "^2.0.0"`,
		`packages/ui <- bff/package.json: "@baz/ui": "workspace:*"`,
		`shared/fixer <- bff/package.json: "@baz/shared-fixer": "file:../shared/fixer"`,
	)
	// Already in the workspace: nothing to add.
	check(t, resolve(t, src, []string{"shared", "packages"}, []string{"bff"}))
}

func TestPNPMWorkspace(t *testing.T) {
	src := memSource{
		"pnpm-workspace.yaml":      "packages:\n  - 'apps/*'\n  - \"libs/**\"\n  - '!libs/legacy'\n",
		"apps/web/package.json":    `{"name": "web", "dependencies": {"lib-a": "workspace:^", "legacy": "workspace:*"}}`,
		"libs/a/package.json":      `{"name": "lib-a"}`,
		"libs/legacy/package.json": `{"name": "legacy"}`,
	}
	check(t, resolve(t, src, nil, []string{"apps/web"}),
		`libs/a <- apps/web/package.json: "lib-a": "workspace:^"`,
	)
}

func TestCargo(t *testing.T) {
	src := memSource{
		"Cargo.toml": `
[workspace]
members = [
  "crates/*", # all crates
  "tools/gen",
]
exclude = ["crates/old"]

[workspace.dependencies]
shared = { path = "libs/shared" }
`,
		"crates/app/Cargo.toml": `
[package]
name = "app"

[lib]
path = "src/lib.rs"

[dependencies]
util = { path = "../../util", version = "1" }
shared.workspace = true
serde = "1"

[target.'cfg(unix)'.dependencies]
nix-shim = { path = "../../shims/nix" }

[dev-dependencies.testkit]
path = "../../testkit"
`,
		"crates/other/Cargo.toml": `[package]`,
		"crates/old/Cargo.toml":   `[package]`,
		"tools/gen/Cargo.toml":    `[package]`,
		"libs/shared/Cargo.toml":  `[package]`,
		"util/Cargo.toml":         `[package]`,
		"shims/nix/Cargo.toml":    `[package]`,
		"testkit/Cargo.toml":      `[package]`,
		"scratch/Cargo.toml":      `[package]`,
	}
	check(t, resolve(t, src, nil, []string{"crates/app"}),
		`crates/other <- Cargo.toml: workspace member`,
		`libs/shared <- Cargo.toml: [workspace.dependencies] shared = { path = ... }`,
		`shims/nix <- crates/app/Cargo.toml: nix-shim = { path = "../../shims/nix" }`,
		`testkit <- crates/app/Cargo.toml: [dev-dependencies.testkit] path = "../../testkit"`,
		`tools/gen <- Cargo.toml: workspace member`,
		`util <- crates/app/Cargo.toml: util = { path = "../../util" }`,
	)
	check(t, resolve(t, src, nil, []string{"scratch"}))
}

func TestGo(t *testing.T) {
	src := memSource{
		"go.work":             "go 1.22\n\nuse (\n\t./svc\n\t./lib // shared\n)\n",
		"svc/go.mod":          "module example.com/svc\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/proto => ../gen/proto\n\nreplace (\n\texample.com/x v1.0.0 => example.com/y v1.0.0\n)\n",
		"svc/main.go":         "",
		"lib/go.mod":          "module example.com/lib\n",
		"gen/proto/go.mod":    "module example.com/proto\n",
		"svc/testdata/go.mod": "module fake\nreplace a => ../../nowhere\n",
	}
	check(t, resolve(t, src, nil, []string{"svc"}),
		`gen/proto <- svc/go.mod: replace example.com/proto => ../gen/proto`,
		`lib <- go.work: use ./lib`,
	)
}

func TestPythonUV(t *testing.T) {
	src := memSource{
		"pyproject.toml":                      "[tool.uv.workspace]\nmembers = [\"packages/*\"]\n",
		"packages/api/pyproject.toml":         "[project]\nname = \"api\"\n\n[tool.uv.sources]\ncore = { workspace = true }\nvendored = { path = \"../../third_party/vendored\", editable = true }\n",
		"packages/core/pyproject.toml":        "[project]\nname = \"core\"\n",
		"packages/docs/README.md":             "",
		"third_party/vendored/pyproject.toml": "[project]\n",
		"tools/lint/pyproject.toml":           "[project]\nname = \"lint\"\n",
	}
	check(t, resolve(t, src, nil, []string{"packages/api"}),
		`packages/core <- pyproject.toml: uv workspace member`,
		`third_party/vendored <- packages/api/pyproject.toml: vendored = { path = "../../third_party/vendored" }`,
	)
	// Not a member: the workspace doesn't apply to it.
	check(t, resolve(t, src, nil, []string{"tools/lint"}))
}

func TestMaven(t *testing.T) {
	src := memSource{
		"pom.xml": `<project><artifactId>root</artifactId><modules><module>services/api</module><module>libs/model</module><module>libs/unused</module></modules></project>`,
		"services/api/pom.xml": `<project>
  <parent><artifactId>platform-parent</artifactId><relativePath>../../build/parent/pom.xml</relativePath></parent>
  <artifactId>api</artifactId>
  <dependencies>
    <dependency><groupId>com.x</groupId><artifactId>model</artifactId></dependency>
    <dependency><groupId>org.junit</groupId><artifactId>junit</artifactId></dependency>
  </dependencies>
</project>`,
		"build/parent/pom.xml": `<project><parent><artifactId>root</artifactId><relativePath>../../pom.xml</relativePath></parent><artifactId>platform-parent</artifactId></project>`,
		"libs/model/pom.xml":   `<project><artifactId>model</artifactId></project>`,
		"libs/unused/pom.xml":  `<project><artifactId>unused</artifactId></project>`,
	}
	check(t, resolve(t, src, nil, []string{"services/api"}),
		`build/parent/pom.xml <- services/api/pom.xml: <relativePath>../../build/parent/pom.xml</relativePath>`,
		`libs/model <- services/api/pom.xml: <artifactId>model</artifactId>`,
	)
}

func TestTerraformHelmTypeScript(t *testing.T) {
	src := memSource{
		"infra/envs/prod/main.tf": `
module "vpc" {
  source = "../../modules/vpc"
}
terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}
module "remote" {
  source = "git::https://example.com/x.git"
}
`,
		"infra/modules/vpc/main.tf":    `module "sub" { source = "../subnet" }`,
		"infra/modules/subnet/main.tf": ``,
		"charts/app/Chart.yaml":        "dependencies:\n  - name: common\n    repository: \"file://../common\"\n  - name: redis\n    repository: https://charts.example.com\n",
		"charts/common/Chart.yaml":     "name: common\n",
		"web/tsconfig.json": `{
  // shared settings
  "extends": "../config/tsconfig.base", /* no .json */
  "references": [{ "path": "../shared" }, { "path": "../types/tsconfig.json" }],
  "compilerOptions": { "paths": { "@x/*": ["../x/*"] } }
}`,
		"config/tsconfig.base.json":   `{"extends": "./tsconfig.strict.json"}`,
		"config/tsconfig.strict.json": `{}`,
		"shared/tsconfig.json":        `{}`,
		"types/tsconfig.json":         `{}`,
	}
	check(t, resolve(t, src, nil, []string{"infra/envs", "charts/app", "web"}),
		`charts/common <- charts/app/Chart.yaml: repository: file://../common`,
		`config/tsconfig.base.json <- web/tsconfig.json: "extends": "../config/tsconfig.base"`,
		`infra/modules/subnet <- infra/modules/vpc/main.tf: source = "../subnet"`,
		`infra/modules/vpc <- infra/envs/prod/main.tf: source = "../../modules/vpc"`,
		`shared <- web/tsconfig.json: "references": [{"path": "../shared"}]`,
		`types <- web/tsconfig.json: "references": [{"path": "../types/tsconfig.json"}]`,
	)
}

func TestParentDirFilesAreNotListed(t *testing.T) {
	// The root tsconfig comes with every sparse clone already.
	src := memSource{
		"tsconfig.base.json": `{}`,
		"app/tsconfig.json":  `{"extends": "../tsconfig.base.json"}`,
		"app/sub/x.ts":       ``,
	}
	check(t, resolve(t, src, nil, []string{"app"}))
}
