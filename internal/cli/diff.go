package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/workspace"
)

// diff compares two commits on the remote, or (with no remote, inside a
// workspace) the workspace's edits against its base.
func diff(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	nameOnly := fs.Bool("name-only", false, "print only changed paths")
	nameStatus := fs.Bool("name-status", false, "print changed paths with A/M/D")
	unified := fs.Int("U", 3, "lines of context")
	md := fs.Bool("md", false, "markdown output")
	var paths multiFlag
	fs.Var(&paths, "path", "limit to this path (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	out := diffOut{nameOnly: *nameOnly, nameStatus: *nameStatus, context: *unified, md: *md}

	if len(pos) == 0 {
		if len(paths) > 0 {
			return errors.New("--path is for remote mode; a workspace diff covers its own paths")
		}
		w, err := workspace.Find(".")
		if err != nil {
			return fmt.Errorf("%w\nusage: %s diff <remote> <rev> [<rev>] (or no args inside a workspace)", err, prog)
		}
		ds, content, err := w.Diff()
		if err != nil {
			return err
		}
		r, err := repo.Open(w.State.Remote)
		if err != nil {
			return err
		}
		defer r.Close()
		out.title = fmt.Sprintf("Workspace edits vs `%s`", w.State.Base[:12])
		return out.print(ctx, r, ds, content)
	}

	if len(pos) > 3 {
		return fmt.Errorf("usage: %s diff <remote> <rev> [<rev>] [--path p]...", prog)
	}
	revs := pos[1:]
	if len(revs) == 1 {
		if a, b, ok := strings.Cut(revs[0], ".."); ok && !strings.HasPrefix(b, ".") {
			revs = []string{a, b}
		}
	}
	if len(revs) == 0 {
		revs = []string{"HEAD"}
	}
	r, err := repo.Open(pos[0])
	if err != nil {
		return err
	}
	defer r.Close()

	var from, to plumbing.Hash // commits; zero from = empty tree
	if to, err = r.Resolve(ctx, revs[len(revs)-1]); err != nil {
		return err
	}
	tc, err := r.Commit(ctx, to)
	if err != nil {
		return err
	}
	var fromTree plumbing.Hash
	if len(revs) == 2 {
		if from, err = r.Resolve(ctx, revs[0]); err != nil {
			return err
		}
		fc, err := r.Commit(ctx, from)
		if err != nil {
			return err
		}
		fromTree = fc.TreeHash
	} else if len(tc.ParentHashes) > 0 {
		// One rev: what that commit changed, against its first parent.
		from = tc.ParentHashes[0]
		fc, err := r.Commit(ctx, from)
		if err != nil {
			return err
		}
		fromTree = fc.TreeHash
	}

	ds, err := r.Changes(ctx, fromTree, tc.TreeHash, scopeDescend(paths), scopeKeep(paths))
	if err != nil {
		return err
	}
	fromName := "(empty)"
	if !from.IsZero() {
		fromName = "`" + from.String()[:12] + "`"
	}
	out.title = fmt.Sprintf("Diff %s..`%s` in `%s`", fromName, to.String()[:12], pos[0])
	return out.print(ctx, r, ds, nil)
}

// scopeKeep reports whether p is under one of paths (all if none).
func scopeKeep(paths []string) func(string) bool {
	if len(paths) == 0 {
		return nil
	}
	clean := cleanPaths(paths)
	return func(p string) bool {
		for _, s := range clean {
			if p == s || strings.HasPrefix(p, s+"/") {
				return true
			}
		}
		return false
	}
}

// scopeDescend reports whether dir may hold files under paths.
func scopeDescend(paths []string) func(string) bool {
	if len(paths) == 0 {
		return nil
	}
	clean := cleanPaths(paths)
	keep := scopeKeep(clean)
	return func(dir string) bool {
		if keep(dir) {
			return true
		}
		for _, s := range clean {
			if strings.HasPrefix(s, dir+"/") {
				return true
			}
		}
		return false
	}
}

func cleanPaths(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = strings.Trim(path.Clean("/"+strings.ReplaceAll(p, "\\", "/")), "/")
	}
	return out
}

type diffOut struct {
	nameOnly, nameStatus, md bool
	context                  int
	title                    string
}

func (o diffOut) print(ctx context.Context, r *repo.Repo, ds []repo.FileDiff, content map[plumbing.Hash][]byte) error {
	if o.md {
		fmt.Printf("## %s\n\n", o.title)
		if len(ds) == 0 {
			fmt.Println("No changes.")
			return nil
		}
		fmt.Printf("%d file(s) changed.\n\n| change | path |\n| --- | --- |\n", len(ds))
		names := map[string]string{"A": "added", "M": "modified", "D": "deleted"}
		for _, d := range ds {
			fmt.Printf("| %s | `%s` |\n", names[d.Status()], d.Path)
		}
		if o.nameOnly || o.nameStatus {
			return nil
		}
		var buf bytes.Buffer
		if err := r.WritePatch(ctx, &buf, ds, content, o.context); err != nil {
			return err
		}
		fence := "```"
		for strings.Contains(buf.String(), fence) {
			fence += "`"
		}
		fmt.Printf("\n%sdiff\n%s%s\n", fence, buf.String(), fence)
		return nil
	}
	switch {
	case o.nameOnly:
		for _, d := range ds {
			fmt.Println(d.Path)
		}
	case o.nameStatus:
		for _, d := range ds {
			fmt.Printf("%s\t%s\n", d.Status(), d.Path)
		}
	default:
		return r.WritePatch(ctx, os.Stdout, ds, content, o.context)
	}
	return nil
}

// add widens a sparse workspace with more paths.
func add(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return fmt.Errorf("usage: %s add <path>...  (inside a workspace)", prog)
	}
	w, err := workspace.Find(".")
	if err != nil {
		return err
	}
	r, err := repo.Open(w.State.Remote)
	if err != nil {
		return err
	}
	defer r.Close()
	res, err := w.Add(ctx, r, pos)
	if err != nil {
		return err
	}
	if *md {
		if len(res.Paths) == 0 {
			fmt.Println("Already in the workspace; nothing to add.")
			return nil
		}
		fmt.Printf("## Added to workspace\n\n- Paths: %s\n- %d files, %s, at `%s`\n", backticks(res.Paths), res.Files, humanBytes(res.Bytes), w.State.Base[:12])
		if len(res.New) > 0 {
			fmt.Printf("- Not in the repo yet (new files there will be committed): %s\n", backticks(res.New))
		}
		return nil
	}
	if len(res.Paths) == 0 {
		fmt.Println("already in the workspace")
		return nil
	}
	fmt.Printf("added %s: %d files, %s\n", strings.Join(res.Paths, ", "), res.Files, humanBytes(res.Bytes))
	return nil
}

func backticks(ps []string) string {
	q := make([]string, len(ps))
	for i, p := range ps {
		q[i] = "`" + p + "`"
	}
	return strings.Join(q, ", ")
}
