package wire

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/testgit"
)

// file:// remotes are served by go-git itself: no filters, one fetch per
// connection. Filtered fetches still work, again and again.
func TestFetchFileRemote(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	testgit.Run(t, dir, "init", "-q", "-b", "main", seed)
	os.WriteFile(filepath.Join(seed, "a.txt"), []byte("a\n"), 0o644)
	testgit.Run(t, seed, "add", "-A")
	testgit.Run(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "seed")
	head := plumbing.NewHash(strings.TrimSpace(testgit.Run(t, seed, "rev-parse", "HEAD")))
	tree := plumbing.NewHash(strings.TrimSpace(testgit.Run(t, seed, "rev-parse", "HEAD^{tree}")))
	bare := filepath.Join(dir, "r.git")
	testgit.Run(t, dir, "clone", "-q", "--bare", seed, bare)

	ctx := context.Background()
	s, err := Open(ctx, "file://"+bare)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, want := range []plumbing.Hash{head, tree, head} {
		objs, err := s.Fetch(ctx, []plumbing.Hash{want}, FilterExact)
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		found := false
		for _, o := range objs {
			found = found || o.Hash() == want
		}
		if !found {
			t.Fatalf("fetch %d: %s not sent", i, want)
		}
	}
}
