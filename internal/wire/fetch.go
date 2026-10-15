package wire

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/memory"
)

// FilterExact makes the server send exactly the wanted objects: wanted
// objects are never filtered, and tree:0 drops everything they reference.
const FilterExact = packp.Filter("tree:0")

// FilterNoBlobs sends wanted objects plus every tree under them.
const FilterNoBlobs = packp.Filter("blob:none")

// Fetch downloads wants by object id with the given filter and returns every
// object the server sent. No haves are sent: we never negotiate history.
//
// Filters only drop trees and blobs, never commits, so a commit want without
// depth would pull its entire history. Depth 1 keeps it to the commit itself.
func (s *Session) Fetch(ctx context.Context, wants []plumbing.Hash, filter packp.Filter) ([]plumbing.EncodedObject, error) {
	if len(wants) == 0 {
		return nil, nil
	}
	st := memory.NewStorage()
	err := s.sess.Fetch(ctx, st, &transport.FetchRequest{Wants: wants, Filter: filter, Depth: 1})
	if err != nil && !errors.Is(err, transport.ErrNoChange) {
		return nil, fmt.Errorf("fetch %d objects: %w", len(wants), err)
	}

	iter, err := st.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return nil, err
	}
	var objs []plumbing.EncodedObject
	err = iter.ForEach(func(o plumbing.EncodedObject) error {
		objs = append(objs, o)
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) {
		return nil, err
	}
	return objs, nil
}
