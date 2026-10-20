package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baz-scm/gion/internal/index"
	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/testgit"
)

func TestIndexDeltaMatchesFullAndGrepLocal(t *testing.T) {
	t.Setenv("GION_CACHE", filepath.Join(t.TempDir(), "objects"))
	remote, clone := testgit.Serve(t)
	ctx := context.Background()
	open := func() *repo.Repo {
		r, err := repo.Open(remote)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		return r
	}
	r := open()

	c1, err := r.Resolve(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	idx1, how, err := index.At(ctx, r, "HEAD", c1)
	if err != nil || how != index.Full || len(idx1.Entries) != 5 {
		t.Fatalf("first index: how=%s n=%d err=%v", how, len(idx1.Entries), err)
	}
	if _, how, _ := index.At(ctx, r, "HEAD", c1); how != index.Hit {
		t.Errorf("same commit: how=%s, want cached", how)
	}

	// Upstream: modify, add in a new dir, delete.
	os.WriteFile(filepath.Join(clone, "a.txt"), []byte("needle here\n"), 0o644)
	os.MkdirAll(filepath.Join(clone, "n", "m"), 0o755)
	os.WriteFile(filepath.Join(clone, "n", "m", "x.go"), []byte("package x\n// Needle\n"), 0o644)
	testgit.Run(t, clone, "rm", "-q", "d/e.txt")
	testgit.Run(t, clone, "add", "-A")
	testgit.Run(t, clone, "commit", "-qm", "up")
	testgit.Run(t, clone, "push", "-q", "origin", "HEAD:main")

	c2, _ := r.Resolve(ctx, "main")
	idx2, how, err := index.At(ctx, r, "HEAD", c2)
	if err != nil || how != index.Delta {
		t.Fatalf("second index: how=%s err=%v", how, err)
	}

	// An older commit gets its own slot, built from HEAD's, and leaves
	// HEAD's index alone.
	old, how, err := index.At(ctx, r, c1.String(), c1)
	if err != nil || how != index.Delta || len(old.Entries) != len(idx1.Entries) {
		t.Fatalf("old commit: how=%s n=%d err=%v", how, len(old.Entries), err)
	}
	if _, how, _ := index.At(ctx, r, "HEAD", c2); how != index.Hit {
		t.Errorf("HEAD after old commit: how=%s, want cached", how)
	}
	if _, how, _ := index.At(ctx, r, c1.String()[:12], c1); how != index.Hit {
		t.Errorf("old commit again: how=%s, want cached", how)
	}

	// A fresh cache must build the identical index from scratch.
	t.Setenv("GION_CACHE", filepath.Join(t.TempDir(), "objects2"))
	full, how, err := index.At(ctx, open(), "HEAD", c2)
	if err != nil || how != index.Full {
		t.Fatalf("fresh index: how=%s err=%v", how, err)
	}
	if len(full.Entries) != len(idx2.Entries) {
		t.Fatalf("delta has %d entries, full has %d", len(idx2.Entries), len(full.Entries))
	}
	for i := range full.Entries {
		if full.Entries[i] != idx2.Entries[i] {
			t.Errorf("entry %d: delta %+v, full %+v", i, idx2.Entries[i], full.Entries[i])
		}
	}

	res, err := Grep(ctx, r, idx2, GrepOptions{Pattern: "needle", Fold: true, MaxFiles: 100, Strategy: Auto})
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != Local || len(res.Matches) != 2 {
		t.Fatalf("grep: strategy=%s matches=%+v", res.Strategy, res.Matches)
	}
	if m := res.Matches[1]; m.Path != "n/m/x.go" || m.Line != 2 || m.Text != "// Needle" {
		t.Errorf("match = %+v", m)
	}

	pm, _ := CompileGlob("n")
	res, _ = Grep(ctx, r, idx2, GrepOptions{Pattern: "needle", Fold: true, Paths: []PathMatcher{pm}, MaxFiles: 100, Strategy: Auto})
	if len(res.Matches) != 1 || res.Scanned != 1 {
		t.Errorf("--path n: scanned=%d matches=%d", res.Scanned, len(res.Matches))
	}

	// Too many candidates, non-GitHub remote: a clear error, not a silent scan.
	if _, err := Grep(ctx, r, idx2, GrepOptions{Pattern: "needle", MaxFiles: 1, Strategy: Auto}); err == nil {
		t.Error("expected an error when code search is unavailable")
	}

	// --path scoping fetches only the named subtrees, no whole-repo index.
	t.Setenv("GION_CACHE", filepath.Join(t.TempDir(), "objects3"))
	sub, err := index.Under(ctx, open(), "HEAD", c2, LiteralDirs([]string{"n/m", "a.txt", "n/*.go", "missing/x"}))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range sub.Entries {
		got = append(got, e.Path)
	}
	if len(got) != 2 || got[0] != "a.txt" || got[1] != "n/m/x.go" || sub.Commit != c2 {
		t.Errorf("Under = %v @ %s", got, sub.Commit)
	}
}
