package repo

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/testgit"
)

// TestPatchAppliesWithGit checks the patch against git itself: applied to
// the old commit, it must reproduce the new one exactly.
func TestPatchAppliesWithGit(t *testing.T) {
	remote, clone := testgit.Serve(t)
	ctx := context.Background()
	old := strings.TrimSpace(testgit.Run(t, clone, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(clone, "a.txt"), []byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\n"), 0o644)
	testgit.Run(t, clone, "rm", "-q", "b.txt")
	os.WriteFile(filepath.Join(clone, "d/f/new.txt"), []byte("new\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "bin.dat"), []byte("x\x00y"), 0o644)
	testgit.Run(t, clone, "add", "-A")
	testgit.Run(t, clone, "commit", "-qm", "change")
	testgit.Run(t, clone, "push", "-q", "origin", "HEAD:main")
	nu := strings.TrimSpace(testgit.Run(t, clone, "rev-parse", "HEAD"))

	r := openTest(t, remote)
	oc, err := r.Commit(ctx, plumbing.NewHash(old))
	if err != nil {
		t.Fatal(err)
	}
	nc, err := r.Commit(ctx, plumbing.NewHash(nu))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := r.Changes(ctx, oc.TreeHash, nc.TreeHash, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range ds {
		got = append(got, d.Status()+" "+d.Path)
	}
	want := "M a.txt|D b.txt|A bin.dat|A d/f/new.txt"
	if strings.Join(got, "|") != want {
		t.Fatalf("changes = %v, want %s", got, want)
	}

	var buf bytes.Buffer
	if err := r.WritePatch(ctx, &buf, ds, nil, 3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Binary files") {
		t.Errorf("binary file not marked:\n%s", buf.String())
	}
	// git apply can't build a binary file from "Binary files differ", so
	// check the text files only.
	testgit.Run(t, clone, "checkout", "-q", old)
	text := filterOut(buf.String(), "bin.dat")
	patch := filepath.Join(t.TempDir(), "p.diff")
	os.WriteFile(patch, []byte(text), 0o644)
	testgit.Run(t, clone, "apply", "--index", patch)
	if st := testgit.Run(t, clone, "diff", "--cached", "--name-status", nu, "--", ".", ":!bin.dat"); st != "" {
		t.Fatalf("patched tree differs from new commit:\n%s\npatch:\n%s", st, text)
	}

	// --path style filtering only enters the scoped directory.
	scoped, err := r.Changes(ctx, oc.TreeHash, nc.TreeHash,
		func(dir string) bool { return dir == "d" || strings.HasPrefix(dir, "d/") },
		func(p string) bool { return strings.HasPrefix(p, "d/") })
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].Path != "d/f/new.txt" {
		t.Fatalf("scoped = %+v", scoped)
	}
}

// filterOut drops the file sections of a patch whose path is p.
func filterOut(patch, p string) string {
	var out []string
	skip := false
	for _, l := range strings.SplitAfter(patch, "\n") {
		if strings.HasPrefix(l, "diff --git ") {
			skip = strings.HasSuffix(strings.TrimSpace(l), " b/"+p)
		}
		if !skip {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

// TestResolveParentSteps matches git rev-parse for ~ and ^ suffixes,
// including a merge commit's second parent.
func TestResolveParentSteps(t *testing.T) {
	remote, clone := testgit.Serve(t)
	ctx := context.Background()
	testgit.Run(t, clone, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(clone, "side.txt"), []byte("s\n"), 0o644)
	testgit.Run(t, clone, "add", "side.txt")
	testgit.Run(t, clone, "commit", "-qm", "side")
	testgit.Run(t, clone, "checkout", "-q", "main")
	for i := 0; i < 2; i++ {
		os.WriteFile(filepath.Join(clone, "a.txt"), []byte(strings.Repeat("x", i+1)), 0o644)
		testgit.Run(t, clone, "commit", "-qam", "main")
	}
	testgit.Run(t, clone, "merge", "-q", "--no-ff", "-m", "merge", "side")
	testgit.Run(t, clone, "push", "-q", "origin", "main")

	r := openTest(t, remote)
	for _, rev := range []string{"main~1", "main^", "main^2", "main~2", "main^1~1", "HEAD~3", "main^0", "main~"} {
		want := strings.TrimSpace(testgit.Run(t, clone, "rev-parse", strings.Replace(rev, "HEAD", "main", 1)))
		got, err := r.Resolve(ctx, rev)
		if err != nil || got.String() != want {
			t.Errorf("Resolve(%s) = %s, %v; want %s", rev, got, err, want)
		}
	}
	if _, err := r.Resolve(ctx, "main~10"); err == nil {
		t.Error("walking past the root should fail")
	}
}
