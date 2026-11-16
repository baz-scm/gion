package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/testgit"
)

func TestGCDeletesOnlyIdleCleanWorkspaces(t *testing.T) {
	t.Setenv("GION_CACHE", filepath.Join(t.TempDir(), "objects"))
	remote, clone := testgit.Serve(t)
	os.WriteFile(filepath.Join(clone, ".gitignore"), []byte("build/\n"), 0o644)
	testgit.Run(t, clone, "add", ".gitignore")
	testgit.Run(t, clone, "commit", "-qm", "ignore build")
	testgit.Run(t, clone, "push", "-q", "origin", "main")
	r := openRepo(t, remote)
	base := t.TempDir()

	ws := map[string]string{}
	for _, name := range []string{"clean", "ignored", "edited", "outside", "recent", "gone"} {
		dir := filepath.Join(base, name)
		if _, _, err := Clone(context.Background(), r, dir, "main", []string{"d"}, true); err != nil {
			t.Fatal(err)
		}
		Touch(dir)
		ws[name] = dir
	}
	os.MkdirAll(filepath.Join(ws["ignored"], "build"), 0o755)
	os.WriteFile(filepath.Join(ws["ignored"], "build", "out.bin"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(ws["edited"], "d", "e.txt"), []byte("e2\n"), 0o644)
	os.MkdirAll(filepath.Join(ws["outside"], "notes"), 0o755) // not in --path d
	os.WriteFile(filepath.Join(ws["outside"], "notes", "todo.md"), []byte("x"), 0o644)
	os.RemoveAll(ws["gone"])

	later := time.Now().Add(8 * 24 * time.Hour)
	reg, _ := cache.WorkspacesDir()
	os.Chtimes(filepath.Join(reg, entryName(ws["recent"])), later, later)

	check := func(res GCResult) {
		t.Helper()
		if want := []string{ws["clean"], ws["ignored"]}; !slices.Equal(res.Removed, want) {
			t.Errorf("removed %v, want %v", res.Removed, want)
		}
		var kept []string
		for _, k := range res.Kept {
			kept = append(kept, filepath.Base(k.Dir)+": "+k.Reason)
		}
		if want := []string{"edited: 1 local change", "outside: 1 local change"}; !slices.Equal(kept, want) {
			t.Errorf("kept %v, want %v", kept, want)
		}
		if res.Active != 1 {
			t.Errorf("active = %d, want 1 (recent)", res.Active)
		}
	}

	res, err := GC(7*24*time.Hour, later, true)
	if err != nil {
		t.Fatal(err)
	}
	check(res)
	for _, d := range ws {
		if _, err := os.Stat(d); err != nil && d != ws["gone"] {
			t.Fatalf("dry run deleted %s", d)
		}
	}

	if res, err = GC(7*24*time.Hour, later, false); err != nil {
		t.Fatal(err)
	}
	check(res)
	for name, d := range ws {
		_, err := os.Stat(d)
		if gone := name == "clean" || name == "ignored" || name == "gone"; gone != os.IsNotExist(err) {
			t.Errorf("%s: exists = %v after gc", name, err == nil)
		}
	}
	if left, _ := os.ReadDir(reg); len(left) != 3 {
		t.Errorf("registry has %d entries, want 3 (edited, outside, recent)", len(left))
	}

	// Nothing is idle yet at the real time.
	if res, err = GC(7*24*time.Hour, time.Now(), false); err != nil || len(res.Removed)+len(res.Kept) != 0 {
		t.Errorf("gc now: %+v, %v", res, err)
	}
}
