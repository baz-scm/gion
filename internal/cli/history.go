package cli

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/workspace"
)

// localTarget reports, inside a workspace, whether args are all local paths
// (so the command is about the workspace), and returns them workspace-
// relative.
func localTarget(args []string) (*workspace.Workspace, []string, bool) {
	w, err := workspace.Find(".")
	if err != nil {
		return nil, nil, false
	}
	var rels []string
	for _, a := range args {
		rel, ok := w.Rel(a)
		if !ok {
			return nil, nil, false
		}
		rels = append(rels, rel)
	}
	return w, rels, true
}

func log(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	n := fs.Int("n", 20, "number of commits")
	pathFlag := fs.String("path", "", "only commits that changed this path")
	oneline := fs.Bool("oneline", false, "one line per commit")
	local := fs.Bool("local", false, "walk history locally even when the forge could answer")
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: %s log <remote> [<rev>] [-n N] [--path p] [--oneline]  (or `%s log [<path>]` in a workspace)", prog, prog)

	var remote, rev, p string
	var base plumbing.Hash
	if w, rels, ok := localTarget(pos); ok && len(rels) <= 1 && *pathFlag == "" {
		remote, rev = w.State.Remote, w.State.Base
		base, _ = plumbing.FromHex(w.State.Base)
		if len(rels) == 1 {
			p = rels[0]
		}
	} else {
		if len(pos) < 1 || len(pos) > 2 {
			return usage
		}
		remote, rev, p = pos[0], "HEAD", *pathFlag
		if len(pos) == 2 {
			rev = pos[1]
		}
	}
	r, err := repo.Open(remote)
	if err != nil {
		return err
	}
	defer r.Close()
	tip := base
	if tip.IsZero() {
		if tip, err = r.Resolve(ctx, rev); err != nil {
			return err
		}
	}
	res, err := r.Log(ctx, tip, repo.LogOptions{Max: *n, Path: p, Forge: !*local})
	if err != nil {
		return err
	}

	if *md {
		scope := ""
		if p != "" {
			scope = fmt.Sprintf(" touching `%s`", p)
		}
		fmt.Printf("## Log of `%s` (`%s`)%s in `%s`\n\n", rev, tip.String()[:12], scope, remote)
		if len(res.Commits) == 0 {
			fmt.Println("No commits.")
		} else {
			fmt.Println("| commit | date | author | subject |\n| --- | --- | --- | --- |")
			for _, c := range res.Commits {
				fmt.Printf("| `%s` | %s | %s | %s |\n", c.Hash.String()[:12], c.Author.When.Format("2006-01-02 15:04"), mdCell(c.Author.Name), mdCell(subject(c)))
			}
		}
		fmt.Printf("\n%d commit(s) via %s.", len(res.Commits), res.Via)
		if res.Truncated {
			fmt.Printf(" Stopped after walking %d commits; more may exist (try a smaller -n, or GitHub with credentials).", res.Walked)
		}
		fmt.Printf(" Full message and diff: `%s diff %s <commit>`.\n", prog, remote)
		return nil
	}
	for i, c := range res.Commits {
		if *oneline {
			fmt.Printf("%s %s\n", c.Hash.String()[:12], subject(c))
			continue
		}
		if i > 0 {
			fmt.Println()
		}
		printCommit(c)
	}
	return nil
}

// printCommit prints c the way `git log` does.
func printCommit(c *object.Commit) {
	fmt.Printf("commit %s\n", c.Hash)
	if len(c.ParentHashes) > 1 {
		var ps []string
		for _, p := range c.ParentHashes {
			ps = append(ps, p.String()[:12])
		}
		fmt.Printf("Merge: %s\n", strings.Join(ps, " "))
	}
	fmt.Printf("Author: %s <%s>\n", c.Author.Name, c.Author.Email)
	fmt.Printf("Date:   %s\n\n", c.Author.When.Format("Mon Jan 2 15:04:05 2006 -0700"))
	for _, l := range strings.Split(strings.TrimRight(c.Message, "\n"), "\n") {
		fmt.Printf("    %s\n", l)
	}
}

func subject(c *object.Commit) string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return s
}

func mdCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

func blame(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("blame", flag.ContinueOnError)
	lines := fs.String("L", "", "only lines a,b (1-based, inclusive)")
	local := fs.Bool("local", false, "compute locally even when the forge could answer")
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	usage := fmt.Errorf("usage: %s blame <remote> <rev>:<path> [-L a,b]  (or `%s blame <path>` in a workspace)", prog, prog)

	var remote, rev, p string
	var tip plumbing.Hash
	if w, rels, ok := localTarget(pos); ok && len(rels) == 1 {
		remote, rev, p = w.State.Remote, w.State.Base, rels[0]
		tip, _ = plumbing.FromHex(w.State.Base)
	} else {
		if len(pos) != 2 || !strings.Contains(pos[1], ":") {
			return usage
		}
		remote = pos[0]
		rev, p = repo.SplitSpec(pos[1])
	}
	from, to := 1, 0
	if *lines != "" {
		a, b, ok := strings.Cut(*lines, ",")
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		if !ok || err1 != nil || err2 != nil || x < 1 || y < x {
			return fmt.Errorf("-L wants a,b with 1 <= a <= b, got %q", *lines)
		}
		from, to = x, y
	}

	r, err := repo.Open(remote)
	if err != nil {
		return err
	}
	defer r.Close()
	if tip.IsZero() {
		if tip, err = r.Resolve(ctx, rev); err != nil {
			return err
		}
	}
	res, err := r.BlameVia(ctx, tip, p, !*local)
	if err != nil {
		return err
	}
	if to == 0 || to > len(res.Lines) {
		to = len(res.Lines)
	}
	if from > len(res.Lines) {
		return fmt.Errorf("%s has %d lines", p, len(res.Lines))
	}

	if *md {
		fmt.Printf("## Blame of `%s` at `%s` in `%s`\n\n", p, tip.String()[:12], remote)
		fmt.Println("| lines | commit | date | author | subject |\n| --- | --- | --- | --- | --- |")
		for i := from - 1; i < to; {
			j := i
			for j+1 < to && res.Lines[j+1].Commit == res.Lines[i].Commit {
				j++
			}
			c := res.Commits[res.Lines[i].Commit]
			span := strconv.Itoa(i + 1)
			if j > i {
				span += "–" + strconv.Itoa(j+1)
			}
			fmt.Printf("| %s | `%s` | %s | %s | %s |\n", span, c.Hash.String()[:12], c.Author.When.Format("2006-01-02"), mdCell(c.Author.Name), mdCell(subject(c)))
			i = j + 1
		}
		fmt.Printf("\nLines %d–%d of %d, via %s. Read the file with `%s cat %s %s:%s`.\n", from, to, len(res.Lines), res.Via, prog, remote, tip.String()[:12], p)
		return nil
	}
	width := len(strconv.Itoa(to))
	for i := from - 1; i < to; i++ {
		l := res.Lines[i]
		c := res.Commits[l.Commit]
		name := c.Author.Name
		if len(name) > 16 {
			name = name[:16]
		}
		fmt.Printf("%s (%-16s %s %*d) %s", l.Commit.String()[:8], name, c.Author.When.Format("2006-01-02"), width, i+1, strings.TrimSuffix(l.Text, "\n")+"\n")
	}
	return nil
}
