package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"

	"github.com/baz-scm/gion/internal/deps"
	"github.com/baz-scm/gion/internal/forge"
	"github.com/baz-scm/gion/internal/repo"
)

// depsMax is how much local dependencies may add to a workspace on their
// own; above it they're only suggested.
const depsMax int64 = 5 << 30

// depPlan is what the manifests under a workspace's new paths depend on.
type depPlan struct {
	found []deps.Found
	add   bool  // false: only suggest them
	size  int64 // total, when measured
	cut   bool  // size is a lower bound
}

// paths returns the paths to add: all found, or none if only suggested.
func (p depPlan) paths() []string {
	if !p.add {
		return nil
	}
	return p.all()
}

func (p depPlan) all() []string {
	out := make([]string, len(p.found))
	for i, f := range p.found {
		out[i] = f.Path
	}
	return out
}

// planDeps finds the local dependencies of start in tree root, and whether
// they fit under depsMax. Sizes come from GitHub's API; elsewhere (or if it
// fails) they're added without a cap.
func planDeps(ctx context.Context, r *repo.Repo, root plumbing.Hash, have, start []string) (depPlan, error) {
	found, err := deps.Resolve(ctx, deps.NewRepoSource(r, root), have, start)
	if err != nil || len(found) == 0 {
		return depPlan{found: found, add: true}, err
	}
	p := depPlan{found: found, add: true}
	if _, ok := forge.GitHubRepo(r.Remote); !ok {
		return p, nil
	}
	// One request for an upper bound: everything under the directory that
	// holds them all (not the root, which on a big repo GitHub truncates).
	if parent := commonDir(p.all()); parent != "" {
		if e, err := r.Lookup(ctx, root, parent); err == nil {
			if size, truncated, err := forge.TreeSize(ctx, r.Remote, e.Hash.String()); err == nil && !truncated && size <= depsMax {
				return p, nil
			}
		}
	}
	var trees []string
	for _, f := range found {
		e, err := r.Lookup(ctx, root, f.Path)
		if err != nil || e.Mode != filemode.Dir {
			continue // files are small; a missing one isn't added anyway
		}
		trees = append(trees, e.Hash.String())
	}
	size, truncated, err := forge.TreeSizes(ctx, r.Remote, trees)
	if err != nil {
		return p, nil
	}
	p.size, p.cut = size, truncated
	p.add = !truncated && size <= depsMax
	return p, nil
}

func (p depPlan) print(md bool) {
	if len(p.found) == 0 {
		return
	}
	more := humanBytes(p.size)
	if p.cut {
		more = "more than " + more
	}
	if md {
		if p.add {
			fmt.Printf("\nAdded for local dependencies (`--no-deps` to skip):\n\n")
		} else {
			fmt.Printf("\nLocal dependencies, not added (%s, over %s): add the ones you need with `%s add <path>`.\n\n", more, humanBytes(depsMax), prog)
		}
		for _, f := range p.found {
			fmt.Printf("- `%s` (`%s`: `%s`)\n", f.Path, f.Manifest, f.Spec)
		}
		return
	}
	if !p.add {
		fmt.Printf("local dependencies not added (%s, over %s); add them with: %s add %s\n", more, humanBytes(depsMax), prog, strings.Join(p.all(), " "))
	}
	width := 0
	for _, f := range p.found {
		width = max(width, len(f.Path))
	}
	sign := "+"
	if !p.add {
		sign = "?"
	}
	for _, f := range p.found {
		fmt.Printf("%s %-*s  (%s: %s)\n", sign, width, f.Path, f.Manifest, f.Spec)
	}
}

// commonDir returns the deepest directory containing all of paths ("" for
// the root).
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := strings.Split(paths[0], "/")
	if len(paths) == 1 {
		common = common[:len(common)-1] // its parent: one path may be a file
	}
	for _, p := range paths[1:] {
		segs := strings.Split(p, "/")
		n := 0
		for n < len(common) && n < len(segs)-1 && common[n] == segs[n] {
			n++
		}
		common = common[:n]
	}
	return strings.Join(common, "/")
}
