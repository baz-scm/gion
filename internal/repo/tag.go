package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/baz-scm/gion/internal/wire"
)

// ErrTagExists means the tag is already on the remote. Tags are never moved:
// module proxies and release tooling cache them permanently.
var ErrTagExists = errors.New("tag already exists")

// TagOptions describes one tag to create.
type TagOptions struct {
	Name string
	Rev  string // commit to tag; default HEAD
	// Message makes an annotated tag (a tag object with tagger and date).
	// Empty makes a lightweight tag pointing straight at the commit.
	Message string
	Who     People
}

// TagResult is what was pushed.
type TagResult struct {
	Ref    plumbing.ReferenceName
	Commit plumbing.Hash
	Object plumbing.Hash // tag object for annotated tags, else the commit
}

// Tag creates a tag on the remote. It only ever creates: the push expects
// the ref to be absent, so an existing tag is refused by the server.
func (r *Repo) Tag(ctx context.Context, o TagOptions) (TagResult, error) {
	ref := plumbing.NewTagReferenceName(strings.TrimPrefix(o.Name, "refs/tags/"))
	if err := ref.Validate(); err != nil || o.Name == "" {
		return TagResult{}, fmt.Errorf("invalid tag name %q", o.Name)
	}
	if o.Rev == "" {
		o.Rev = "HEAD"
	}
	commit, err := r.Resolve(ctx, o.Rev)
	if err != nil {
		return TagResult{}, err
	}
	if _, err := r.Commit(ctx, commit); err != nil {
		return TagResult{}, fmt.Errorf("%s: %w", o.Rev, err)
	}

	res := TagResult{Ref: ref, Commit: commit, Object: commit}
	var objs []plumbing.EncodedObject
	if o.Message != "" {
		msg := o.Message
		if !strings.HasSuffix(msg, "\n") {
			msg += "\n"
		}
		t := &object.Tag{
			Name:       ref.Short(),
			Tagger:     o.Who.Committer,
			Message:    msg,
			TargetType: plumbing.CommitObject,
			Target:     commit,
		}
		obj := r.Cache.New()
		if err := t.Encode(obj); err != nil {
			return TagResult{}, err
		}
		if err := r.Cache.Put(obj); err != nil {
			return TagResult{}, err
		}
		objs = append(objs, obj)
		res.Object = obj.Hash()
	}

	p, err := pack(objs)
	if err != nil {
		return TagResult{}, err
	}
	err = wire.Push(ctx, r.Remote, []wire.Update{{Ref: ref, Old: plumbing.ZeroHash, New: res.Object}}, p)
	if err != nil {
		if wire.IsRefRejected(err) {
			if _, rerr := r.Resolve(ctx, ref.String()); rerr == nil {
				return TagResult{}, fmt.Errorf("%s: %w; tags are never moved, so pick a new version", ref.Short(), ErrTagExists)
			}
		}
		return TagResult{}, err
	}
	return res, nil
}
