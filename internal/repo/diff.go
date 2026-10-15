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
// level, so the cost follows the size of the change, not of the repo.
//
// descend decides whether a directory is worth entering (e.g. a sparse
// scope); nil means always. fn gets nil for a side where the file is absent.
// Submodules are ignored.
func (r *Repo) DiffTrees(ctx context.Context, a, b plumbing.Hash, descend func(dir string) bool, fn func(p string, old, new *FileEntry) error) error {
	return r.diffDir(ctx, "", a, b, descend, fn)
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

func (r *Repo) diffDir(ctx context.Context, dir string, a, b plumbing.Hash, descend func(string) bool, fn func(string, *FileEntry, *FileEntry) error) error {
	if a == b {
		return nil
	}
	ea, err := r.entries(ctx, a)
	if err != nil {
		return err
	}
	eb, err := r.entries(ctx, b)
	if err != nil {
		return err
	}

	names := map[string]bool{}
	for n := range ea {
		names[n] = true
	}
	for n := range eb {
		names[n] = true
	}

	// Collect subtrees to enter, then fetch them all in one request.
	type sub struct {
		p    string
		a, b plumbing.Hash
	}
	var subs []sub
	var prefetch []plumbing.Hash
	for n := range names {
		x, inA := ea[n]
		y, inB := eb[n]
		if inA && inB && x.Hash == y.Hash && x.Mode == y.Mode {
			continue
		}
		p := path.Join(dir, n)
		var ta, tb plumbing.Hash
		if inA && x.Mode == filemode.Dir {
			ta = x.Hash
		}
		if inB && y.Mode == filemode.Dir {
			tb = y.Hash
		}
		if (!ta.IsZero() || !tb.IsZero()) && (descend == nil || descend(p)) {
			subs = append(subs, sub{p, ta, tb})
			for _, h := range []plumbing.Hash{ta, tb} {
				if !h.IsZero() {
					prefetch = append(prefetch, h)
				}
			}
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
				return err
			}
		}
	}
	if err := r.Ensure(ctx, prefetch...); err != nil {
		return err
	}
	for _, s := range subs {
		if err := r.diffDir(ctx, s.p, s.a, s.b, descend, fn); err != nil {
			return err
		}
	}
	return nil
}
