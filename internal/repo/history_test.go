package repo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/testgit"
)

// historyRepo builds a history with a side branch merged back, every commit
// a minute apart so date order is unambiguous:
//
//	seed - c1 - c2 - c3 - merge - c5   (main)
//	          \           /
//	           s1 ---- s2              (side)
func historyRepo(t *testing.T) (remote, clone string) {
	remote, clone = testgit.Serve(t)
	at := time.Now().Add(time.Hour)
	commit := func(msg string, files map[string]string) {
		t.Helper()
		for p, c := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(clone, p)), 0o755)
			os.WriteFile(filepath.Join(clone, p), []byte(c), 0o644)
			testgit.Run(t, clone, "add", p)
		}
		at = at.Add(time.Minute)
		d := at.Format(time.RFC3339)
		t.Setenv("GIT_AUTHOR_DATE", d)
		t.Setenv("GIT_COMMITTER_DATE", d)
		testgit.Run(t, clone, "commit", "-qm", msg)
	}
	lines := func(edits map[int]string) string {
		var b strings.Builder
		for i := 1; i <= 10; i++ {
			if e, ok := edits[i]; ok {
				b.WriteString(e + "\n")
			} else {
				fmt.Fprintf(&b, "line %d\n", i)
			}
		}
		return b.String()
	}
	commit("c1", map[string]string{"a.txt": lines(nil)})
	testgit.Run(t, clone, "checkout", "-qb", "side")
	commit("s1", map[string]string{"d/e.txt": "side e\n"})
	commit("s2", map[string]string{"a.txt": lines(map[int]string{9: "side 9"})})
	testgit.Run(t, clone, "checkout", "-q", "main")
	commit("c2", map[string]string{"b.txt": "b2\n"})
	commit("c3", map[string]string{"a.txt": lines(map[int]string{2: "main 2"})})
	at = at.Add(time.Minute)
	t.Setenv("GIT_AUTHOR_DATE", at.Format(time.RFC3339))
	t.Setenv("GIT_COMMITTER_DATE", at.Format(time.RFC3339))
	testgit.Run(t, clone, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	commit("c5", map[string]string{"a.txt": lines(map[int]string{2: "main 2", 5: "main 5", 9: "side 9"})})
	testgit.Run(t, clone, "push", "-q", "origin", "main", "side")
	return remote, clone
}

func hashes(t *testing.T, out string) []string {
	return strings.Fields(out)
}

func TestLogMatchesGit(t *testing.T) {
	remote, clone := historyRepo(t)
	r := openTest(t, remote)
	ctx := context.Background()
	tip := plumbing.NewHash(strings.TrimSpace(testgit.Run(t, clone, "rev-parse", "main")))

	for _, p := range []string{"", "a.txt", "d", "d/e.txt", "b.txt", "nope.txt"} {
		args := []string{"log", "--format=%H", "main"}
		if p != "" {
			args = append(args, "--", p)
		}
		want := hashes(t, testgit.Run(t, clone, args...))
		res, err := r.Log(ctx, tip, LogOptions{Max: 100, Path: p})
		if err != nil {
			t.Fatalf("Log(%q): %v", p, err)
		}
		var got []string
		for _, c := range res.Commits {
			got = append(got, c.Hash.String())
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("log -- %q:\n got %v\nwant %v", p, short8(got), short8(want))
		}
	}

	res, err := r.Log(ctx, tip, LogOptions{Max: 2})
	if err != nil || len(res.Commits) != 2 {
		t.Fatalf("Max 2: %d commits, %v", len(res.Commits), err)
	}
}

func short8(hs []string) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h[:8]
	}
	return out
}

func TestBlameMatchesGit(t *testing.T) {
	remote, clone := historyRepo(t)
	r := openTest(t, remote)
	ctx := context.Background()
	tip := plumbing.NewHash(strings.TrimSpace(testgit.Run(t, clone, "rev-parse", "main")))

	for _, p := range []string{"a.txt", "d/e.txt", "b.txt", "run.sh"} {
		var want []string
		for _, l := range strings.Split(testgit.Run(t, clone, "blame", "--porcelain", "main", "--", p), "\n") {
			// Header lines: "<40 hex> <orig line> <final line> [<group size>]".
			f := strings.Fields(l)
			if len(f) >= 3 && len(f[0]) == 40 && !strings.HasPrefix(l, "\t") {
				want = append(want, f[0])
			}
		}
		res, err := r.Blame(ctx, tip, p)
		if err != nil {
			t.Fatalf("Blame(%s): %v", p, err)
		}
		var got []string
		for _, l := range res.Lines {
			got = append(got, l.Commit.String())
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("blame %s:\n got %v\nwant %v", p, short8(got), short8(want))
		}
	}
	if _, err := r.Blame(ctx, tip, "nope.txt"); err == nil {
		t.Error("blame of a missing file should fail")
	}
}

func TestMerge3MatchesGitMergeFile(t *testing.T) {
	base := "1\n2\n3\n4\n5\n6\n7\n8\n"
	cases := []struct {
		name, ours, theirs string
	}{
		{"disjoint", "1\nTWO\n3\n4\n5\n6\n7\n8\n", "1\n2\n3\n4\n5\n6\nSEVEN\n8\n"},
		{"same change", "1\nX\n3\n4\n5\n6\n7\n8\n", "1\nX\n3\n4\n5\n6\n7\n8\n"},
		{"conflict", "1\nours\n3\n4\n5\n6\n7\n8\n", "1\ntheirs\n3\n4\n5\n6\n7\n8\n"},
		{"insert both ends", "0\n1\n2\n3\n4\n5\n6\n7\n8\n", "1\n2\n3\n4\n5\n6\n7\n8\n9\n"},
		{"delete vs edit", "1\n2\n5\n6\n7\n8\n", "1\n2\n3\nFOUR\n5\n6\n7\n8\n"},
		{"one side only", base, "1\n2\n3\n4\n5\n6\n7\n8\nend\n"},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		got, conflict := merge3(base, tc.ours, tc.theirs, Labels{"ours", "base", "theirs"})
		for n, c := range map[string]string{"o": tc.ours, "b": base, "t": tc.theirs} {
			os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644)
		}
		out, err := testgit.RunErr(dir, "merge-file", "-p", "--diff3", "-L", "ours", "-L", "base", "-L", "theirs", "o", "b", "t")
		gitConflict := err != nil
		if conflict != gitConflict {
			t.Errorf("%s: conflict = %v, git says %v\n%s", tc.name, conflict, gitConflict, got)
			continue
		}
		if !conflict && got != out {
			t.Errorf("%s: merged\n%q\ngit\n%q", tc.name, got, out)
		}
		if conflict && !HasConflictMarkers([]byte(got)) {
			t.Errorf("%s: conflict without markers:\n%s", tc.name, got)
		}
	}
}
