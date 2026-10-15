// Package workspace is a light working directory: the files for chosen paths
// at one commit, plus .any/state.json. There is no .git and no history; the
// remote branch is the only source of truth.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	format "github.com/go-git/go-git/v6/plumbing/format/config"

	"github.com/AnyGitForge/AnyForge/cli/internal/repo"
)

const stateDir = ".any"

// FileState is the index entry for one materialized file.
type FileState struct {
	OID   string `json:"oid"`
	Mode  uint32 `json:"mode"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // UnixNano, for the stat cache
}

// State is persisted in .any/state.json.
type State struct {
	Remote string `json:"remote"`
	// Ref is what was cloned (e.g. "main"); Base is the commit on disk.
	Ref  string `json:"ref"`
	Base string `json:"base"`
	// Branch is where commits go. Empty until the first commit creates one.
	Branch string `json:"branch,omitempty"`
	// Paths limits the workspace; empty means the whole repo.
	Paths []string             `json:"paths,omitempty"`
	Files map[string]FileState `json:"files"`
}

// Workspace is an opened workspace directory.
type Workspace struct {
	Dir   string
	State State
}

// Find walks up from dir to the nearest workspace.
func Find(dir string) (*Workspace, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for d := dir; ; d = filepath.Dir(d) {
		b, err := os.ReadFile(filepath.Join(d, stateDir, "state.json"))
		if err == nil {
			w := &Workspace{Dir: d}
			if err := json.Unmarshal(b, &w.State); err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Join(d, stateDir, "state.json"), err)
			}
			return w, nil
		}
		if filepath.Dir(d) == d {
			return nil, errors.New("not in an any workspace (run `any clone` first)")
		}
	}
}

func (w *Workspace) save() error {
	b, err := json.MarshalIndent(w.State, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(w.Dir, stateDir, "state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(w.Dir, stateDir, "state.json"))
}

// CloneResult summarizes a clone.
type CloneResult struct {
	Commit plumbing.Hash
	Files  int
	Bytes  int64
}

// Clone materializes paths (all if empty) of ref into dir.
func Clone(ctx context.Context, r *repo.Repo, dir, ref string, paths []string) (*Workspace, CloneResult, error) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return nil, CloneResult{}, fmt.Errorf("%s exists and is not empty", dir)
	}
	commit, root, err := r.RootTree(ctx, ref)
	if err != nil {
		return nil, CloneResult{}, err
	}

	// Resolve each scope path to a tree (walk it) or a single file.
	type scope struct {
		prefix string
		tree   plumbing.Hash
		file   *repo.FileEntry
	}
	var scopes []scope
	var clean []string
	if len(paths) == 0 {
		scopes = append(scopes, scope{tree: root})
	}
	for _, p := range paths {
		p = strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")
		e, err := r.Lookup(ctx, root, p)
		if err != nil {
			return nil, CloneResult{}, err
		}
		clean = append(clean, p)
		if e.Mode == filemode.Dir {
			scopes = append(scopes, scope{prefix: p, tree: e.Hash})
		} else {
			scopes = append(scopes, scope{file: &repo.FileEntry{Path: p, Mode: e.Mode, Hash: e.Hash}})
		}
	}

	var trees []plumbing.Hash
	for _, s := range scopes {
		if s.file == nil {
			trees = append(trees, s.tree)
		}
	}
	if err := r.EnsureTrees(ctx, trees...); err != nil {
		return nil, CloneResult{}, err
	}
	var files []repo.FileEntry
	for _, s := range scopes {
		if s.file != nil {
			files = append(files, *s.file)
			continue
		}
		err := r.Walk(ctx, s.tree, s.prefix, func(f repo.FileEntry) error {
			files = append(files, f)
			return nil
		})
		if err != nil {
			return nil, CloneResult{}, err
		}
	}

	blobs := make([]plumbing.Hash, len(files))
	for i, f := range files {
		blobs[i] = f.Hash
	}
	if err := r.Ensure(ctx, blobs...); err != nil {
		return nil, CloneResult{}, err
	}

	if err := os.MkdirAll(filepath.Join(dir, stateDir), 0o755); err != nil {
		return nil, CloneResult{}, err
	}
	w := &Workspace{Dir: dir, State: State{
		Remote: r.Remote, Ref: ref, Base: commit.String(), Paths: clean,
		Files: make(map[string]FileState, len(files)),
	}}
	res := CloneResult{Commit: commit}
	for _, f := range files {
		data, err := r.Blob(ctx, f.Hash)
		if err != nil {
			return nil, CloneResult{}, err
		}
		if err := w.writeFile(f.Path, f.Mode, data); err != nil {
			return nil, CloneResult{}, err
		}
		if err := w.record(f.Path, f.Hash, f.Mode); err != nil {
			return nil, CloneResult{}, err
		}
		res.Files++
		res.Bytes += int64(len(data))
	}
	return w, res, w.save()
}

func (w *Workspace) abs(p string) string { return filepath.Join(w.Dir, filepath.FromSlash(p)) }

func (w *Workspace) writeFile(p string, mode filemode.FileMode, data []byte) error {
	dst := w.abs(p)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if mode == filemode.Symlink {
		return os.Symlink(string(data), dst)
	}
	perm := os.FileMode(0o644)
	if mode == filemode.Executable {
		perm = 0o755
	}
	return os.WriteFile(dst, data, perm)
}

// record stores the on-disk stat of p so status can skip unchanged files.
func (w *Workspace) record(p string, oid plumbing.Hash, mode filemode.FileMode) error {
	fi, err := os.Lstat(w.abs(p))
	if err != nil {
		return err
	}
	w.State.Files[p] = FileState{OID: oid.String(), Mode: uint32(mode), Size: fi.Size(), MTime: fi.ModTime().UnixNano()}
	return nil
}

// inScope reports whether p is inside the workspace's chosen paths.
func (w *Workspace) inScope(p string) bool {
	if len(w.State.Paths) == 0 {
		return true
	}
	for _, s := range w.State.Paths {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// Kind of change.
type Kind string

const (
	Added    Kind = "added"
	Modified Kind = "modified"
	Deleted  Kind = "deleted"
)

// FileChange is one entry of Status.
type FileChange struct {
	Path string
	Kind Kind
	Mode filemode.FileMode
	Data []byte // nil for Deleted
	OID  plumbing.Hash
}

func blobHash(data []byte) plumbing.Hash {
	h := plumbing.NewHasher(format.SHA1, plumbing.BlobObject, int64(len(data)))
	h.Write(data)
	return h.Sum()
}

// statMode maps a file's stat to its git mode. chmod changes neither size
// nor mtime, so the stat cache must compare this too.
func statMode(fi fs.FileInfo) filemode.FileMode {
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return filemode.Symlink
	case fi.Mode().Perm()&0o111 != 0:
		return filemode.Executable
	}
	return filemode.Regular
}

// readDisk returns content and git mode for a file on disk.
func readDisk(abs string, fi fs.FileInfo) ([]byte, filemode.FileMode, error) {
	mode := statMode(fi)
	if mode == filemode.Symlink {
		t, err := os.Readlink(abs)
		return []byte(t), mode, err
	}
	data, err := os.ReadFile(abs)
	return data, mode, err
}

// Status compares disk to the index. Files outside the chosen paths are
// ignored, so a sparse workspace never reports them as deleted.
func (w *Workspace) Status() ([]FileChange, error) {
	var out []FileChange
	seen := map[string]bool{}
	err := filepath.WalkDir(w.Dir, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(w.Dir, abs)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == stateDir || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !w.inScope(rel) {
			return nil
		}
		seen[rel] = true
		fi, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		prev, tracked := w.State.Files[rel]
		if tracked && prev.Size == fi.Size() && prev.MTime == fi.ModTime().UnixNano() && prev.Mode == uint32(statMode(fi)) {
			return nil // stat cache hit
		}
		data, mode, err := readDisk(abs, fi)
		if err != nil {
			return err
		}
		oid := blobHash(data)
		switch {
		case !tracked:
			out = append(out, FileChange{Path: rel, Kind: Added, Mode: mode, Data: data, OID: oid})
		case oid.String() != prev.OID || uint32(mode) != prev.Mode:
			out = append(out, FileChange{Path: rel, Kind: Modified, Mode: mode, Data: data, OID: oid})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for p := range w.State.Files {
		if !seen[p] {
			out = append(out, FileChange{Path: p, Kind: Deleted})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// CommitResult extends the repo result with workspace notes.
type CommitResult struct {
	repo.CommitResult
	Changes []FileChange
	// UpstreamMoved is set when the branch had commits we didn't have; they
	// touched other paths (or we'd have failed), but disk doesn't show them.
	UpstreamMoved bool
}

// Commit pushes every change in the workspace. The first commit creates
// branch (any/<timestamp> if empty); later ones update it with CAS.
func (w *Workspace) Commit(ctx context.Context, r *repo.Repo, msg, branch string) (CommitResult, error) {
	changes, err := w.Status()
	if err != nil {
		return CommitResult{}, err
	}
	if len(changes) == 0 {
		return CommitResult{}, errors.New("nothing to commit")
	}
	author, err := repo.Identity()
	if err != nil {
		return CommitResult{}, err
	}
	rc := make([]repo.Change, len(changes))
	for i, c := range changes {
		rc[i] = repo.Change{Path: c.Path, Data: c.Data, Delete: c.Kind == Deleted, Mode: c.Mode}
	}
	base, _ := plumbing.FromHex(w.State.Base)
	opts := repo.CommitOptions{Changes: rc, Message: msg, Author: author}
	switch {
	case w.State.Branch != "" && branch != "" && branch != w.State.Branch:
		return CommitResult{}, fmt.Errorf("workspace already pushes to %s", w.State.Branch)
	case w.State.Branch != "":
		opts.To, opts.Base = w.State.Branch, base
	default:
		if branch == "" {
			branch = "any/" + time.Now().UTC().Format("20060102-150405")
		}
		opts.Branch, opts.Onto = branch, w.State.Base
	}

	res, err := r.CommitAndPush(ctx, opts)
	if err != nil {
		return CommitResult{}, err
	}
	out := CommitResult{CommitResult: res, Changes: changes, UpstreamMoved: res.Parent != base}
	w.State.Base = res.Commit.String()
	w.State.Branch = res.Ref.Short()
	for _, c := range changes {
		if c.Kind == Deleted {
			delete(w.State.Files, c.Path)
			continue
		}
		if err := w.record(c.Path, c.OID, c.Mode); err != nil {
			return out, err
		}
	}
	return out, w.save()
}
