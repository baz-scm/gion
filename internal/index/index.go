// Package index keeps a list of every file path in a repository at one
// commit, so whole-repo path searches need no clone. The first build fetches
// every tree once (no blobs); later builds diff from the last indexed commit
// and fetch only the trees that changed.
package index

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"

	"github.com/baz-scm/gion/internal/cache"
	"github.com/baz-scm/gion/internal/repo"
	"github.com/baz-scm/gion/internal/wire"
)

// Entry is one file.
type Entry struct {
	Path string
	Mode filemode.FileMode
	OID  plumbing.Hash
}

// Index is every file at Commit, sorted by path.
type Index struct {
	Commit  plumbing.Hash
	Entries []Entry
}

// How an index was brought up to date.
const (
	Hit   = "cached"
	Delta = "delta"
	Full  = "full"
)

// file returns where remote's index lives: next to the object cache.
func file(remote string) (string, error) {
	dir, err := cache.Dir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(wire.RemoteURL(remote)))
	return filepath.Join(filepath.Dir(dir), "index", hex.EncodeToString(sum[:8])+".idx.gz"), nil
}

// At returns the index of r at commit, and how it was obtained.
func At(ctx context.Context, r *repo.Repo, commit plumbing.Hash) (*Index, string, error) {
	path, err := file(r.Remote)
	if err != nil {
		return nil, "", err
	}
	prev, err := load(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		prev = nil // unreadable index: rebuild
	}
	if prev != nil && prev.Commit == commit {
		return prev, Hit, nil
	}

	c, err := r.Commit(ctx, commit)
	if err != nil {
		return nil, "", err
	}
	var idx *Index
	how := Full
	if prev != nil {
		if idx, err = delta(ctx, r, prev, c.TreeHash); err == nil {
			how = Delta
		}
	}
	if idx == nil {
		if idx, err = full(ctx, r, c.TreeHash); err != nil {
			return nil, "", err
		}
	}
	idx.Commit = commit
	return idx, how, save(path, idx)
}

func full(ctx context.Context, r *repo.Repo, tree plumbing.Hash) (*Index, error) {
	if err := r.EnsureTrees(ctx, tree); err != nil {
		return nil, err
	}
	idx := &Index{}
	err := r.Walk(ctx, tree, "", func(f repo.FileEntry) error {
		idx.Entries = append(idx.Entries, Entry{Path: f.Path, Mode: f.Mode, OID: f.Hash})
		return nil
	})
	sortEntries(idx.Entries)
	return idx, err
}

func delta(ctx context.Context, r *repo.Repo, prev *Index, tree plumbing.Hash) (*Index, error) {
	pc, err := r.Commit(ctx, prev.Commit)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Entry, len(prev.Entries))
	for _, e := range prev.Entries {
		m[e.Path] = e
	}
	err = r.DiffTrees(ctx, pc.TreeHash, tree, nil, func(p string, _, n *repo.FileEntry) error {
		if n == nil {
			delete(m, p)
		} else {
			m[p] = Entry{Path: p, Mode: n.Mode, OID: n.Hash}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	idx := &Index{Entries: make([]Entry, 0, len(m))}
	for _, e := range m {
		idx.Entries = append(idx.Entries, e)
	}
	sortEntries(idx.Entries)
	return idx, nil
}

func sortEntries(es []Entry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Path < es[j].Path })
}

// Format: gzip of a "gion-index 1 <commit>" header, then "<mode> <oid> <path>"
// lines.
const magic = "gion-index 1 "

func load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), magic) {
		return nil, errors.New("index: bad header")
	}
	commit, ok := plumbing.FromHex(strings.TrimPrefix(sc.Text(), magic))
	if !ok {
		return nil, errors.New("index: bad commit")
	}
	idx := &Index{Commit: commit}
	for sc.Scan() {
		mode, rest, _ := strings.Cut(sc.Text(), " ")
		oid, p, _ := strings.Cut(rest, " ")
		m, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			return nil, fmt.Errorf("index: %w", err)
		}
		h, ok := plumbing.FromHex(oid)
		if !ok {
			return nil, errors.New("index: bad oid")
		}
		idx.Entries = append(idx.Entries, Entry{Path: p, Mode: filemode.FileMode(m), OID: h})
	}
	return idx, sc.Err()
}

func save(path string, idx *Index) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "idx-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	zw := gzip.NewWriter(tmp)
	w := bufio.NewWriter(zw)
	fmt.Fprintf(w, "%s%s\n", magic, idx.Commit)
	for _, e := range idx.Entries {
		fmt.Fprintf(w, "%o %s %s\n", uint32(e.Mode), e.OID, e.Path)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
