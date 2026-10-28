package deps

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/testgit"
)

func TestRepoSource(t *testing.T) {
	remote, clone := testgit.Serve(t)
	for p, c := range map[string]string{
		"package.json":              `{"workspaces": ["packages/*"]}`,
		"bff/package.json":          `{"dependencies": {"@baz/shared-fixer": "file:../shared/fixer", "ui": "workspace:*"}}`,
		"shared/fixer/package.json": `{"name": "@baz/shared-fixer"}`,
		"packages/ui/package.json":  `{"name": "ui"}`,
		"packages/ui/tsconfig.json": `{"extends": "../../tsconfig.base.json"}`,
		"tsconfig.base.json":        `{}`,
	} {
		os.MkdirAll(filepath.Join(clone, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(clone, p), []byte(c), 0o644)
	}
	testgit.Run(t, clone, "add", "-A")
	testgit.Run(t, clone, "commit", "-qm", "monorepo")
	testgit.Run(t, clone, "push", "-q", "origin", "HEAD:main")

	c, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &repo.Repo{Remote: remote, Cache: c}
	defer r.Close()
	ctx := context.Background()
	_, root, err := r.RootTree(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	found, err := Resolve(ctx, NewRepoSource(r, root), nil, []string{"bff"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range found {
		got = append(got, f.Path+" <- "+f.Manifest+": "+f.Spec)
	}
	sort.Strings(got)
	check(t, got,
		`packages/ui <- bff/package.json: "ui": "workspace:*"`,
		`shared/fixer <- bff/package.json: "@baz/shared-fixer": "file:../shared/fixer"`,
	)
}
