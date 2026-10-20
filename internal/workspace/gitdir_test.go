package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/testgit"
)

func TestGitMirror(t *testing.T) {
	for _, n := range []int{64, 0} {
		t.Run(map[int]string{64: "loose", 0: "packed"}[n], func(t *testing.T) {
			defer func(old int) { packAbove = old }(packAbove)
			packAbove = n
			testGitMirror(t)
		})
	}
}

func testGitMirror(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "gion")
	t.Setenv("GIT_AUTHOR_EMAIL", "gion@test")
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ws")

	w, _, err := Clone(ctx, r, dir, "main", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := filepath.EvalSymlinks(testgit.Run(t, dir, "rev-parse", "--show-toplevel")); got != mustEval(t, dir) {
		t.Errorf("toplevel = %q, want %q", got, dir)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("fresh clone: git status = %q", got)
	}
	if got := testgit.Run(t, dir, "ls-files"); got != "a.txt\nb.txt\nd/e.txt\nd/f/g.txt\nrun.sh" {
		t.Errorf("ls-files = %q", got)
	}
	if got := testgit.Run(t, dir, "ls-files", "-s", "run.sh"); !strings.HasPrefix(got, "100755 ") {
		t.Errorf("run.sh: %s", got)
	}
	if got := testgit.Run(t, dir, "branch", "--show-current"); got != "main" {
		t.Errorf("branch = %q", got)
	}
	// The snapshot tree is the real one when the workspace holds everything.
	if got, want := testgit.Run(t, dir, "rev-parse", "HEAD^{tree}"), testgit.Run(t, clone, "rev-parse", "main^{tree}"); got != want {
		t.Errorf("tree = %s, want %s", got, want)
	}
	testgit.Run(t, dir, "fsck", "--strict")

	write(t, dir, "a.txt", "a2\n")
	write(t, dir, "new.txt", "n\n")
	os.Remove(filepath.Join(dir, "d", "e.txt"))
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "M a.txt\n D d/e.txt\n?? new.txt" {
		t.Errorf("git status = %q", got)
	}
	if got := testgit.Run(t, dir, "diff", "a.txt"); !strings.Contains(got, "-a\n+a2") {
		t.Errorf("git diff = %q", got)
	}
	if got := kinds(mustStatus(t, w)); len(got) != 3 {
		t.Errorf("gion status = %v", got)
	}

	head := testgit.Run(t, dir, "rev-parse", "HEAD")
	if _, err := w.Commit(ctx, r, "edit", "ws-branch", ""); err != nil {
		t.Fatal(err)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("after commit: git status = %q", got)
	}
	if testgit.Run(t, dir, "rev-parse", "HEAD") == head {
		t.Error("HEAD didn't move after commit")
	}
	if got := testgit.Run(t, dir, "branch", "--show-current"); got != "ws-branch" {
		t.Errorf("branch = %q", got)
	}
	if got := testgit.Run(t, dir, "branch", "--list"); got != "* ws-branch" {
		t.Errorf("branches = %q", got)
	}
	testgit.Run(t, clone, "fetch", "-q", "origin", "ws-branch")
	if got, want := testgit.Run(t, dir, "rev-parse", "HEAD^{tree}"), testgit.Run(t, clone, "rev-parse", "FETCH_HEAD^{tree}"); got != want {
		t.Errorf("tree = %s, want %s", got, want)
	}
	testgit.Run(t, dir, "fsck", "--strict")

	// Saving without changes keeps HEAD.
	head = testgit.Run(t, dir, "rev-parse", "HEAD")
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if testgit.Run(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved on a no-op save")
	}
}

