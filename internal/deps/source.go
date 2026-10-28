package deps

import (
	"context"
	"errors"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"

	"github.com/baz-scm/gion/internal/repo"
)

// RepoSource reads tree root of r: trees as needed, blobs only for the
// manifests read.
type RepoSource struct {
	r     *repo.Repo
	root  plumbing.Hash
	blobs map[string]plumbing.Hash // files seen in walks
}

// NewRepoSource reads tree root (a commit's root tree) of r.
func NewRepoSource(r *repo.Repo, root plumbing.Hash) *RepoSource {
	return &RepoSource{r: r, root: root, blobs: map[string]plumbing.Hash{}}
}

func (s *RepoSource) Stat(ctx context.Context, p string) (Kind, error) {
	e, err := s.r.Lookup(ctx, s.root, p)
	if errors.Is(err, repo.ErrNotExist) {
		return Missing, nil
	}
	if err != nil {
		return Missing, err
	}
	switch e.Mode {
	case filemode.Dir:
		return Dir, nil
	case filemode.Submodule:
		return Missing, nil
	}
	s.blobs[p] = e.Hash
	return File, nil
}

func (s *RepoSource) ReadFile(ctx context.Context, p string) ([]byte, error) {
	h, ok := s.blobs[p]
	if !ok {
		e, err := s.r.Lookup(ctx, s.root, p)
		if err != nil {
			return nil, err
		}
		h = e.Hash
	}
	return s.r.Blob(ctx, h)
}

func (s *RepoSource) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	e, err := s.r.Lookup(ctx, s.root, dir)
	if errors.Is(err, repo.ErrNotExist) {
		return nil, nil
	}
	if err != nil || e.Mode != filemode.Dir {
		return nil, err
	}
	t, err := s.r.Tree(ctx, e.Hash)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(t.Entries))
	for _, te := range t.Entries {
		if te.Mode == filemode.Submodule {
			continue
		}
		out = append(out, Entry{Name: te.Name, Dir: te.Mode == filemode.Dir})
	}
	return out, nil
}

func (s *RepoSource) Files(ctx context.Context, p string) ([]string, error) {
	e, err := s.r.Lookup(ctx, s.root, p)
	if errors.Is(err, repo.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch e.Mode {
	case filemode.Submodule:
		return nil, nil
	case filemode.Dir:
	default:
		s.blobs[p] = e.Hash
		return []string{p}, nil
	}
	if err := s.r.EnsureTrees(ctx, e.Hash); err != nil {
		return nil, err
	}
	var out []string
	err = s.r.Walk(ctx, e.Hash, p, func(f repo.FileEntry) error {
		s.blobs[f.Path] = f.Hash
		out = append(out, f.Path)
		return nil
	})
	return out, err
}

// Prefetch fetches the trees under the directories among paths in one
// round trip, and the files' contents in another.
func (s *RepoSource) Prefetch(ctx context.Context, paths []string) error {
	var trees, blobs []plumbing.Hash
	for _, p := range paths {
		h, ok := s.blobs[p]
		if !ok {
			e, err := s.r.Lookup(ctx, s.root, p)
			if errors.Is(err, repo.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if e.Mode == filemode.Dir {
				trees = append(trees, e.Hash)
				continue
			}
			if e.Mode == filemode.Submodule {
				continue
			}
			h = e.Hash
			s.blobs[p] = h
		}
		blobs = append(blobs, h)
	}
	if err := s.r.EnsureTrees(ctx, trees...); err != nil {
		return err
	}
	return s.r.Ensure(ctx, blobs...)
}
