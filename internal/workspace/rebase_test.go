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

// featureWorkspace clones paths (nil: all), then pushes one commit per
// edit to a new branch "feat" from the workspace.
func featureWorkspace(t *testing.T, paths []string, edits ...map[string]string) (*Workspace, *repo.Repo, string) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@test")
	remote, clone := testgit.Serve(t)
	r := openRepo(t, remote)
	ctx := context.Background()
	w, _, err := Clone(ctx, r, filepath.Join(t.TempDir(), "ws"), "main", paths)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range edits {
		for p, c := range e {
			write(t, w.Dir, p, c)
		}
		branch := ""
		if i == 0 {
			branch = "feat"
		}
		if _, err := w.Commit(ctx, r, "feat "+string(rune('1'+i)), branch, ""); err != nil {
			t.Fatal(err)
		}
	}
	return w, r, clone
}

func revParse(t *testing.T, clone, rev string) string {
	t.Helper()
	testgit.Run(t, clone, "fetch", "-q", "origin")
	return testgit.Run(t, clone, "rev-parse", rev)
}

func TestRebaseClean(t *testing.T) {
	w, r, clone := featureWorkspace(t, nil, map[string]string{"a.txt": "feature a\n"}, map[string]string{"b.txt": "feature b\n"})
	pushUpstream(t, clone, "main", map[string]string{"d/e.txt": "upstream e\n"})
	ctx := context.Background()

	res, err := w.Rebase(ctx, r, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != nil || res.Picked != 2 || res.Onto != "main" {
		t.Fatalf("result = %+v", res)
	}
	tip := revParse(t, clone, "origin/feat")
	if tip != res.NewTip.String() || w.State.Base != tip {
		t.Fatalf("feat = %s, result %s, base %s", tip, res.NewTip, w.State.Base)
	}
	if got, want := revParse(t, clone, "origin/feat~2"), revParse(t, clone, "origin/main"); got != want {
		t.Fatalf("feat~2 = %s, want main %s", got, want)
	}
	if msg := testgit.Run(t, clone, "log", "-1", "--format=%an %s", "origin/feat~1"); msg != "dev feat 1" {
		t.Errorf("author and message not kept: %q", msg)
	}
	if read(t, w.Dir, "d/e.txt") != "upstream e\n" || read(t, w.Dir, "a.txt") != "feature a\n" {
		t.Error("workspace files not at the new tip")
	}
	if cs := mustStatus(t, w); len(cs) != 0 {
		t.Fatalf("dirty after rebase: %v", kinds(cs))
	}

	// Again: nothing to do.
	res, err = w.Rebase(ctx, r, "main")
	if err != nil || !res.UpToDate {
		t.Fatalf("second rebase: %+v %v", res, err)
	}
}

func TestRebaseConflictContinue(t *testing.T) {
	w, r, clone := featureWorkspace(t, nil, map[string]string{"a.txt": "1\nfeature\n3\n"}, map[string]string{"b.txt": "feature b\n"})
	pushUpstream(t, clone, "main", map[string]string{"a.txt": "1\nupstream\n3\n"})
	ctx := context.Background()
	before := revParse(t, clone, "origin/feat")

	res, err := w.Rebase(ctx, r, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped == nil || len(res.Stopped.Conflicts) != 1 || res.Stopped.Conflicts[0].Path != "a.txt" || res.Remaining != 1 {
		t.Fatalf("want a stop on a.txt with 1 commit left, got %+v", res)
	}
	got := read(t, w.Dir, "a.txt")
	// The seed's a.txt is one line, so the whole file is the conflict.
	for _, s := range []string{"<<<<<<< main\n1\nupstream\n3\n||||||| base ", "\na\n=======\n1\nfeature\n3\n>>>>>>> ", " feat 1\n"} {
		if !strings.Contains(got, s) {
			t.Errorf("a.txt lacks %q:\n%s", s, got)
		}
	}
	if revParse(t, clone, "origin/feat") != before {
		t.Fatal("branch moved before the rebase finished")
	}
	if _, err := w.Commit(ctx, r, "x", "", ""); !errors.Is(err, ErrRebaseInProgress) {
		t.Fatalf("commit during rebase: %v", err)
	}
	if _, err := w.RebaseContinue(ctx, r); err == nil || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("continue with markers: %v", err)
	}

	write(t, w.Dir, "a.txt", "1\nboth\n3\n")
	res, err = w.RebaseContinue(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != nil || res.Picked != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got, want := revParse(t, clone, "origin/feat~2"), revParse(t, clone, "origin/main"); got != want {
		t.Fatalf("feat~2 = %s, want main %s", got, want)
	}
	if s := testgit.Run(t, clone, "show", "origin/feat:a.txt"); s != "1\nboth\n3" {
		t.Errorf("a.txt on feat = %q", s)
	}
	if w.Rebasing() || len(mustStatus(t, w)) != 0 {
		t.Fatal("rebase state or edits left over")
	}
}

func TestRebaseAbortAndSkip(t *testing.T) {
	w, r, clone := featureWorkspace(t, []string{"d"}, map[string]string{"a.txt": "feature\n", "d/e.txt": "feature e\n"})
	// o/x.txt is outside the workspace; a conflict there must still show up.
	pushUpstream(t, clone, "main", map[string]string{"o/x.txt": "base\n"})
	ctx := context.Background()
	// Put a feat commit on o/x.txt from elsewhere, then conflict it on main.
	pushUpstream(t, clone, "feat", map[string]string{"o/x.txt": "feat\n"})
	if _, err := w.Sync(ctx, r); err != nil {
		t.Fatal(err)
	}
	pushUpstream(t, clone, "main", map[string]string{"a.txt": "upstream\n", "o/x.txt": "main\n"})

	res, err := w.Rebase(ctx, r, "main")
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped == nil {
		t.Fatalf("expected a conflict, got %+v", res)
	}
	if !strings.Contains(read(t, w.Dir, "a.txt"), "<<<<<<<") {
		t.Fatal("a.txt has no markers")
	}

	if err := w.RebaseAbort(ctx, r); err != nil {
		t.Fatal(err)
	}
	if read(t, w.Dir, "a.txt") != "feature\n" || w.Rebasing() || len(mustStatus(t, w)) != 0 {
		t.Fatal("abort didn't restore the workspace")
	}

	// Now skip the first commit; the second (o/x.txt) conflicts outside the
	// workspace's paths, so that file joins it.
	if res, err = w.Rebase(ctx, r, "main"); err != nil || res.Stopped == nil {
		t.Fatalf("rebase again: %+v %v", res, err)
	}
	res, err = w.RebaseSkip(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped == nil || res.Stopped.Conflicts[0].Path != "o/x.txt" {
		t.Fatalf("want a stop on o/x.txt, got %+v", res)
	}
	if !strings.Contains(read(t, w.Dir, "o/x.txt"), "<<<<<<<") {
		t.Fatal("o/x.txt conflict not written")
	}
	os.Remove(filepath.Join(w.Dir, "o/x.txt")) // resolve by deleting
	res, err = w.RebaseContinue(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Picked != 1 {
		t.Fatalf("result = %+v", res)
	}
	revParse(t, clone, "origin/feat") // fetches
	if out, err := testgit.RunErr(clone, "cat-file", "-e", "origin/feat:o/x.txt"); err == nil {
		t.Fatalf("o/x.txt should be deleted on feat: %s", out)
	}
	if s := testgit.Run(t, clone, "show", "origin/feat:a.txt"); s != "upstream" {
		t.Errorf("a.txt on feat = %q (skipped commit should be gone)", s)
	}
}

func TestRebaseFastForward(t *testing.T) {
	w, r, clone := featureWorkspace(t, nil, map[string]string{"a.txt": "feature\n"})
	ctx := context.Background()
	// Make main contain feat plus more: feat is just behind.
	testgit.Run(t, clone, "fetch", "-q", "origin")
	testgit.Run(t, clone, "checkout", "-q", "-B", "main", "origin/feat")
	write(t, clone, "b.txt", "more\n")
	testgit.Run(t, clone, "commit", "-qam", "more")
	testgit.Run(t, clone, "push", "-q", "origin", "main")

	res, err := w.Rebase(ctx, r, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := revParse(t, clone, "origin/feat"), revParse(t, clone, "origin/main"); got != want || res.Picked != 0 {
		t.Fatalf("feat = %s, want main %s (%+v)", got, want, res)
	}
	if read(t, w.Dir, "b.txt") != "more\n" {
		t.Error("workspace not updated")
	}
}
