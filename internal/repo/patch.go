package repo

import (
	"bytes"
	"context"
	"io"
	"sort"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v6/plumbing/format/diff"
	"github.com/go-git/go-git/v6/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// FileDiff is one changed file between two trees. Old or New is nil when
// the file was added or deleted.
type FileDiff struct {
	Path     string
	Old, New *FileEntry
}

// Status is git's one-letter change code: A, D or M.
func (d FileDiff) Status() string {
	switch {
	case d.Old == nil:
		return "A"
	case d.New == nil:
		return "D"
	}
	return "M"
}

// Changes lists the files that differ between trees a and b, sorted by
// path. descend limits which directories are entered (nil: all); keep
// filters files (nil: all).
func (r *Repo) Changes(ctx context.Context, a, b plumbing.Hash, descend func(string) bool, keep func(string) bool) ([]FileDiff, error) {
	var out []FileDiff
	err := r.DiffTrees(ctx, a, b, descend, func(p string, o, n *FileEntry) error {
		if keep == nil || keep(p) {
			out = append(out, FileDiff{Path: p, Old: o, New: n})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// WritePatch writes ds as a unified diff (git diff format), fetching every
// needed blob in one round trip first. Content may override what a side
// holds (e.g. a workspace's files on disk); keys are blob ids.
func (r *Repo) WritePatch(ctx context.Context, w io.Writer, ds []FileDiff, content map[plumbing.Hash][]byte, contextLines int) error {
	var need []plumbing.Hash
	for _, d := range ds {
		for _, e := range []*FileEntry{d.Old, d.New} {
			if e != nil && e.Mode != filemode.Submodule {
				if _, ok := content[e.Hash]; !ok {
					need = append(need, e.Hash)
				}
			}
		}
	}
	if err := r.Ensure(ctx, need...); err != nil {
		return err
	}
	read := func(e *FileEntry) ([]byte, error) {
		if e == nil {
			return nil, nil
		}
		if b, ok := content[e.Hash]; ok {
			return b, nil
		}
		return r.Blob(ctx, e.Hash)
	}
	p := patch{}
	for _, d := range ds {
		a, err := read(d.Old)
		if err != nil {
			return err
		}
		b, err := read(d.New)
		if err != nil {
			return err
		}
		fp := filePatch{from: asFile(d.Old), to: asFile(d.New)}
		if isBinary(a) || isBinary(b) {
			fp.binary = true
		} else {
			for _, c := range diff.Do(string(a), string(b)) {
				op := fdiff.Equal
				switch c.Type {
				case diffmatchpatch.DiffInsert:
					op = fdiff.Add
				case diffmatchpatch.DiffDelete:
					op = fdiff.Delete
				}
				fp.chunks = append(fp.chunks, chunk{c.Text, op})
			}
		}
		p = append(p, fp)
	}
	return fdiff.NewUnifiedEncoder(w, contextLines).Encode(p)
}

// isBinary uses git's rule: a NUL byte in the first 8000 bytes.
func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

type patch []fdiff.FilePatch

func (p patch) FilePatches() []fdiff.FilePatch { return p }
func (p patch) Message() string                { return "" }

type filePatch struct {
	from, to fdiff.File
	binary   bool
	chunks   []fdiff.Chunk
}

func (f filePatch) IsBinary() bool               { return f.binary }
func (f filePatch) Files() (from, to fdiff.File) { return f.from, f.to }
func (f filePatch) Chunks() []fdiff.Chunk        { return f.chunks }

type file struct{ e *FileEntry }

func (f file) Hash() plumbing.Hash     { return f.e.Hash }
func (f file) Mode() filemode.FileMode { return f.e.Mode }
func (f file) Path() string            { return f.e.Path }

// asFile returns nil (not a nil-holding interface) for an absent side.
func asFile(e *FileEntry) fdiff.File {
	if e == nil {
		return nil
	}
	return file{e}
}

type chunk struct {
	content string
	op      fdiff.Operation
}

func (c chunk) Content() string       { return c.content }
func (c chunk) Type() fdiff.Operation { return c.op }
