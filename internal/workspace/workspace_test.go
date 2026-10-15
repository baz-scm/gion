package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/testgit"
)

func openRepo(t *testing.T, remote string) *repo.Repo {
	t.Helper()
	c, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &repo.Repo{Remote: remote, Cache: c}
	t.Cleanup(func() { r.Close() })
	return r
}

func kinds(cs []FileChange) map[string]Kind {
	m := map[string]Kind{}
	for _, c := range cs {
		m[c.Path] = c.Kind
	}
	return m
}

func TestCloneEditCommit(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "gion")
	t.Setenv("GIT_AUTHOR_EMAIL", "gion@test")
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ws")

	w, res, err := Clone(ctx, r, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 5 {
		t.Fatalf("files = %d, want 5", res.Files)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "run.sh")); fi.Mode().Perm()&0o111 == 0 {
		t.Error("run.sh lost its executable bit")
	}
	if cs, _ := w.Status(); len(cs) != 0 {
		t.Fatalf("fresh clone has changes: %v", kinds(cs))
	}

	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a2\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "new"), 0o755)
	os.WriteFile(filepath.Join(dir, "new", "n.txt"), []byte("n\n"), 0o644)
	os.Remove(filepath.Join(dir, "d", "e.txt"))
	os.Chmod(filepath.Join(dir, "b.txt"), 0o755)

	cs, err := w.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Kind{"a.txt": Modified, "b.txt": Modified, "d/e.txt": Deleted, "new/n.txt": Added}
	if got := kinds(cs); len(got) != len(want) {
		t.Fatalf("status = %v, want %v", got, want)
	} else {
		for p, k := range want {
			if got[p] != k {
				t.Errorf("%s: %s, want %s", p, got[p], k)
			}
		}
	}

	c1, err := w.Commit(ctx, r, "first", "ws-branch")
	if err != nil {
		t.Fatal(err)
	}
	if !c1.Created || w.State.Branch != "ws-branch" {
		t.Fatalf("first commit: created=%v branch=%q", c1.Created, w.State.Branch)
	}
	if cs, _ := w.Status(); len(cs) != 0 {
		t.Fatalf("clean after commit, got %v", kinds(cs))
	}

	// Second commit updates the same branch with CAS.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a3\n"), 0o644)
	c2, err := w.Commit(ctx, r, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Created || c2.Parent != c1.Commit {
		t.Fatalf("second commit: created=%v parent=%s want %s", c2.Created, c2.Parent, c1.Commit)
	}

	testgit.Run(t, clone, "fetch", "-q", "origin", "ws-branch")
	if got := testgit.Run(t, clone, "show", "FETCH_HEAD:a.txt"); got != "a3" {
		t.Errorf("a.txt = %q", got)
	}
	if got := testgit.Run(t, clone, "ls-tree", "FETCH_HEAD", "b.txt"); got[:6] != "100755" {
		t.Errorf("b.txt mode: %s", got)
	}
	testgit.Run(t, clone, "fsck", "--strict")

	// Reopening from a subdirectory finds the workspace.
	w2, err := Find(filepath.Join(dir, "new"))
	if err != nil || w2.State.Branch != "ws-branch" {
		t.Fatalf("Find: %v %+v", err, w2)
	}
}

func TestSparseCloneIgnoresOutOfScope(t *testing.T) {
	remote, _ := testgit.Serve(t)
	r := openRepo(t, remote)
	dir := filepath.Join(t.TempDir(), "ws")

	w, res, err := Clone(context.Background(), r, dir, "main", []string{"d/f"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 {
		t.Fatalf("files = %d, want 1", res.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Error("a.txt materialized outside scope")
	}
	os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644)
	if cs, _ := w.Status(); len(cs) != 0 {
		t.Fatalf("out-of-scope files reported: %v", kinds(cs))
	}
}
