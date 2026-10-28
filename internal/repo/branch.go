package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"

	"github.com/baz-scm/gion/internal/wire"
)

// ErrBranchExists means the new name of a rename is already taken.
var ErrBranchExists = errors.New("branch already exists")

// RenameBranch creates branch to at the tip of from and deletes from, in one
// atomic push: either both happen or neither does. The default branch is
// refused, since deleting it would leave the remote's HEAD dangling.
// It returns the tip both names pointed at.
func (r *Repo) RenameBranch(ctx context.Context, from, to string) (plumbing.Hash, error) {
	from = strings.TrimPrefix(from, "refs/heads/")
	to = strings.TrimPrefix(to, "refs/heads/")
	oldRef, newRef := plumbing.NewBranchReferenceName(from), plumbing.NewBranchReferenceName(to)
	if err := newRef.Validate(); err != nil || to == "" {
		return plumbing.ZeroHash, fmt.Errorf("invalid branch name %q", to)
	}
	if from == to {
		return plumbing.ZeroHash, fmt.Errorf("branch is already named %s", to)
	}
	tip, err := r.Resolve(ctx, oldRef.String())
	if errors.Is(err, ErrNotExist) {
		return plumbing.ZeroHash, fmt.Errorf("branch %s doesn't exist", from)
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if def, err := r.DefaultBranch(ctx); err == nil && def == from {
		return plumbing.ZeroHash, fmt.Errorf("%s is the default branch; rename it in the forge's settings", from)
	}

	p, err := pack(nil) // the server has the commit already
	if err != nil {
		return plumbing.ZeroHash, err
	}
	err = wire.Push(ctx, r.Remote, []wire.Update{
		{Ref: newRef, Old: plumbing.ZeroHash, New: tip},
		{Ref: oldRef, Old: tip, New: plumbing.ZeroHash},
	}, p)
	if err != nil {
		if wire.IsRefRejected(err) {
			if _, rerr := r.Resolve(ctx, newRef.String()); rerr == nil {
				return plumbing.ZeroHash, fmt.Errorf("%s: %w", to, ErrBranchExists)
			}
			if now, rerr := r.Resolve(ctx, oldRef.String()); rerr == nil && now != tip {
				return plumbing.ZeroHash, fmt.Errorf("%s moved during the rename; nothing changed, try again", from)
			}
		}
		return plumbing.ZeroHash, err
	}
	return tip, nil
}
