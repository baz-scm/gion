// Package deps finds the local dependencies of a sparse workspace's paths:
// the other directories and files of the repo that the manifests under
// those paths point at (file: packages, path crates, go.work modules,
// reactor modules...), so installs and builds work without the whole repo.
//
// It reads manifests remotely, through a Source, and follows what they
// point at until no new paths turn up.
package deps

import (
	"context"
	"path"
	"sort"
	"strings"
)

// Kind says what a path is in the repo.
type Kind int

const (
	Missing Kind = iota
	File
	Dir
)

// Entry is one name in a directory listing.
type Entry struct {
	Name string
	Dir  bool
}

// Source reads one commit of the repo. Paths are slash-separated and
// relative to the repo root ("" is the root).
type Source interface {
	Stat(ctx context.Context, p string) (Kind, error)
	ReadFile(ctx context.Context, p string) ([]byte, error)
	ReadDir(ctx context.Context, dir string) ([]Entry, error)
	// Files lists every file under p (p itself if it's a file).
	Files(ctx context.Context, p string) ([]string, error)
	// Prefetch is a hint that these paths are about to be read: files'
	// contents, or everything under directories. Sources that fetch over
	// the network do it in one round trip.
	Prefetch(ctx context.Context, paths []string) error
}

// Found is one path a manifest depends on.
type Found struct {
	Path     string // file or directory to add
	Manifest string // the manifest that points at it
	Spec     string // what in the manifest does, as written there
}

// Resolve returns the paths that the manifests under start depend on,
// directly or through each other, that aren't already covered by have or
// start. Gradle builds are scripts and aren't read.
func Resolve(ctx context.Context, src Source, have, start []string) ([]Found, error) {
	r := &resolver{src: src, files: map[string][]byte{}, roots: map[string]any{}}
	scope := append(append([]string{}, have...), start...)
	queue := append([]string{}, start...)
	var found []Found
	scanned := map[string]bool{}
	for len(queue) > 0 {
		// One level at a time, so each level costs a few round trips
		// however many paths it has.
		level := queue
		queue = nil
		cands, err := r.scan(ctx, level, scanned)
		if err != nil {
			return nil, err
		}
		for _, c := range cands {
			if c.path == "" || covered(scope, c.path) {
				continue
			}
			k, err := src.Stat(ctx, c.path)
			if err != nil {
				return nil, err
			}
			// A lone file (a parent POM, an extended tsconfig) is there for
			// its settings: follow its own parent chain, not the modules it
			// aggregates.
			if k == Missing || (c.fromFile && k != File) {
				continue
			}
			if k == File && inParentDir(scope, c.path) {
				// Clones bring the files beside every path's parents
				// already; follow what this one points at, quietly.
				if !scanned[c.path] {
					queue = append(queue, c.path)
				}
				continue
			}
			scope = append(scope, c.path)
			found = append(found, Found{Path: c.path, Manifest: c.manifest, Spec: c.spec})
			queue = append(queue, c.path)
		}
	}
	// A directory found later can cover a file found earlier.
	var out []Found
	for i, f := range found {
		dup := covered(have, f.Path) || covered(start, f.Path)
		for j, g := range found {
			if i != j && g.Path != f.Path && covered([]string{g.Path}, f.Path) {
				dup = true
			}
		}
		if !dup {
			out = append(out, f)
		}
	}
	return out, nil
}

type cand struct{ path, manifest, spec string }

// scanned is a cand and how it was found.
type scannedCand struct {
	cand
	fromFile bool // in a lone file's manifest, not a directory's
}

type resolver struct {
	src   Source
	files map[string][]byte // file contents read so far (nil: missing)
	roots map[string]any    // per-ecosystem workspace roots, by key
}

// skipDirs hold manifests that aren't the project's own.
var skipDirs = map[string]bool{"node_modules": true, "vendor": true, ".git": true, "testdata": true, "test_data": true, "fixtures": true, "__fixtures__": true}

// scan reads every manifest under paths and returns what they point at.
func (r *resolver) scan(ctx context.Context, paths []string, scanned map[string]bool) ([]scannedCand, error) {
	if err := r.src.Prefetch(ctx, paths); err != nil {
		return nil, err
	}
	var manifests []string
	lone := map[string]bool{}
	for _, p := range paths {
		all, err := r.src.Files(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, f := range all {
			if scanned[f] || kindOf(f) == "" || inSkipDir(f) {
				continue
			}
			scanned[f] = true
			manifests = append(manifests, f)
			lone[f] = f == p
		}
	}
	if err := r.src.Prefetch(ctx, manifests); err != nil {
		return nil, err
	}
	var out []scannedCand
	for _, f := range manifests {
		data, err := r.read(ctx, f)
		if err != nil {
			return nil, err
		}
		var cs []cand
		switch kindOf(f) {
		case "npm":
			cs, err = r.npm(ctx, f, data)
		case "cargo":
			cs, err = r.cargo(ctx, f, data)
		case "go":
			cs, err = r.gomod(ctx, f, data)
		case "python":
			cs, err = r.python(ctx, f, data)
		case "maven":
			cs, err = r.maven(ctx, f, data)
		case "terraform":
			cs = terraform(f, data)
		case "helm":
			cs = helm(f, data)
		case "tsconfig":
			cs, err = r.tsconfig(ctx, f, data)
		}
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			out = append(out, scannedCand{c, lone[f]})
		}
	}
	return out, nil
}

