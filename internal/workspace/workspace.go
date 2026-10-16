// Package workspace is a light working directory: the files for chosen paths
// at one commit, plus .gion/state.json. There is no .git and no history; the
// remote branch is the only source of truth.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/gitignore"

	"github.com/baz-scm/gion/internal/repo"
)

const stateDir = ".gion"

// FileState is the index entry for one materialized file.
type FileState struct {
	OID   string `json:"oid"`
	Mode  uint32 `json:"mode"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // UnixNano, for the stat cache
}

// State is persisted in .gion/state.json.
type State struct {
	Remote string `json:"remote"`
	// Ref is what was cloned (e.g. "main"); Base is the commit on disk.
	Ref  string `json:"ref"`
	Base string `json:"base"`
	// Branch is where commits go. Empty until the first commit creates one.
	Branch string `json:"branch,omitempty"`
	// Paths limits the workspace; empty means the whole repo.
	Paths []string `json:"paths,omitempty"`
	// Dirs are the parent directories of Paths ("" is the root). Their own
	// files (not subdirectories) are fetched too, since that's where config
	// lives: .gitignore, pyproject.toml, go.mod, package.json, conftest.py.
	// Edits to those files are committed; new files there are not, unless
	// added with `gion add`.
	Dirs  []string             `json:"dirs,omitempty"`
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
			return nil, errors.New("not in a gion workspace (run `gion clone` first)")
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

	var dirs []string
	if len(clean) > 0 {
		dirs = ancestorDirs(clean)
		more, err := contextFiles(ctx, r, root, dirs, func(p string) bool { return covered(clean, p) })
		if err != nil {
			return nil, CloneResult{}, err
		}
		files = append(files, more...)
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
		Remote: r.Remote, Ref: ref, Base: commit.String(), Paths: clean, Dirs: dirs,
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
	return len(w.State.Paths) == 0 || covered(w.State.Paths, p)
}

// covered reports whether p is one of paths or under one.
func covered(paths []string, p string) bool {
	for _, s := range paths {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// inContext reports whether p is a file directly inside one of Dirs.
func (w *Workspace) inContext(p string) bool {
	d := path.Dir(p)
	if d == "." {
		d = ""
	}
	for _, x := range w.State.Dirs {
		if x == d {
			return true
		}
	}
	return false
}

// ancestorDirs returns every parent directory of paths, root ("") included,
// sorted and without duplicates.
func ancestorDirs(paths []string) []string {
	seen := map[string]bool{"": true}
	for _, p := range paths {
		for d := path.Dir(p); d != "." && d != "/"; d = path.Dir(d) {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// contextFiles lists the files (not subdirectories or submodules) directly
// inside each of dirs in tree root, except those skip accepts.
func contextFiles(ctx context.Context, r *repo.Repo, root plumbing.Hash, dirs []string, skip func(string) bool) ([]repo.FileEntry, error) {
	var out []repo.FileEntry
	for _, d := range dirs {
		e, err := r.Lookup(ctx, root, d)
		if errors.Is(err, repo.ErrNotExist) {
			continue // parent of a path that doesn't exist yet
		}
		if err != nil {
			return nil, err
		}
		if e.Mode != filemode.Dir {
			continue
		}
		t, err := r.Tree(ctx, e.Hash)
		if err != nil {
			return nil, err
		}
		for _, te := range t.Entries {
			if te.Mode == filemode.Dir || te.Mode == filemode.Submodule {
				continue
			}
			p := path.Join(d, te.Name)
			if skip == nil || !skip(p) {
				out = append(out, repo.FileEntry{Path: p, Mode: te.Mode, Hash: te.Hash})
			}
		}
	}
	return out, nil
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

// relevantDir reports whether dir may contain in-scope files.
func (w *Workspace) relevantDir(dir string) bool {
	if dir == "" || w.inScope(dir) {
		return true
	}
	for _, s := range w.State.Paths {
		if strings.HasPrefix(s, dir+"/") {
			return true
		}
	}
	return false
}

// ignores builds the root .gitignore scope: global core.excludesFile
// patterns, then the workspace's root .gitignore.
func (w *Workspace) ignores() (*gitignore.Scope, billy.Filesystem, error) {
	fsys := osfs.New(w.Dir)
	var base []gitignore.Pattern
	if g, err := gitignore.LoadGlobalPatterns(osfs.New("/")); err == nil {
		base = append(base, g...)
	}
	root, err := gitignore.DirPatterns(fsys, nil)
	if err != nil {
		return nil, nil, err
	}
	return gitignore.NewScope(append(base, root...)), fsys, nil
}

// Status compares disk to the index. Files outside the chosen paths are
// skipped, so a sparse workspace never reports them as deleted. Untracked
// files matched by .gitignore are skipped too; tracked files are always
// checked, as in git.
func (w *Workspace) Status() ([]FileChange, error) {
	scope, fsys, err := w.ignores()
	if err != nil {
		return nil, err
	}
	var out []FileChange
	seen := map[string]bool{}

	check := func(rel string) error {
		seen[rel] = true
		abs := w.abs(rel)
		fi, err := os.Lstat(abs)
		if errors.Is(err, fs.ErrNotExist) {
			if _, tracked := w.State.Files[rel]; tracked {
				out = append(out, FileChange{Path: rel, Kind: Deleted})
			}
			return nil
		}
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
	}

	var walk func(dir []string, sc *gitignore.Scope) error
	walk = func(dir []string, sc *gitignore.Scope) error {
		entries, err := os.ReadDir(filepath.Join(append([]string{w.Dir}, dir...)...))
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if name == ".git" || (len(dir) == 0 && name == stateDir) {
				continue
			}
			p := append(append([]string{}, dir...), name)
			rel := strings.Join(p, "/")
			if e.IsDir() {
				if !w.relevantDir(rel) {
					continue
				}
				child, err := sc.Descend(p, func() ([]gitignore.Pattern, error) { return gitignore.DirPatterns(fsys, p) })
				if err != nil {
					return err
				}
				if child.Excluded() {
					continue // tracked files inside are checked below
				}
				if err := walk(p, child); err != nil {
					return err
				}
				continue
			}
			_, tracked := w.State.Files[rel]
			if !w.inScope(rel) && !(tracked && w.inContext(rel)) {
				continue
			}
			if !tracked && sc.Match(p, false) {
				continue
			}
			if err := check(rel); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(nil, scope); err != nil {
		return nil, err
	}
	// Tracked files the walk didn't reach: deleted, or inside an ignored dir.
	for p := range w.State.Files {
		if !seen[p] {
			if err := check(p); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// CommitResult extends the repo result with workspace notes.
type CommitResult struct {
	repo.CommitResult
	Changes []FileChange
	// Pulled lists files updated on disk because the branch had upstream
	// commits on other paths when we pushed.
	Pulled SyncResult
}

// Commit pushes every change in the workspace. The first commit creates
// branch (gion/<timestamp> if empty); later ones update it with CAS. If the
// branch had moved on other paths, those upstream changes are written to disk
// too, so the workspace matches the new commit.
//
// to pushes onto an existing branch instead (compare-and-swap, accepting
// upstream moves only on other paths), and makes it the workspace's branch.
func (w *Workspace) Commit(ctx context.Context, r *repo.Repo, msg, branch, to string) (CommitResult, error) {
	if branch != "" && to != "" {
		return CommitResult{}, errors.New("--branch creates a new branch and --to updates an existing one; pick one")
	}
	changes, err := w.Status()
	if err != nil {
		return CommitResult{}, err
	}
	if len(changes) == 0 {
		return CommitResult{}, errors.New("nothing to commit")
	}
	who, err := repo.Identity()
	if err != nil {
		return CommitResult{}, err
	}
	rc := make([]repo.Change, len(changes))
	for i, c := range changes {
		rc[i] = repo.Change{Path: c.Path, Data: c.Data, Delete: c.Kind == Deleted, Mode: c.Mode}
	}
	base, _ := plumbing.FromHex(w.State.Base)
	opts := repo.CommitOptions{Changes: rc, Message: msg, Who: who}
	switch {
	case to != "":
		opts.To, opts.Base = strings.TrimPrefix(to, "refs/heads/"), base
	case w.State.Branch != "" && branch != "" && branch != w.State.Branch:
		return CommitResult{}, fmt.Errorf("workspace already pushes to %s; use --to to push elsewhere", w.State.Branch)
	case w.State.Branch != "":
		opts.To, opts.Base = w.State.Branch, base
	default:
		if branch == "" {
			branch = "gion/" + time.Now().UTC().Format("20060102-150405")
		}
		opts.Branch, opts.Onto = branch, w.State.Base
	}

	res, err := r.CommitAndPush(ctx, opts)
	if err != nil {
		return CommitResult{}, err
	}
	out := CommitResult{CommitResult: res, Changes: changes}
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
	if res.Parent != base {
		// Upstream moved on paths we didn't touch (CommitAndPush checked).
		// Bring them onto disk; our own files are already recorded.
		pulled, err := w.applyUpstream(ctx, r, base, res.Parent, nil)
		out.Pulled = pulled
		if err != nil {
			return out, fmt.Errorf("pushed %s, but updating files from upstream failed (run `gion sync`): %w", res.Commit.String()[:12], err)
		}
	}
	w.State.Base = res.Commit.String()
	return out, w.save()
}

// ErrLocalConflict means upstream changed files that also have local edits.
var ErrLocalConflict = errors.New("local changes conflict with upstream")

// SyncResult describes what sync changed on disk.
type SyncResult struct {
	From, To plumbing.Hash
	Updated  []string // added or modified upstream
	Removed  []string // deleted upstream
}

// Sync brings the workspace up to date with its branch (or the cloned ref
// before the first commit). Local edits are kept; if upstream changed the
// same files differently, nothing is written and ErrLocalConflict lists them.
func (w *Workspace) Sync(ctx context.Context, r *repo.Repo) (SyncResult, error) {
	target := w.State.Ref
	if w.State.Branch != "" {
		target = "refs/heads/" + w.State.Branch
	}
	tip, err := r.Resolve(ctx, target)
	if err != nil {
		return SyncResult{}, err
	}
	base, _ := plumbing.FromHex(w.State.Base)
	if tip == base {
		return SyncResult{From: base, To: tip}, nil
	}
	local, err := w.Status()
	if err != nil {
		return SyncResult{}, err
	}
	res, err := w.applyUpstream(ctx, r, base, tip, local)
	if err != nil {
		return res, err
	}
	w.State.Base = tip.String()
	return res, w.save()
}

// applyUpstream writes the in-scope differences between commits from and to
// onto disk. Files in local (pending edits) are left alone; if upstream also
// changed one of them to something else, nothing is written at all.
func (w *Workspace) applyUpstream(ctx context.Context, r *repo.Repo, from, to plumbing.Hash, local []FileChange) (SyncResult, error) {
	res := SyncResult{From: from, To: to}
	fc, err := r.Commit(ctx, from)
	if err != nil {
		return res, err
	}
	tc, err := r.Commit(ctx, to)
	if err != nil {
		return res, err
	}
	edits := map[string]FileChange{}
	for _, c := range local {
		edits[c.Path] = c
	}

	type update struct {
		path  string
		entry *repo.FileEntry
	}
	var updates []update
	var conflicts []string
	err = r.DiffTrees(ctx, fc.TreeHash, tc.TreeHash, w.relevantDir, func(p string, _, n *repo.FileEntry) error {
		if !w.inScope(p) && !w.inContext(p) {
			return nil
		}
		if e, edited := edits[p]; edited {
			same := (n == nil && e.Kind == Deleted) || (n != nil && e.Kind != Deleted && e.OID == n.Hash && e.Mode == n.Mode)
			if !same {
				conflicts = append(conflicts, p)
			}
			return nil // keep the local version (identical, or a conflict)
		}
		updates = append(updates, update{p, n})
		return nil
	})
	if err != nil {
		return res, err
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return res, fmt.Errorf("%w: %s", ErrLocalConflict, strings.Join(conflicts, ", "))
	}

	var blobs []plumbing.Hash
	for _, u := range updates {
		if u.entry != nil {
			blobs = append(blobs, u.entry.Hash)
		}
	}
	if err := r.Ensure(ctx, blobs...); err != nil {
		return res, err
	}
	for _, u := range updates {
		if u.entry == nil {
			if err := os.Remove(w.abs(u.path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, err
			}
			w.pruneEmptyDirs(u.path)
			delete(w.State.Files, u.path)
			res.Removed = append(res.Removed, u.path)
			continue
		}
		data, err := r.Blob(ctx, u.entry.Hash)
		if err != nil {
			return res, err
		}
		if err := w.writeFile(u.path, u.entry.Mode, data); err != nil {
			return res, err
		}
		if err := w.record(u.path, u.entry.Hash, u.entry.Mode); err != nil {
			return res, err
		}
		res.Updated = append(res.Updated, u.path)
	}
	sort.Strings(res.Updated)
	sort.Strings(res.Removed)
	return res, nil
}

// pruneEmptyDirs removes now-empty parent directories of p, up to the root.
func (w *Workspace) pruneEmptyDirs(p string) {
	for d := filepath.Dir(w.abs(p)); d != w.Dir && strings.HasPrefix(d, w.Dir); d = filepath.Dir(d) {
		if os.Remove(d) != nil { // fails unless empty
			return
		}
	}
}

// Switch moves the workspace to an existing branch: files that differ
// between the current base and the branch tip are rewritten, local edits are
// kept (nothing changes if they conflict), and later commits go to branch.
func (w *Workspace) Switch(ctx context.Context, r *repo.Repo, branch string) (SyncResult, error) {
	branch = strings.TrimPrefix(branch, "refs/heads/")
	tip, err := r.Resolve(ctx, "refs/heads/"+branch)
	if errors.Is(err, repo.ErrNotExist) {
		return SyncResult{}, fmt.Errorf("branch %s doesn't exist; to create it, commit with --branch %s", branch, branch)
	}
	if err != nil {
		return SyncResult{}, err
	}
	base, _ := plumbing.FromHex(w.State.Base)
	res := SyncResult{From: base, To: tip}
	if tip != base {
		local, err := w.Status()
		if err != nil {
			return res, err
		}
		if res, err = w.applyUpstream(ctx, r, base, tip, local); err != nil {
			return res, err
		}
	}
	w.State.Ref, w.State.Branch, w.State.Base = branch, branch, tip.String()
	return res, w.save()
}

// Diff returns the workspace's pending changes as file diffs against its
// base, plus the on-disk contents of new and modified files keyed by blob id
// (for repo.WritePatch).
func (w *Workspace) Diff() ([]repo.FileDiff, map[plumbing.Hash][]byte, error) {
	changes, err := w.Status()
	if err != nil {
		return nil, nil, err
	}
	content := map[plumbing.Hash][]byte{}
	out := make([]repo.FileDiff, 0, len(changes))
	for _, c := range changes {
		d := repo.FileDiff{Path: c.Path}
		if prev, ok := w.State.Files[c.Path]; ok {
			h, _ := plumbing.FromHex(prev.OID)
			d.Old = &repo.FileEntry{Path: c.Path, Mode: filemode.FileMode(prev.Mode), Hash: h}
		}
		if c.Kind != Deleted {
			d.New = &repo.FileEntry{Path: c.Path, Mode: c.Mode, Hash: c.OID}
			content[c.OID] = c.Data
		}
		out = append(out, d)
	}
	return out, content, nil
}

// AddResult describes what Add brought into the workspace.
type AddResult struct {
	Paths []string // scope entries added
	Files int
	Bytes int64
	New   []string // paths not in the base commit yet: scope only, for new files
}

// Add widens a sparse workspace: each path (file or directory) is fetched at
// the workspace's base commit and written to disk, and joins the scope so
// status, sync and commit see it. A path that doesn't exist yet only joins
// the scope, so new files there can be committed. Files already on disk
// that differ from the base are left alone and reported; nothing is written
// then.
func (w *Workspace) Add(ctx context.Context, r *repo.Repo, paths []string) (AddResult, error) {
	var res AddResult
	if len(w.State.Paths) == 0 {
		return res, errors.New("this workspace already holds the whole repo")
	}
	base, ok := plumbing.FromHex(w.State.Base)
	if !ok {
		return res, fmt.Errorf("bad base %q in state", w.State.Base)
	}
	c, err := r.Commit(ctx, base)
	if err != nil {
		return res, err
	}

	var trees []plumbing.Hash
	type scope struct {
		prefix string
		tree   plumbing.Hash
		file   *repo.FileEntry
	}
	var scopes []scope
	for _, p := range paths {
		p = strings.Trim(filepath.ToSlash(filepath.Clean(p)), "/")
		if p == "" || p == "." {
			return res, errors.New("to take the whole repo, clone it again without --path")
		}
		if strings.HasPrefix(p, "../") || p == ".." || p == stateDir || strings.HasPrefix(p, stateDir+"/") {
			return res, fmt.Errorf("%s is outside the workspace", p)
		}
		if w.inScope(p) {
			continue // already covered
		}
		e, err := r.Lookup(ctx, c.TreeHash, p)
		switch {
		case errors.Is(err, repo.ErrNotExist):
			res.New = append(res.New, p)
		case err != nil:
			return res, err
		case e.Mode == filemode.Dir:
			scopes = append(scopes, scope{prefix: p, tree: e.Hash})
			trees = append(trees, e.Hash)
		default:
			scopes = append(scopes, scope{file: &repo.FileEntry{Path: p, Mode: e.Mode, Hash: e.Hash}})
		}
		res.Paths = append(res.Paths, p)
	}
	if err := r.EnsureTrees(ctx, trees...); err != nil {
		return res, err
	}
	var files []repo.FileEntry
	for _, s := range scopes {
		if s.file != nil {
			files = append(files, *s.file)
			continue
		}
		if err := r.Walk(ctx, s.tree, s.prefix, func(f repo.FileEntry) error {
			files = append(files, f)
			return nil
		}); err != nil {
			return res, err
		}
	}

	// Parent directories new to the workspace bring their own files too.
	var newDirs []string
	if len(res.Paths) > 0 {
		have := map[string]bool{}
		for _, d := range w.State.Dirs {
			have[d] = true
		}
		for _, d := range ancestorDirs(res.Paths) {
			if !have[d] {
				newDirs = append(newDirs, d)
			}
		}
		all := append(append([]string{}, w.State.Paths...), res.Paths...)
		more, err := contextFiles(ctx, r, c.TreeHash, newDirs, func(p string) bool { return covered(all, p) })
		if err != nil {
			return res, err
		}
		files = append(files, more...)
	}

	// Out-of-scope files on disk were invisible to status; refuse to clobber
	// one that differs from the base.
	var clash []string
	var write []repo.FileEntry
	for _, f := range files {
		if _, tracked := w.State.Files[f.Path]; tracked {
			continue // already in scope via a narrower path; may hold edits
		}
		fi, err := os.Lstat(w.abs(f.Path))
		if errors.Is(err, fs.ErrNotExist) {
			write = append(write, f)
			continue
		}
		if err != nil {
			return res, err
		}
		data, mode, err := readDisk(w.abs(f.Path), fi)
		if err != nil {
			return res, err
		}
		if blobHash(data) != f.Hash || mode != f.Mode {
			clash = append(clash, f.Path)
			continue
		}
		write = append(write, f) // identical: just start tracking it
	}
	if len(clash) > 0 {
		sort.Strings(clash)
		return res, fmt.Errorf("%w: %s differ from %s; move them aside first", ErrLocalConflict, strings.Join(clash, ", "), w.State.Base[:12])
	}

	blobs := make([]plumbing.Hash, len(write))
	for i, f := range write {
		blobs[i] = f.Hash
	}
	if err := r.Ensure(ctx, blobs...); err != nil {
		return res, err
	}
	for _, f := range write {
		data, err := r.Blob(ctx, f.Hash)
		if err != nil {
			return res, err
		}
		if err := w.writeFile(f.Path, f.Mode, data); err != nil {
			return res, err
		}
		if err := w.record(f.Path, f.Hash, f.Mode); err != nil {
			return res, err
		}
		res.Files++
		res.Bytes += int64(len(data))
	}

	// New entries may cover old ones (adding "d" after "d/f"): keep the
	// widest.
	all := append(append([]string{}, w.State.Paths...), res.Paths...)
	sort.Strings(all)
	var merged []string
	for _, p := range all {
		if n := len(merged); n > 0 && (p == merged[n-1] || strings.HasPrefix(p, merged[n-1]+"/")) {
			continue
		}
		merged = append(merged, p)
	}
	w.State.Paths = merged
	if len(newDirs) > 0 {
		w.State.Dirs = append(w.State.Dirs, newDirs...)
		sort.Strings(w.State.Dirs)
	}
	return res, w.save()
}

// Rel turns a path given on the command line (relative to the current
// directory) into a workspace path, reporting false if it is outside the
// workspace or is neither on disk nor tracked.
func (w *Workspace) Rel(arg string) (string, bool) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(w.Dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "", true
	}
	if _, err := os.Lstat(abs); err == nil {
		return rel, true
	}
	for p := range w.State.Files {
		if p == rel || strings.HasPrefix(p, rel+"/") {
			return rel, true // deleted on disk, still tracked
		}
	}
	return "", false
}
