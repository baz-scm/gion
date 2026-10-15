package wire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/memory"
)

// Update is one ref compare-and-swap: the server applies it only if the ref
// still points at Old (zero Old means "must not exist").
type Update struct {
	Ref plumbing.ReferenceName
	Old plumbing.Hash
	New plumbing.Hash
}

// Push sends pack and applies updates atomically via receive-pack. Protocol v2
// has no push, so this uses v0. pack only needs the new objects: the server
// already has everything they reference.
func Push(ctx context.Context, remote string, updates []Update, pack []byte) error {
	sess, cl, err := dial(ctx, remote, transport.ReceivePackService, protocol.V0)
	if err != nil {
		return err
	}
	defer cl.Close()
	defer sess.Close()

	cmds := make([]*packp.Command, len(updates))
	for i, u := range updates {
		cmds[i] = &packp.Command{Name: u.Ref, Old: u.Old, New: u.New}
	}
	err = sess.Push(ctx, memory.NewStorage(), &transport.PushRequest{
		Packfile: io.NopCloser(bytes.NewReader(pack)),
		Commands: cmds,
		Atomic:   len(cmds) > 1,
		Quiet:    true,
	})
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// IsRefRejected reports whether the server refused a ref update (as opposed
// to a transport or unpack failure). Servers word the reason differently
// ("incorrect old value provided", "stale info", "cannot lock ref"), so
// callers re-read the ref to learn whether they lost a race.
func IsRefRejected(err error) bool {
	var cerr packp.CommandStatusErr
	return errors.As(err, &cerr)
}
