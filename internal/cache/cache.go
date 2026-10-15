// Package cache is the shared, content-addressed object store every `gion`
// invocation reads and writes. Objects are immutable, so concurrent processes
// need no locks: each write goes to a temp file and is renamed into place, and
// two writers of the same id produce identical bytes.
//
// Layout is git's loose-object format: <dir>/<oid[:2]>/<oid[2:]>. The
// default dir is ~/.gion/objects; path indexes live in ~/.gion/index. The
// whole root is capped in size (see Evict).
package cache

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/objfile"
	"github.com/go-git/go-git/v6/storage/memory"
)

// ErrNotFound means the object is not cached.
var ErrNotFound = errors.New("object not in cache")

// Store is an on-disk object cache.
type Store struct {
	dir string
	mem *memory.Storage // object factory only; never holds data
}

// Dir returns the object directory: $GION_CACHE, else ~/.gion/objects.
func Dir() (string, error) {
	if d := os.Getenv("GION_CACHE"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gion", "objects"), nil
}

// Root is the directory holding objects/ and index/; it's what the size cap
// covers.
func Root() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Dir(d), nil
}

// cacheDirTag marks the root as a cache for backup tools (restic, borg, tar
// --exclude-caches): https://bford.info/cachedir/
const cacheDirTag = "Signature: 8a477f597d28d172789f06886806bc55\n# gion object cache; safe to delete, it refills on demand.\n"

// Open opens (creating if needed) the cache at dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "tmp"), 0o755); err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}
	tag := filepath.Join(filepath.Dir(dir), "CACHEDIR.TAG")
	if _, err := os.Stat(tag); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(tag, []byte(cacheDirTag), 0o644)
	}
	return &Store{dir: dir, mem: memory.NewStorage()}, nil
}

// touchAfter is how stale an object's mtime may get before a read refreshes
// it. Eviction is least-recently-used by mtime; an hour's precision is plenty
// and saves a write on most reads.
const touchAfter = time.Hour

func (s *Store) path(h plumbing.Hash) string {
	hex := h.String()
	return filepath.Join(s.dir, hex[:2], hex[2:])
}

// Has reports whether h is cached.
func (s *Store) Has(h plumbing.Hash) bool {
	_, err := os.Stat(s.path(h))
	return err == nil
}

// Get reads h from the cache.
func (s *Store) Get(h plumbing.Hash) (plumbing.EncodedObject, error) {
	f, err := os.Open(s.path(h))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && time.Since(fi.ModTime()) > touchAfter {
		now := time.Now()
		_ = os.Chtimes(s.path(h), now, now)
	}

	r, err := objfile.NewReader(f, format.SHA1)
	if err != nil {
		return nil, fmt.Errorf("cache: read %s: %w", h, err)
	}
	defer r.Close()
	t, size, err := r.Header()
	if err != nil {
		return nil, fmt.Errorf("cache: read %s: %w", h, err)
	}

	obj := s.mem.NewEncodedObject()
	obj.SetType(t)
	obj.SetSize(size)
	w, err := obj.Writer()
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(w, r); err != nil {
		return nil, fmt.Errorf("cache: read %s: %w", h, err)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if obj.Hash() != h {
		// Corrupt entry: drop it so the next read refetches.
		_ = os.Remove(s.path(h))
		return nil, fmt.Errorf("cache: %s failed hash check", h)
	}
	return obj, nil
}

// Put writes obj to the cache. Writing an object that exists is a no-op.
func (s *Store) Put(obj plumbing.EncodedObject) error {
	h := obj.Hash()
	if s.Has(h) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "tmp"), "obj-")
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	w := objfile.NewWriter(tmp, format.SHA1)
	err = w.WriteHeader(obj.Type(), obj.Size())
	if err == nil {
		var rd io.ReadCloser
		if rd, err = obj.Reader(); err == nil {
			_, err = io.Copy(w, rd)
			rd.Close()
		}
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("cache: write %s: %w", h, err)
	}

	dst := s.path(h)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return os.Rename(tmp.Name(), dst)
}

// New returns an empty object for the caller to fill and Put.
func (s *Store) New() plumbing.EncodedObject { return s.mem.NewEncodedObject() }
