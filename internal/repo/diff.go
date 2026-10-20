package repo

import (
	"context"
	"path"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// DiffTrees reports every file that differs between trees a and b (either
// may be zero, meaning empty). Subtrees with equal ids are skipped without
// being read, and the subtrees that do differ are fetched one batch per
// level, so the cost follows the size of the change, not of the repo, and
// the round trips follow the depth of the tree.
//
// descend decides whether a directory is worth entering (e.g. a sparse
// scope); nil means always. fn gets nil for a side where the file is absent.
// A directory's files are reported before anything below it. Submodules are
// ignored.
func (r *Repo) DiffTrees(ctx context.Context, a, b plumbing.Hash, descend func(dir string) bool, fn func(p string, old, new *FileEntry) error) error {
	level := []treePair{{"", a, b}}
	for len(level) > 0 {
		var want []plumbing.Hash
		for _, s := range level {
			for _, h := range []plumbing.Hash{s.a, s.b} {
				if !h.IsZero() {
					want = append(want, h)
				}
			}
		}
		if err := r.Ensure(ctx, want...); err != nil {
			return err
		}
		var next []treePair
		for _, s := range level {
			subs, err := r.diffDir(ctx, s, descend, fn)
			if err != nil {
				return err
			}
			next = append(next, subs...)
		}
		level = next
	}
	return nil
}

// treePair is one directory on both sides of a diff.
type treePair struct {
	p    string
	a, b plumbing.Hash
}

func (r *Repo) entries(ctx context.Context, h plumbing.Hash) (map[string]object.TreeEntry, error) {
	m := map[string]object.TreeEntry{}
	if h.IsZero() {
		return m, nil
	}
	t, err := r.Tree(ctx, h)
	if err != nil {
		return nil, err
	}
	for _, e := range t.Entries {
		if e.Mode != filemode.Submodule {
			m[e.Name] = e
		}
	}
	return m, nil
}

// diffDir reports the files that differ directly in one directory, and
// returns the subdirectories that differ, for the next level.
func (r *Repo) diffDir(ctx context.Context, d treePair, descend func(string) bool, fn func(string, *FileEntry, *FileEntry) error) ([]treePair, error) {
	if d.a == d.b {
		return nil, nil
	}
	ea, err := r.entries(ctx, d.a)
	if err != nil {
		return nil, err
	}
	eb, err := r.entries(ctx, d.b)
	if err != nil {
		return nil, err
	}

	names := map[string]bool{}
	for n := range ea {
		names[n] = true
	}
	for n := range eb {
		names[n] = true
	}

	var subs []treePair
	for n := range names {
		x, inA := ea[n]
		y, inB := eb[n]
		if inA && inB && x.Hash == y.Hash && x.Mode == y.Mode {
			continue
		}
		p := path.Join(d.p, n)
		var ta, tb plumbing.Hash
		if inA && x.Mode == filemode.Dir {
			ta = x.Hash
		}
		if inB && y.Mode == filemode.Dir {
			tb = y.Hash
		}
		if (!ta.IsZero() || !tb.IsZero()) && (descend == nil || descend(p)) {
			subs = append(subs, treePair{p, ta, tb})
		}
		// Files on either side (including a file replaced by a directory or
		// the reverse) are reported directly.
		var fa, fb *FileEntry
		if inA && x.Mode != filemode.Dir {
			fa = &FileEntry{Path: p, Mode: x.Mode, Hash: x.Hash}
		}
		if inB && y.Mode != filemode.Dir {
			fb = &FileEntry{Path: p, Mode: y.Mode, Hash: y.Hash}
		}
		if fa != nil || fb != nil {
			if err := fn(p, fa, fb); err != nil {
				return nil, err
			}
		}
	}
	return subs, nil
}
