package repo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baz-scm/gion/internal/testgit"
)

func TestRenameBranch(t *testing.T) {
	remote, clone := testgit.Serve(t)
	r := openTest(t, remote)
	ctx := context.Background()
	testgit.Run(t, clone, "push", "-q", "origin", "HEAD:refs/heads/old", "HEAD:refs/heads/taken")
	head := testgit.Run(t, clone, "rev-parse", "HEAD")

	tip, err := r.RenameBranch(ctx, "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if tip.String() != head {
		t.Errorf("tip = %s, want %s", tip, head)
	}
	got := testgit.Run(t, clone, "ls-remote", "--heads", "origin")
	if !strings.Contains(got, head+"\trefs/heads/new") || strings.Contains(got, "refs/heads/old") {
		t.Errorf("remote heads after rename:\n%s", got)
	}

	if _, err := r.RenameBranch(ctx, "new", "taken"); !errors.Is(err, ErrBranchExists) {
		t.Errorf("rename onto an existing branch: %v, want ErrBranchExists", err)
	}
	if _, err := r.RenameBranch(ctx, "old", "x"); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Errorf("rename of a missing branch: %v", err)
	}
	if _, err := r.RenameBranch(ctx, "main", "trunk"); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("rename of the default branch: %v", err)
	}
	if got2 := testgit.Run(t, clone, "ls-remote", "--heads", "origin"); got2 != got {
		t.Errorf("refused renames changed the remote:\n%s", got2)
	}
}
