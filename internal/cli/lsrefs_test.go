package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/baz-scm/gion/internal/testgit"
)

// ls-refs with no --prefix lists what `git ls-remote --symref` does:
// HEAD, branches and tags (annotated tags with their peeled line).
func TestLsRefsMatchesLsRemote(t *testing.T) {
	remote, clone := testgit.Serve(t)
	testgit.Run(t, clone, "tag", "v0.1.0")
	testgit.Run(t, clone, "tag", "-a", "v0.2.0", "-m", "v0.2.0")
	testgit.Run(t, clone, "push", "-q", "origin", "--tags")

	got := captureStdout(t, func() error { return lsRefs(context.Background(), []string{remote}) })
	want := testgit.Run(t, clone, "ls-remote", "--symref", remote)
	if strings.TrimSpace(got) != want {
		t.Errorf("gion ls-refs:\n%s\ngit ls-remote --symref:\n%s", got, want)
	}
	if !strings.Contains(got, "refs/tags/v0.2.0^{}") {
		t.Errorf("annotated tag not listed with its peeled commit:\n%s", got)
	}
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err = fn()
	w.Close()
	os.Stdout = old
	out := <-done
	if err != nil {
		t.Fatal(err)
	}
	return out
}
