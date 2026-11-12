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
// fails) they're added without a cap. conf, if not nil, stands in for the
// repo's config file.
func planDeps(ctx context.Context, r *repo.Repo, root plumbing.Hash, conf *localConfig, have, start []string) (depPlan, error) {
	found, err := deps.Resolve(ctx, depsSource(r, root, conf), have, start)
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
	lines := p.lines()
	if md {
		if p.add {
			fmt.Printf("\nAdded for local dependencies (`--no-deps` to skip):\n\n")
		} else {
			fmt.Printf("\nLocal dependencies, not added (%s, over %s): add the ones you need with `%s add <path>`.\n\n", more, humanBytes(depsMax), prog)
		}
		for _, l := range lines {
			if len(l.paths) > 1 {
				fmt.Printf("- %d workspace members of `%s`: %s\n", len(l.paths), l.manifest, backticks(l.paths))
				continue
			}
			fmt.Printf("- `%s` (`%s`: `%s`)\n", l.paths[0], l.manifest, l.spec)
		}
		return
	}
	if !p.add {
		fmt.Printf("local dependencies not added (%s, over %s); add them with: %s add %s\n", more, humanBytes(depsMax), prog, strings.Join(p.all(), " "))
	}
	width := 0
	for _, l := range lines {
		width = max(width, len(l.label()))
	}
	sign := "+"
	if !p.add {
		sign = "?"
	}
	for _, l := range lines {
		if len(l.paths) > 1 {
			fmt.Printf("%s %-*s  (%s)\n", sign, width, l.label(), l.manifest)
			continue
		}
		fmt.Printf("%s %-*s  (%s: %s)\n", sign, width, l.label(), l.manifest, l.spec)
	}
}

// depLine is one line of a plan's report: one path, or a workspace's
// members together so they don't bury the dependencies that matter.
type depLine struct {
	paths          []string
	manifest, spec string
}

func (l depLine) label() string {
	if len(l.paths) > 1 {
		return fmt.Sprintf("%d workspace members", len(l.paths))
	}
	return l.paths[0]
}

func (p depPlan) lines() []depLine {
	var out []depLine
	members := map[string]int{} // manifest -> index in out
	for _, f := range p.found {
		if f.Spec == "workspace member" {
			if i, ok := members[f.Manifest]; ok {
				out[i].paths = append(out[i].paths, f.Path)
				continue
			}
			members[f.Manifest] = len(out)
		}
		out = append(out, depLine{paths: []string{f.Path}, manifest: f.Manifest, spec: f.Spec})
	}
	return out
}

// scope describes what a clone or add asked for (paths, and projects with
// their paths) and what came with it.
func (p depPlan) scope(paths, projects, projectPaths []string) string {
	var parts []string
	if len(projects) > 0 {
		parts = append(parts, fmt.Sprintf("project %s: %s", strings.Join(projects, ", "), strings.Join(projectPaths, ", ")))
	}
	if len(paths) > 0 {
		label := ""
		if len(projects) > 0 {
			label = "path "
		}
		parts = append(parts, label+strings.Join(paths, ", "))
	}
	s := strings.Join(parts, "; ")
	if n := len(p.paths()); n > 0 {
		s += fmt.Sprintf(" + %d %s", n, plural2(n, "dependency", "dependencies"))
	}
	return s
}

func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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

// projectPaths returns the starting paths of the named projects, from the
// config in tree root (or conf, if not nil).
func projectPaths(ctx context.Context, r *repo.Repo, root plumbing.Hash, conf *localConfig, names []string) ([]string, error) {
	c, err := deps.LoadConfig(ctx, depsSource(r, root, conf))
	if err != nil {
		return nil, err
	}
	return c.ProjectPaths(names)
}

func depsSource(r *repo.Repo, root plumbing.Hash, conf *localConfig) deps.Source {
	src := deps.Source(deps.NewRepoSource(r, root))
	if conf != nil {
		src = deps.WithConfig(src, conf.name, conf.data)
	}
	return src
}

// localConfig is a config read from a workspace instead of the repo.
type localConfig struct {
	name string // as messages show it
	data []byte
}
