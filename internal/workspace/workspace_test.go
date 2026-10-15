package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	c1, err := w.Commit(ctx, r, "first", "ws-branch", "")
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
	c2, err := w.Commit(ctx, r, "second", "", "")
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

func write(t *testing.T, dir, p, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pushUpstream commits files (content "" deletes) to branch from the clone.
func pushUpstream(t *testing.T, clone, branch string, files map[string]string) {
	t.Helper()
	testgit.Run(t, clone, "fetch", "-q", "origin", branch)
	testgit.Run(t, clone, "checkout", "-q", "-B", branch, "FETCH_HEAD")
	for p, c := range files {
		if c == "" {
			testgit.Run(t, clone, "rm", "-q", p)
			continue
		}
		write(t, clone, p, c)
		testgit.Run(t, clone, "add", p)
	}
	testgit.Run(t, clone, "commit", "-qm", "upstream")
	testgit.Run(t, clone, "push", "-q", "origin", "HEAD:"+branch)
}

func read(t *testing.T, dir, p string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatusHonorsGitignore(t *testing.T) {
	remote, _ := testgit.Serve(t)
	r := openRepo(t, remote)
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(context.Background(), r, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	write(t, dir, ".gitignore", "build/\n*.log\na.txt\nd/\n")
	write(t, dir, "build/out.bin", "x")
	write(t, dir, "x.log", "x")
	write(t, dir, "keep.txt", "k")
	write(t, dir, "sub/.gitignore", "*.tmp\n")
	write(t, dir, "sub/s.tmp", "x")
	write(t, dir, "sub/s.go", "x")
	write(t, dir, "a.txt", "tracked files stay tracked\n")          // tracked, matches a.txt
	write(t, dir, "d/e.txt", "tracked inside an ignored dir\n")     // tracked, under ignored d/
	write(t, dir, "d/new.txt", "untracked inside an ignored dir\n") // ignored

	cs, err := w.Status()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Kind{".gitignore": Added, "keep.txt": Added, "sub/.gitignore": Added, "sub/s.go": Added, "a.txt": Modified, "d/e.txt": Modified}
	got := kinds(cs)
	if len(got) != len(want) {
		t.Fatalf("status = %v\nwant     %v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s: %q, want %q", p, got[p], k)
		}
	}
}

func TestSyncPullsUpstreamAndKeepsLocalEdits(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(context.Background(), r, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}

	write(t, dir, "a.txt", "local edit\n")
	pushUpstream(t, clone, "main", map[string]string{"b.txt": "b2\n", "d/f/g.txt": "", "n/new.txt": "new\n"})

	res, err := w.Sync(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Updated, ","); got != "b.txt,n/new.txt" {
		t.Errorf("updated = %s", got)
	}
	if got := strings.Join(res.Removed, ","); got != "d/f/g.txt" {
		t.Errorf("removed = %s", got)
	}
	if read(t, dir, "b.txt") != "b2\n" || read(t, dir, "n/new.txt") != "new\n" {
		t.Error("upstream content not on disk")
	}
	if _, err := os.Stat(filepath.Join(dir, "d", "f")); !os.IsNotExist(err) {
		t.Error("empty dir d/f not pruned")
	}
	if read(t, dir, "a.txt") != "local edit\n" {
		t.Error("local edit lost")
	}
	cs, _ := w.Status()
	if got := kinds(cs); len(got) != 1 || got["a.txt"] != Modified {
		t.Errorf("status after sync = %v, want only a.txt modified", got)
	}
	tip := testgit.Run(t, clone, "rev-parse", "HEAD")
	if w.State.Base != tip {
		t.Errorf("base = %s, want %s", w.State.Base, tip)
	}

	// Reopened state agrees, and a second sync is a no-op.
	w2, _ := Find(dir)
	if res, err := w2.Sync(context.Background(), r); err != nil || res.From != res.To {
		t.Errorf("second sync: %v %+v", err, res)
	}
}

func TestSyncStopsOnConflict(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(context.Background(), r, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := w.State.Base

	write(t, dir, "a.txt", "ours\n")
	pushUpstream(t, clone, "main", map[string]string{"a.txt": "theirs\n", "b.txt": "b2\n"})

	_, err = w.Sync(context.Background(), r)
	if !errors.Is(err, ErrLocalConflict) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("err = %v, want conflict on a.txt", err)
	}
	if read(t, dir, "a.txt") != "ours\n" || read(t, dir, "b.txt") != "b\n" {
		t.Error("sync changed files despite the conflict")
	}
	if w.State.Base != base {
		t.Error("base moved despite the conflict")
	}
}

func TestSyncRespectsSparseScope(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(context.Background(), r, dir, "main", []string{"d"})
	if err != nil {
		t.Fatal(err)
	}
	pushUpstream(t, clone, "main", map[string]string{"a.txt": "outside\n", "d/e.txt": "inside\n"})

	res, err := w.Sync(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Updated, ","); got != "d/e.txt" {
		t.Errorf("updated = %s, want only d/e.txt", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Error("out-of-scope a.txt was written")
	}
}

func TestCommitPullsUpstreamOnOtherPaths(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "gion")
	t.Setenv("GIT_AUTHOR_EMAIL", "gion@test")
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(ctx, r, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "a.txt", "first\n")
	if _, err := w.Commit(ctx, r, "first", "ws", ""); err != nil {
		t.Fatal(err)
	}

	// Someone else pushes to our branch, on a different file.
	pushUpstream(t, clone, "ws", map[string]string{"b.txt": "theirs\n"})

	write(t, dir, "a.txt", "second\n")
	res, err := w.Commit(ctx, r, "second", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Pulled.Updated, ","); got != "b.txt" {
		t.Errorf("pulled = %q, want b.txt", got)
	}
	if read(t, dir, "b.txt") != "theirs\n" {
		t.Error("upstream b.txt not on disk")
	}
	if cs, _ := w.Status(); len(cs) != 0 {
		t.Errorf("status after commit = %v, want clean", kinds(cs))
	}
	if w.State.Base != res.Commit.String() {
		t.Error("base is not the pushed commit")
	}
}

func TestSwitchAndCommitToExistingBranch(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "gion")
	t.Setenv("GIT_AUTHOR_EMAIL", "gion@test")
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()

	// A feature branch that differs from main.
	testgit.Run(t, clone, "checkout", "-q", "-b", "feature")
	write(t, clone, "b.txt", "feature b\n")
	write(t, clone, "f/only.txt", "only on feature\n")
	testgit.Run(t, clone, "add", "-A")
	testgit.Run(t, clone, "commit", "-qm", "feature work")
	testgit.Run(t, clone, "push", "-q", "origin", "feature")

	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(ctx, r, dir, "main", nil)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "a.txt", "on feature\n") // local edit survives the switch

	if _, err := w.Switch(ctx, r, "feature"); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "b.txt") != "feature b\n" || read(t, dir, "f/only.txt") != "only on feature\n" {
		t.Error("feature files not on disk after switch")
	}
	if read(t, dir, "a.txt") != "on feature\n" {
		t.Error("local edit lost on switch")
	}
	if cs, _ := w.Status(); len(cs) != 1 || cs[0].Path != "a.txt" {
		t.Errorf("status after switch = %v", kinds(cs))
	}

	// Commit now goes onto feature, on top of its tip.
	featureTip := testgit.Run(t, clone, "rev-parse", "feature")
	res, err := w.Commit(ctx, r, "on feature", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.Ref.Short() != "feature" || res.Parent.String() != featureTip {
		t.Fatalf("commit: created=%v ref=%s parent=%s want parent %s", res.Created, res.Ref, res.Parent, featureTip)
	}

	// --to onto a branch where our file differs from our base: refused,
	// since overwriting main's a.txt may not be what was meant.
	write(t, dir, "a.txt", "to main\n")
	if _, err := w.Commit(ctx, r, "to main", "", "main"); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("--to main touching a.txt: err = %v, want conflict", err)
	}
	write(t, dir, "a.txt", "on feature\n") // back to what we committed

	// --to onto main with a file that's the same on both branches works,
	// and the workspace then mirrors main.
	write(t, dir, "d/e.txt", "e via --to\n")
	res, err = w.Commit(ctx, r, "to main", "", "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref.Short() != "main" || w.State.Branch != "main" {
		t.Fatalf("--to main: ref=%s state=%s", res.Ref, w.State.Branch)
	}
	testgit.Run(t, clone, "fetch", "-q", "origin")
	if got := testgit.Run(t, clone, "show", "origin/main:d/e.txt"); got != "e via --to" {
		t.Errorf("main d/e.txt = %q", got)
	}
	if read(t, dir, "b.txt") != "b\n" || read(t, dir, "a.txt") != "a\n" {
		t.Error("workspace doesn't mirror main after --to")
	}
	if _, err := os.Stat(filepath.Join(dir, "f", "only.txt")); !os.IsNotExist(err) {
		t.Error("feature-only file still on disk after moving to main")
	}
	if cs, _ := w.Status(); len(cs) != 0 {
		t.Errorf("status after --to = %v, want clean", kinds(cs))
	}

	if _, err := w.Switch(ctx, r, "nope"); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("switch to missing branch: %v", err)
	}
}
