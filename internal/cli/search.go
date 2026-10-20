package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/baz-scm/gion/internal/index"
	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/search"
)

// openIndex resolves rev on remote and returns its path index.
func openIndex(ctx context.Context, remote, rev string) (*repo.Repo, *index.Index, string, error) {
	r, err := repo.Open(remote)
	if err != nil {
		return nil, nil, "", err
	}
	commit, err := r.Resolve(ctx, rev)
	if err != nil {
		r.Close()
		return nil, nil, "", err
	}
	idx, how, err := index.At(ctx, r, rev, commit)
	if err != nil {
		r.Close()
		return nil, nil, "", err
	}
	return r, idx, how, nil
}

// remoteRevArg splits "<remote> [<rev>] <arg>" positionals.
func remoteRevArg(pos []string, usage string) (remote, rev, arg string, err error) {
	switch len(pos) {
	case 2:
		return pos[0], "HEAD", pos[1], nil
	case 3:
		return pos[0], pos[1], pos[2], nil
	}
	return "", "", "", fmt.Errorf("usage: %s %s", prog, usage)
}

func find(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	limit := fs.Int("limit", 0, "stop after this many results (0 = all)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	remote, rev, pattern, err := remoteRevArg(pos, "find <remote> [<rev>] <pattern>")
	if err != nil {
		return err
	}
	match, err := search.CompilePath(pattern)
	if err != nil {
		return err
	}
	start := time.Now()
	r, idx, how, err := openIndex(ctx, remote, rev)
	if err != nil {
		return err
	}
	defer r.Close()

	var hits []string
	for _, e := range idx.Entries {
		if match(e.Path) {
			hits = append(hits, e.Path)
			if *limit > 0 && len(hits) == *limit {
				break
			}
		}
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	if *md {
		fmt.Printf("## `%s` in `%s` @ `%s`\n\n", pattern, remote, idx.Commit.String()[:12])
		for _, h := range hits {
			fmt.Printf("- `%s`\n", h)
		}
		fmt.Printf("\n%d of %d files matched (index: %s, %s).\n", len(hits), len(idx.Entries), how, elapsed)
		return nil
	}
	for _, h := range hits {
		fmt.Println(h)
	}
	fmt.Fprintf(os.Stderr, "%d of %d files (index %s, %s)\n", len(hits), len(idx.Entries), how, elapsed)
	return nil
}

func grep(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("grep", flag.ContinueOnError)
	md := fs.Bool("md", false, "markdown output")
	fold := fs.Bool("i", false, "case-insensitive")
	filesOnly := fs.Bool("l", false, "print only names of files with matches")
	local := fs.Bool("local", false, "scan every candidate file, however many")
	useForge := fs.Bool("forge", false, "use the forge's code search to pick files")
	maxFiles := fs.Int("max-files", 5000, "above this many candidates, use code search")
	var paths multiFlag
	fs.Var(&paths, "path", "only files under this path or matching this glob (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	remote, rev, pattern, err := remoteRevArg(pos, "grep <remote> [<rev>] <regex> [--path p]...")
	if err != nil {
		return err
	}
	if *local && *useForge {
		return errors.New("--local and --forge are exclusive")
	}
	opts := search.GrepOptions{Pattern: pattern, Fold: *fold, MaxFiles: *maxFiles, Strategy: search.Auto}
	if *local {
		opts.Strategy = search.Local
	}
	if *useForge {
		opts.Strategy = search.Forge
	}
	for _, p := range paths {
		m, err := search.CompileGlob(p)
		if err != nil {
			return err
		}
		opts.Paths = append(opts.Paths, m)
	}

	start := time.Now()
	var idx *index.Index
	r, err := repo.Open(remote)
	if err != nil {
		return err
	}
	defer r.Close()
	commit, err := r.Resolve(ctx, rev)
	if err != nil {
		return err
	}
	// Paths under known directories need only those subtrees, not an index
	// of the whole repository.
	if dirs := search.LiteralDirs(paths); dirs != nil {
		if len(dirs) == 1 {
			opts.ForgeDir = dirs[0]
		}
		idx, err = index.Under(ctx, r, rev, commit, dirs)
	} else {
		idx, _, err = index.At(ctx, r, rev, commit)
	}
	if err != nil {
		return err
	}
	res, err := search.Grep(ctx, r, idx, opts)
	if err != nil {
		return err
	}
	elapsed := time.Since(start).Round(time.Millisecond)

	files := 0
	for i, m := range res.Matches {
		if i == 0 || res.Matches[i-1].Path != m.Path {
			files++
		}
	}
	summary := fmt.Sprintf("%d matches in %d files; scanned %d files via %s in %s", len(res.Matches), files, res.Scanned, res.Strategy, elapsed)

	if *md {
		fmt.Printf("## `%s` in `%s` @ `%s`\n\n", pattern, remote, idx.Commit.String()[:12])
		for i, m := range res.Matches {
			if i == 0 || res.Matches[i-1].Path != m.Path {
				if i > 0 && !*filesOnly {
					fmt.Print("```\n\n")
				}
				fmt.Printf("### `%s`\n", m.Path)
				if !*filesOnly {
					fmt.Println("```")
				}
			}
			if !*filesOnly {
				fmt.Printf("%d: %s\n", m.Line, m.Text)
			}
		}
		if len(res.Matches) > 0 && !*filesOnly {
			fmt.Println("```")
		}
		fmt.Printf("\n%s.\n", summary)
		if res.Note != "" {
			fmt.Printf("\n> Note: %s.\n", res.Note)
		}
		return nil
	}
	for i, m := range res.Matches {
		if *filesOnly {
			if i == 0 || res.Matches[i-1].Path != m.Path {
				fmt.Println(m.Path)
			}
			continue
		}
		fmt.Printf("%s:%d:%s\n", m.Path, m.Line, m.Text)
	}
	fmt.Fprintln(os.Stderr, summary)
	if res.Note != "" {
		fmt.Fprintln(os.Stderr, "note:", res.Note)
	}
	return nil
}