func TestGitMirrorSparse(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ws")

	w, _, err := Clone(ctx, r, dir, "main", []string{"d/f"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// Files outside the paths are left out of the snapshot, not deleted.
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("git status = %q", got)
	}
	if got := testgit.Run(t, dir, "ls-files"); got != "a.txt\nb.txt\nd/e.txt\nd/f/g.txt\nrun.sh" {
		t.Errorf("ls-files = %q", got)
	}

	pushUpstream(t, clone, "main", map[string]string{"d/f/g.txt": "g2\n"})
	if _, err := w.Sync(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("after sync: git status = %q", got)
	}
	if got := testgit.Run(t, dir, "show", "HEAD:d/f/g.txt"); got != "g2" {
		t.Errorf("HEAD:d/f/g.txt = %q", got)
	}
	testgit.Run(t, dir, "fsck", "--strict")
}

func TestGitHeadIsUpstream(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "gion")
	t.Setenv("GIT_AUTHOR_EMAIL", "gion@test")
	remote, clone := testgit.Serve(t)
	pushUpstream(t, clone, "main", map[string]string{"x/y.txt": "y\n"}) // main gets a parent
	// .git takes the upstream commit from the shared cache.
	t.Setenv("GION_CACHE", t.TempDir())
	r, err := repo.Open(remote)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	ctx := context.Background()
	main := testgit.Run(t, clone, "rev-parse", "main")

	// A whole workspace's HEAD is the cloned commit, shallow.
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(ctx, r, dir, "main", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := testgit.Run(t, dir, "rev-parse", "HEAD"); got != main {
		t.Errorf("HEAD = %s, want %s", got, main)
	}
	if got := testgit.Run(t, dir, "rev-parse", "GION_HEAD"); got != main {
		t.Errorf("GION_HEAD = %s, want %s", got, main)
	}
	if got := testgit.Run(t, dir, "log", "--format=%s"); got != "upstream" {
		t.Errorf("log = %q", got)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("git status = %q", got)
	}
	testgit.Run(t, dir, "fsck", "--strict")

	write(t, dir, "a.txt", "a2\n")
	if _, err := w.Commit(ctx, r, "edit", "ws-branch", ""); err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, clone, "fetch", "-q", "origin", "ws-branch")
	if got, want := testgit.Run(t, dir, "rev-parse", "HEAD"), testgit.Run(t, clone, "rev-parse", "FETCH_HEAD"); got != want {
		t.Errorf("after commit: HEAD = %s, want %s", got, want)
	}
	testgit.Run(t, dir, "fsck", "--strict")

	// A sparse workspace's HEAD is the upstream commit too, replaced by a
	// snapshot of the tracked files.
	dir = filepath.Join(t.TempDir(), "ws")
	w, _, err = Clone(ctx, r, dir, "main", []string{"d/f"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, rev := range []string{"HEAD", "GION_HEAD"} {
		if got := testgit.Run(t, dir, "rev-parse", rev); got != main {
			t.Errorf("%s = %s, want %s", rev, got, main)
		}
		if got := testgit.Run(t, dir, "rev-parse", "--short=7", rev); got != main[:7] {
			t.Errorf("%s = %s, want %s", rev, got, main[:7])
		}
	}
	if got := testgit.Run(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "x/y.txt") {
		t.Errorf("HEAD tree has files outside the paths: %q", got)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("git status = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "shallow")); err == nil {
		t.Error("sparse workspace has .git/shallow")
	}
	testgit.Run(t, dir, "log", "--all", "--oneline")
	testgit.Run(t, dir, "fsck", "--strict")
	snap := testgit.Run(t, dir, "rev-parse", "refs/replace/"+main)

	// gc packs the refs and keeps the objects; saving puts back the same
	// snapshot.
	testgit.Run(t, dir, "gc", "-q")
	testgit.Run(t, dir, "fsck", "--strict")
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if got := testgit.Run(t, dir, "rev-parse", "HEAD", "refs/replace/"+main); got != main+"\n"+snap {
		t.Errorf("after save: HEAD, replacement = %q", got)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("after gc: git status = %q", got)
	}

	// Syncing moves HEAD to the new upstream commit, with one replace ref.
	pushUpstream(t, clone, "main", map[string]string{"d/f/g.txt": "g2\n"})
	if _, err := w.Sync(ctx, r); err != nil {
		t.Fatal(err)
	}
	main = testgit.Run(t, clone, "rev-parse", "main")
	if got := testgit.Run(t, dir, "rev-parse", "HEAD"); got != main {
		t.Errorf("after sync: HEAD = %s, want %s", got, main)
	}
	if got := testgit.Run(t, dir, "for-each-ref", "--format=%(refname)", "refs/replace/"); got != "refs/replace/"+main {
		t.Errorf("replace refs = %q", got)
	}
	if got := testgit.Run(t, dir, "show", "HEAD:d/f/g.txt"); got != "g2" {
		t.Errorf("HEAD:d/f/g.txt = %q", got)
	}
	if got := testgit.Run(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf("after sync: git status = %q", got)
	}
	testgit.Run(t, dir, "fsck", "--strict")

	// A local commit's parent is the upstream commit.
	write(t, dir, "d/f/g.txt", "g3\n")
	testgit.Run(t, dir, "commit", "-qam", "local")
	if got := testgit.Run(t, dir, "rev-parse", "HEAD^"); got != main {
		t.Errorf("local commit's parent = %s, want %s", got, main)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	got, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCommitHooks(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "gion")
	t.Setenv("GIT_AUTHOR_EMAIL", "gion@test")
	remote, _ := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "ws")
	w, _, err := Clone(ctx, r, dir, "main", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(dir, ".git", "hooks")
	os.MkdirAll(hooks, 0o755)
	// pre-commit sees every change as staged and nothing unstaged, as after
	// `git commit -a`; it fails while a.txt says "bad".
	os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(`#!/bin/sh
git diff --cached --name-status > staged.out
git diff --name-only > unstaged.out
! grep -q bad a.txt
`), 0o755)
	os.WriteFile(filepath.Join(hooks, "commit-msg"), []byte("#!/bin/sh\necho 'Signed-off-by: hook' >> \"$1\"\n"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.out\n"), 0o644)

	write(t, dir, "a.txt", "bad\n")
	write(t, dir, "new.txt", "n\n")
	os.Remove(filepath.Join(dir, "d", "e.txt"))
	var out strings.Builder
	if _, err := w.RunCommitHooks("msg\n", &out); !errors.Is(err, ErrHookFailed) {
		t.Fatalf("err = %v, want ErrHookFailed", err)
	}
	if got := strings.TrimSpace(read(t, dir, "staged.out")); got != "A\t.gitignore\nM\ta.txt\nD\td/e.txt\nA\tnew.txt" {
		t.Errorf("staged = %q", got)
	}
	if got := read(t, dir, "unstaged.out"); got != "" {
		t.Errorf("unstaged = %q", got)
	}
	// The index is back to HEAD.
	if got := testgit.Run(t, dir, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("index left staged: %q", got)
	}

	write(t, dir, "a.txt", "good\n")
	msg, err := w.RunCommitHooks("msg\n", &out)
	if err != nil {
		t.Fatal(err)
	}
	if msg != "msg\nSigned-off-by: hook\n" {
		t.Errorf("msg = %q", msg)
	}

	// core.hooksPath wins over .git/hooks.
	f, _ := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\thooksPath = .hooks\n")
	f.Close()
	write(t, dir, ".hooks/pre-commit", "#!/bin/sh\nexit 3\n")
	os.Chmod(filepath.Join(dir, ".hooks", "pre-commit"), 0o755)
	if _, err := w.RunCommitHooks("msg\n", &out); !errors.Is(err, ErrHookFailed) || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("hooksPath: err = %v", err)
	}
}
