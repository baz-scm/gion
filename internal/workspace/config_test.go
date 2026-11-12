package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baz-scm/gion/internal/testgit"
)

// The repo's own files in .gion (its gion config) are ordinary files of a
// workspace: cloned even when sparse, tracked, committable, seen by git.
// gion's state files beside them stay out of all of that.
func TestRepoFilesInStateDir(t *testing.T) {
	remote, clone := testgit.Serve(t)
	pushUpstream(t, clone, "main", map[string]string{".gion/projects.toml": "[projects]\nf = [\"d/f\"]\n"})
	r := openRepo(t, remote)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(ctx, r, dir, "main", []string{"d/f"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, dir, ".gion/projects.toml"), "[projects]") {
		t.Fatal("sparse clone left out .gion/projects.toml")
	}
	if cs := mustStatus(t, w); len(cs) != 0 {
		t.Fatalf("fresh clone has changes: %v", kinds(cs))
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("git status on a fresh clone: %q", got)
	}

	write(t, dir, ".gion/projects.toml", "[projects]\nf = [\"d\"]\n")
	if got := kinds(mustStatus(t, w)); len(got) != 1 || got[".gion/projects.toml"] != Modified {
		t.Fatalf("status = %v, want only .gion/projects.toml modified", got)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "M .gion/projects.toml" {
		t.Fatalf("git status = %q", got)
	}
	if _, err := w.Commit(ctx, r, "widen f", "cfg", ""); err != nil {
		t.Fatal(err)
	}
	if got := testgit.Run(t, clone, "ls-remote", "origin", "cfg"); got == "" {
		t.Fatal("branch cfg not pushed")
	}

	if _, err := w.Add(ctx, r, []string{".gion/state.json"}); err == nil {
		t.Error("add .gion/state.json: no error")
	}
}
