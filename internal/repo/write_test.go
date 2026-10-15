package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/testgit"
)

func openTest(t *testing.T, remote string) *Repo {
	t.Helper()
	c, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := &Repo{Remote: remote, Cache: c}
	t.Cleanup(func() { r.Close() })
	return r
}

var sig = object.Signature{Name: "gion", Email: "gion@test", When: time.Unix(1_700_000_000, 0)}

// upstreamWrite simulates another writer pushing to main from a clone.
func upstreamWrite(t *testing.T, clone, path, content string) {
	t.Helper()
	testgit.Run(t, clone, "pull", "-q", "--rebase", "origin", "main")
	os.WriteFile(filepath.Join(clone, path), []byte(content), 0o644)
	testgit.Run(t, clone, "commit", "-qam", "upstream "+path)
	testgit.Run(t, clone, "push", "-q", "origin", "HEAD:main")
}

func TestCommitToRetriesWhenUpstreamTouchesOtherPaths(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openTest(t, remote)
	ctx := context.Background()

	testHookBeforePush = func(attempt int) {
		if attempt == 1 {
			upstreamWrite(t, clone, "b.txt", "b2\n")
		}
	}
	defer func() { testHookBeforePush = nil }()

	res, err := r.CommitAndPush(ctx, CommitOptions{
		Changes: []Change{{Path: "a.txt", Data: []byte("a2\n")}},
		Message: "ours", Author: sig, To: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", res.Attempts)
	}
	testgit.Run(t, clone, "pull", "-q", "origin", "main")
	for p, want := range map[string]string{"a.txt": "a2", "b.txt": "b2"} {
		if got := testgit.Run(t, clone, "show", "HEAD:"+p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}

func TestCommitToConflictsWhenUpstreamTouchesSamePath(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openTest(t, remote)

	testHookBeforePush = func(attempt int) {
		if attempt == 1 {
			upstreamWrite(t, clone, "a.txt", "theirs\n")
		}
	}
	defer func() { testHookBeforePush = nil }()

	_, err := r.CommitAndPush(context.Background(), CommitOptions{
		Changes: []Change{{Path: "a.txt", Data: []byte("ours\n")}},
		Message: "ours", Author: sig, To: "main",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	testgit.Run(t, clone, "pull", "-q", "origin", "main")
	if got := testgit.Run(t, clone, "show", "HEAD:a.txt"); got != "theirs" {
		t.Errorf("upstream write lost: a.txt = %q", got)
	}
}

func TestCommitNewBranchPrunesEmptyDirs(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openTest(t, remote)

	_, err := r.CommitAndPush(context.Background(), CommitOptions{
		Changes: []Change{{Path: "d/e.txt", Delete: true}, {Path: "d/f/g.txt", Delete: true}, {Path: "x/y/z.txt", Data: []byte("z\n")}},
		Message: "reshape", Author: sig, Onto: "main", Branch: "feat",
	})
	if err != nil {
		t.Fatal(err)
	}
	testgit.Run(t, clone, "fetch", "-q", "origin", "feat")
	got := testgit.Run(t, clone, "ls-tree", "-r", "--name-only", "FETCH_HEAD")
	if want := "a.txt\nb.txt\nrun.sh\nx/y/z.txt"; got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	testgit.Run(t, clone, "fsck", "--strict")
}

func TestSortEntriesGitOrder(t *testing.T) {
	es := []object.TreeEntry{
		{Name: "a", Mode: filemode.Dir},
		{Name: "a.b", Mode: filemode.Regular},
		{Name: "a-b", Mode: filemode.Regular},
	}
	sortEntries(es)
	// "a-b" < "a.b" < "a/" because '-' (0x2d) < '.' (0x2e) < '/' (0x2f).
	if es[0].Name != "a-b" || es[1].Name != "a.b" || es[2].Name != "a" {
		t.Errorf("order = %v %v %v", es[0].Name, es[1].Name, es[2].Name)
	}
}