// kindOf names the ecosystem whose manifest f is, or "".
func kindOf(f string) string {
	base := path.Base(f)
	switch {
	case base == "package.json":
		return "npm"
	case base == "Cargo.toml":
		return "cargo"
	case base == "go.mod":
		return "go"
	case base == "pyproject.toml":
		return "python"
	case base == "pom.xml":
		return "maven"
	case strings.HasSuffix(base, ".tf"):
		return "terraform"
	case base == "Chart.yaml":
		return "helm"
	case strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json"):
		return "tsconfig"
	}
	return ""
}

func inSkipDir(f string) bool {
	for _, seg := range strings.Split(path.Dir(f), "/") {
		if skipDirs[seg] {
			return true
		}
	}
	return false
}

// read returns f's contents, or nil if it doesn't exist.
func (r *resolver) read(ctx context.Context, f string) ([]byte, error) {
	if data, ok := r.files[f]; ok {
		return data, nil
	}
	k, err := r.src.Stat(ctx, f)
	if err != nil {
		return nil, err
	}
	var data []byte
	if k == File {
		if data, err = r.src.ReadFile(ctx, f); err != nil {
			return nil, err
		}
	}
	r.files[f] = data
	return data, nil
}

// ancestors returns dir and each directory above it, up to the root ("").
func ancestors(dir string) []string {
	out := []string{dir}
	for dir != "" {
		if dir = path.Dir(dir); dir == "." {
			dir = ""
		}
		out = append(out, dir)
	}
	return out
}

// join resolves rel against dir. It returns "" for paths outside the repo,
// the root itself, and absolute paths.
func join(dir, rel string) string {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "://") {
		return ""
	}
	p := path.Join(dir, rel)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}

// inParentDir reports whether file f sits directly in a parent directory of
// one of paths (the root included).
func inParentDir(paths []string, f string) bool {
	d := dirOf(f)
	for _, s := range paths {
		if d == "" || strings.HasPrefix(s, d+"/") {
			return true
		}
	}
	return false
}

// covered reports whether p is one of paths or under one.
func covered(paths []string, p string) bool {
	for _, s := range paths {
		if s == "" || p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// glob expands a workspace-members pattern (*, ?, [..] per segment; ** for
// any depth) against the directories under base.
func (r *resolver) glob(ctx context.Context, base, pattern string) ([]string, error) {
	pattern = strings.Trim(strings.TrimPrefix(path.Clean(pattern), "./"), "/")
	if pattern == "" || pattern == "." || strings.HasPrefix(pattern, "../") {
		if p := join(base, pattern); p != "" {
			return []string{p}, nil
		}
		return nil, nil
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return []string{join(base, pattern)}, nil
	}
	var out []string
	var walk func(dir string, segs []string, depth int) error
	walk = func(dir string, segs []string, depth int) error {
		if len(segs) == 0 {
			out = append(out, dir)
			return nil
		}
		if !strings.ContainsAny(segs[0], "*?[") {
			next := path.Join(dir, segs[0])
			if k, err := r.src.Stat(ctx, next); err != nil || k != Dir {
				return err
			}
			return walk(next, segs[1:], depth)
		}
		if segs[0] == "**" {
			if err := walk(dir, segs[1:], depth); err != nil {
				return err
			}
			if depth >= 6 {
				return nil
			}
		}
		entries, err := r.src.ReadDir(ctx, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.Dir || skipDirs[e.Name] || strings.HasPrefix(e.Name, ".") {
				continue
			}
			child := path.Join(dir, e.Name)
			if segs[0] == "**" {
				if err := walk(child, segs, depth+1); err != nil {
					return err
				}
			} else if ok, _ := path.Match(segs[0], e.Name); ok {
				if err := walk(child, segs[1:], depth); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(base, strings.Split(pattern, "/"), 0); err != nil {
		return nil, err
	}
	sort.Strings(out)
	return dedupe(out), nil
}

func dedupe(s []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// members expands include patterns minus exclude ones under base.
func (r *resolver) members(ctx context.Context, base string, include, exclude []string) ([]string, error) {
	skip := map[string]bool{}
	for _, pat := range include {
		if strings.HasPrefix(pat, "!") {
			exclude = append(exclude, pat)
		}
	}
	for _, pat := range exclude {
		ms, err := r.glob(ctx, base, strings.TrimPrefix(pat, "!"))
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			skip[m] = true
		}
	}
	var out []string
	for _, pat := range include {
		if strings.HasPrefix(pat, "!") {
			continue
		}
		ms, err := r.glob(ctx, base, pat)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if !skip[m] && m != "" {
				out = append(out, m)
			}
		}
	}
	return dedupe(out), nil
}
