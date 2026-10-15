// Package repo reads a remote repository through the object cache. It holds
// no local clone: every object is fetched by id on first use.
package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/wire"
)

// ErrNotExist means a path or ref was not found.
var ErrNotExist = errors.New("does not exist")

// Repo is a remote seen through the shared cache.
type Repo struct {
	Remote string
	Cache  *cache.Store
	sess   *wire.Session
}

// Open prepares a Repo. The network session is opened lazily.
func Open(remote string) (*Repo, error) {
	dir, err := cache.Dir()
	if err != nil {
		return nil, err
	}
	c, err := cache.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Repo{Remote: remote, Cache: c}, nil
}

// Close releases the network session, if any.
func (r *Repo) Close() error {
	if r.sess == nil {
		return nil
	}
	return r.sess.Close()
}

func (r *Repo) session(ctx context.Context) (*wire.Session, error) {
	if r.sess == nil {
		s, err := wire.Open(ctx, r.Remote)
		if err != nil {
			return nil, err
		}
		r.sess = s
	}
	return r.sess, nil
}

// Resolve turns a rev (full oid, HEAD, branch, tag, or full ref name) into a
// commit id. Refs are always asked of the server, never cached.
func (r *Repo) Resolve(ctx context.Context, rev string) (plumbing.Hash, error) {
	if h, ok := plumbing.FromHex(rev); ok && len(rev) == 40 {
		return h, nil
	}
	s, err := r.session(ctx)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if !s.Stateless() {
		// Refs are never cached, and a long-lived server process may cache
		// them for us; read them over a fresh connection.
		if s, err = wire.Open(ctx, r.Remote); err != nil {
			return plumbing.ZeroHash, err
		}
		defer s.Close()
	}
	candidates := []string{rev, "refs/heads/" + rev, "refs/tags/" + rev}
	if rev == "HEAD" || strings.HasPrefix(rev, "refs/") {
		candidates = []string{rev}
	}
	refs, err := s.LsRefs(ctx, candidates)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	// ref-prefix is a prefix match; pick the first exact hit in candidate order.
	for _, c := range candidates {
		for _, ref := range refs {
			if ref.Name != c || ref.OID.IsZero() {
				continue
			}
			if !ref.Peeled.IsZero() {
				return ref.Peeled, nil
			}
			return ref.OID, nil
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("rev %q: %w", rev, ErrNotExist)
}

// Ensure makes every id in hs present in the cache, fetching missing ones
// in a single round trip.
func (r *Repo) Ensure(ctx context.Context, hs ...plumbing.Hash) error {
	var missing []plumbing.Hash
	seen := map[plumbing.Hash]bool{}
	for _, h := range hs {
		if !seen[h] && !r.Cache.Has(h) {
			missing = append(missing, h)
		}
		seen[h] = true
	}
	if len(missing) == 0 {
		return nil
	}
	for start := 0; start < len(missing); start += maxWants {
		end := min(start+maxWants, len(missing))
		if err := r.fetch(ctx, missing[start:end], wire.FilterExact); err != nil {
			return err
		}
	}
	for _, h := range missing {
		if !r.Cache.Has(h) {
			return fmt.Errorf("server did not send %s", h)
		}
	}
	return nil
}

// maxWants caps object ids per fetch request.
const maxWants = 1000

func (r *Repo) fetch(ctx context.Context, wants []plumbing.Hash, filter packp.Filter) error {
	s, err := r.session(ctx)
	if err != nil {
		return err
	}
	objs, err := s.Fetch(ctx, wants, filter)
	if err != nil {
		return err
	}
	for _, o := range objs {
		if err := r.Cache.Put(o); err != nil {
			return err
		}
	}
	return nil
}

// EnsureTrees fetches every tree under the given trees in one round trip
// per call (filter blob:none), so whole-directory walks need no further
// requests. Trees already cached are not skipped: the server can't be told
// which subtrees we have without commit history.
func (r *Repo) EnsureTrees(ctx context.Context, roots ...plumbing.Hash) error {
	if len(roots) == 0 {
		return nil
	}
	return r.fetch(ctx, roots, wire.FilterNoBlobs)
}

// FileEntry is one file found by Walk.
type FileEntry struct {
	Path string
	Mode filemode.FileMode
	Hash plumbing.Hash
}

// Walk lists every file under tree h (from cache; call EnsureTrees first),
// with paths prefixed by prefix. Submodules are skipped.
func (r *Repo) Walk(ctx context.Context, h plumbing.Hash, prefix string, fn func(FileEntry) error) error {
	t, err := r.Tree(ctx, h)
	if err != nil {
		return err
	}
	for _, e := range t.Entries {
		p := path.Join(prefix, e.Name)
		switch e.Mode {
		case filemode.Dir:
			if err := r.Walk(ctx, e.Hash, p, fn); err != nil {
				return err
			}
		case filemode.Submodule:
			continue
		default:
			if err := fn(FileEntry{Path: p, Mode: e.Mode, Hash: e.Hash}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Object returns h, fetching it if needed.
func (r *Repo) Object(ctx context.Context, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if err := r.Ensure(ctx, h); err != nil {
		return nil, err
	}
	return r.Cache.Get(h)
}

// Commit returns the commit h.
func (r *Repo) Commit(ctx context.Context, h plumbing.Hash) (*object.Commit, error) {
	o, err := r.Object(ctx, h)
	if err != nil {
		return nil, err
	}
	c := &object.Commit{}
	if err := c.Decode(o); err != nil {
		return nil, fmt.Errorf("commit %s: %w", h, err)
	}
	return c, nil
}

// Tree returns the tree h.
func (r *Repo) Tree(ctx context.Context, h plumbing.Hash) (*object.Tree, error) {
	o, err := r.Object(ctx, h)
	if err != nil {
		return nil, err
	}
	t := &object.Tree{}
	if err := t.Decode(o); err != nil {
		return nil, fmt.Errorf("tree %s: %w", h, err)
	}
	return t, nil
}

// Blob returns the contents of blob h.
func (r *Repo) Blob(ctx context.Context, h plumbing.Hash) ([]byte, error) {
	o, err := r.Object(ctx, h)
	if err != nil {
		return nil, err
	}
	if o.Type() != plumbing.BlobObject {
		return nil, fmt.Errorf("%s is a %s, not a blob", h, o.Type())
	}
	rd, err := o.Reader()
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	buf := make([]byte, o.Size())
	_, err = io.ReadFull(rd, buf)
	return buf, err
}

// Lookup walks p from root tree, fetching one tree per path segment. An
// empty p returns the root itself as a directory entry.
func (r *Repo) Lookup(ctx context.Context, root plumbing.Hash, p string) (object.TreeEntry, error) {
	entry := object.TreeEntry{Name: "", Mode: filemode.Dir, Hash: root}
	p = strings.Trim(path.Clean("/"+p), "/")
	if p == "" {
		return entry, nil
	}
	for _, seg := range strings.Split(p, "/") {
		if entry.Mode != filemode.Dir {
			return object.TreeEntry{}, fmt.Errorf("path %q: %s is not a directory: %w", p, entry.Name, ErrNotExist)
		}
		t, err := r.Tree(ctx, entry.Hash)
		if err != nil {
			return object.TreeEntry{}, err
		}
		found := false
		for _, e := range t.Entries {
			if e.Name == seg {
				entry, found = e, true
				break
			}
		}
		if !found {
			return object.TreeEntry{}, fmt.Errorf("path %q: %w", p, ErrNotExist)
		}
	}
	return entry, nil
}

// SplitSpec splits "rev:path" (path optional) into its parts.
func SplitSpec(spec string) (rev, p string) {
	rev, p, _ = strings.Cut(spec, ":")
	if rev == "" {
		rev = "HEAD"
	}
	return rev, p
}

// RootTree resolves rev and returns its commit id and root tree id.
func (r *Repo) RootTree(ctx context.Context, rev string) (commit, tree plumbing.Hash, err error) {
	commit, err = r.Resolve(ctx, rev)
	if err != nil {
		return
	}
	c, err := r.Commit(ctx, commit)
	if err != nil {
		return
	}
	return commit, c.TreeHash, nil
}
