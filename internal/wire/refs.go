// Package wire speaks git protocol v2 to a remote. Refs are never cached:
// every call goes to the server.
package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/transport"

	"github.com/baz-scm/gion/internal/auth"
)

// DefaultPrefixes keeps ls-refs small on repos with huge tag/PR namespaces.
var DefaultPrefixes = []string{"HEAD", "refs/heads/"}

// Ref is one ls-refs line. Unlike go-git's plumbing.Reference, a symref keeps
// both its target and the oid it resolves to, so HEAD resolves in one round trip.
type Ref struct {
	Name   string
	OID    plumbing.Hash // zero if unborn
	Target string        // symref target, empty for plain refs
	Peeled plumbing.Hash // peeled oid for annotated tags, zero otherwise
}

// RemoteURL expands "owner/repo" to a GitHub HTTPS URL; full URLs pass through.
func RemoteURL(arg string) string {
	if strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") {
		return arg
	}
	return "https://github.com/" + strings.TrimSuffix(arg, ".git") + ".git"
}

// Session is an open v2 upload-pack session.
type Session struct {
	sess      transport.Session
	cmd       transport.Commander
	cl        *client.Client
	stateless bool
}

// Stateless reports whether each command is an independent request (HTTP).
// Persistent connections (git://, ssh://) talk to one long-lived server
// process that may cache refs, so ref reads there need a fresh session.
func (s *Session) Stateless() bool { return s.stateless }

// dial handshakes with remote for service.
func dial(ctx context.Context, remote, service string, ver protocol.Version) (transport.Session, *client.Client, error) {
	u, err := transport.ParseURL(RemoteURL(remote))
	if err != nil {
		return nil, nil, fmt.Errorf("parse remote: %w", err)
	}

	var opts []client.Option
	if u.Scheme == "https" || u.Scheme == "http" {
		a, err := auth.ForURL(ctx, u)
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, client.WithHTTPAuth(a))
		if tracing {
			opts = append(opts, client.WithHTTPClient(&http.Client{Transport: traceTransport{http.DefaultTransport}}))
		}
	}

	cl := client.New(opts...)
	sess, err := cl.Handshake(ctx, &transport.Request{URL: u, Command: service, Protocol: ver})
	if err != nil {
		cl.Close()
		return nil, nil, fmt.Errorf("handshake: %w", err)
	}
	return sess, cl, nil
}

// Open performs the v2 upload-pack handshake against remote.
func Open(ctx context.Context, remote string) (*Session, error) {
	sess, cl, err := dial(ctx, remote, transport.UploadPackService, protocol.V2)
	if err != nil {
		return nil, err
	}
	cmd, ok := sess.(transport.Commander)
	if !ok {
		sess.Close()
		cl.Close()
		return nil, errors.New("handshake: server did not negotiate protocol v2")
	}
	scheme := RemoteURL(remote)
	stateless := strings.HasPrefix(scheme, "http://") || strings.HasPrefix(scheme, "https://")
	return &Session{sess: sess, cmd: cmd, cl: cl, stateless: stateless}, nil
}

// Close releases the session.
func (s *Session) Close() error {
	return errors.Join(s.sess.Close(), s.cl.Close())
}

// LsRefs runs a v2 ls-refs limited to prefixes.
func (s *Session) LsRefs(ctx context.Context, prefixes []string) ([]Ref, error) {
	args := &packp.LsRefsArgs{Peel: true, Symrefs: true, Unborn: true, RefPrefixes: prefixes}
	out := &lsRefsOutput{}
	if err := s.cmd.Command(ctx, "ls-refs", args, out); err != nil {
		return nil, fmt.Errorf("ls-refs: %w", err)
	}
	return out.refs, nil
}

// lsRefsOutput decodes ls-refs lines:
//
//	<oid|unborn> SP <refname> [SP symref-target:<t>] [SP peeled:<oid>]
type lsRefsOutput struct{ refs []Ref }

func (o *lsRefsOutput) Decode(r io.Reader) error {
	for {
		l, pkt, err := pktline.ReadLine(r)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if l == pktline.Flush {
			return nil
		}
		line := strings.TrimSuffix(string(pkt), "\n")
		if line == "" {
			continue
		}
		ref, err := parseRefLine(line)
		if err != nil {
			return err
		}
		o.refs = append(o.refs, ref)
	}
}

func parseRefLine(line string) (Ref, error) {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return Ref{}, fmt.Errorf("malformed ref line: %q", line)
	}
	ref := Ref{Name: parts[1]}
	if parts[0] != "unborn" {
		h, ok := plumbing.FromHex(parts[0])
		if !ok {
			return Ref{}, fmt.Errorf("malformed oid in ref line: %q", line)
		}
		ref.OID = h
	}
	for _, attr := range parts[2:] {
		if t, ok := strings.CutPrefix(attr, "symref-target:"); ok {
			ref.Target = t
		} else if p, ok := strings.CutPrefix(attr, "peeled:"); ok {
			h, ok := plumbing.FromHex(p)
			if !ok {
				return Ref{}, fmt.Errorf("malformed peeled oid: %q", line)
			}
			ref.Peeled = h
		}
	}
	return ref, nil
}
