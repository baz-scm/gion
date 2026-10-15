package repo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/AnyGitForge/AnyForge/cli/internal/cache"
)

// The git CLI is only a test fixture: it seeds and serves a bare repo over
// git:// so we exercise the real wire protocol.

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// serve seeds a bare repo and serves it with git daemon. It returns the
// remote URL and a working clone for simulating other writers.
func serve(t *testing.T) (remote, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	gitRun(t, dir, "init", "-q", "-b", "main", seed)
	for p, c := range map[string]string{"a.txt": "a\n", "b.txt": "b\n", "d/e.txt": "e\n"} {
		os.MkdirAll(filepath.Join(seed, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(seed, p), []byte(c), 0o644)
	}
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "commit", "-qm", "seed")
	srv := filepath.Join(dir, "srv")
	os.MkdirAll(srv, 0o755)
	bare := filepath.Join(srv, "r.git")
	gitRun(t, dir, "clone", "-q", "--bare", seed, bare)
	gitRun(t, bare, "config", "uploadpack.allowFilter", "true")
	gitRun(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	d := exec.Command("git", "daemon", "--reuseaddr", "--export-all", "--enable=receive-pack",
		"--base-path="+srv, "--listen=127.0.0.1", fmt.Sprintf("--port=%d", port), srv)
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Process.Kill(); d.Wait() })
	remote = fmt.Sprintf("git://127.0.0.1:%d/r.git", port)
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			c.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	clone = filepath.Join(dir, "clone")
	gitRun(t, dir, "clone", "-q", remote, clone)
	return remote, clone
}

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

var sig = object.Signature{Name: "any", Email: "any@test", When: time.Unix(1_700_000_000, 0)}

// upstreamWrite simulates another writer pushing to main from a clone.
func upstreamWrite(t *testing.T, clone, path, content string) {
	t.Helper()
	gitRun(t, clone, "pull", "-q", "--rebase", "origin", "main")
	os.WriteFile(filepath.Join(clone, path), []byte(content), 0o644)
	gitRun(t, clone, "commit", "-qam", "upstream "+path)
	gitRun(t, clone, "push", "-q", "origin", "HEAD:main")
}

func TestCommitToRetriesWhenUpstreamTouchesOtherPaths(t *testing.T) {
	remote, clone := serve(t)
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
	gitRun(t, clone, "pull", "-q", "origin", "main")
	for p, want := range map[string]string{"a.txt": "a2", "b.txt": "b2"} {
		if got := gitRun(t, clone, "show", "HEAD:"+p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}
}

func TestCommitToConflictsWhenUpstreamTouchesSamePath(t *testing.T) {
	remote, clone := serve(t)
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
	gitRun(t, clone, "pull", "-q", "origin", "main")
	if got := gitRun(t, clone, "show", "HEAD:a.txt"); got != "theirs" {
		t.Errorf("upstream write lost: a.txt = %q", got)
	}
}

func TestCommitNewBranchPrunesEmptyDirs(t *testing.T) {
	remote, clone := serve(t)
	r := openTest(t, remote)

	_, err := r.CommitAndPush(context.Background(), CommitOptions{
		Changes: []Change{{Path: "d/e.txt", Delete: true}, {Path: "x/y/z.txt", Data: []byte("z\n")}},
		Message: "reshape", Author: sig, Onto: "main", Branch: "feat",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, clone, "fetch", "-q", "origin", "feat")
	got := gitRun(t, clone, "ls-tree", "-r", "--name-only", "FETCH_HEAD")
	if want := "a.txt\nb.txt\nx/y/z.txt"; got != want {
		t.Errorf("tree:\n%s\nwant:\n%s", got, want)
	}
	gitRun(t, clone, "fsck", "--strict")
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
