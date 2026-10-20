package workspace

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/format/objfile"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/baz-scm/gion/internal/cache"
)

// A workspace (unless cloned with --no-git) has a .git that git-based tools
// (pre-commit, `git diff`, `git ls-files`) can read. It is a mirror of the
// workspace state, rewritten on every save. When the tracked files are the
// whole of the cloned commit's tree, HEAD is that commit, as in a depth-1
// clone (.git/shallow). Otherwise HEAD is a snapshot commit, with no parent,
// whose tree holds exactly the tracked files: files outside a sparse
// workspace's paths are not in that tree, so git sees a small, complete repo
// rather than a huge sparse one. Either way the index matches HEAD, and
// .git/GION_HEAD names the upstream commit (`git rev-parse GION_HEAD`); it is
// a pseudoref, not a ref under refs/, as the object may be absent and
// `git log --all` would fail on it. Only gion pushes; a local `git commit`
// goes nowhere.

const gitDir = ".git"

// gitConfig is written once; tools may add to it later (core.hooksPath).
// checkStat=minimal and trustctime=false because the index only has the size
// and mtime gion recorded, not inode or ctime.
const gitConfig = `[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
	logallrefupdates = false
	trustctime = false
	checkStat = minimal
`

// snapshotAuthor signs the HEAD commit, which is never pushed.
var snapshotAuthor = object.Signature{Name: "gion", Email: "gion@localhost"}

func (w *Workspace) gitPath(parts ...string) string {
	return filepath.Join(append([]string{w.Dir, gitDir}, parts...)...)
}

// writeGit brings .git in line with the workspace state.
func (w *Workspace) writeGit() error {
	for _, d := range []string{"objects", filepath.Join("refs", "heads"), filepath.Join("refs", "tags"), "info"} {
		if err := os.MkdirAll(w.gitPath(d), 0o755); err != nil {
			return err
		}
	}
	if _, err := os.Stat(w.gitPath("config")); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(w.gitPath("config"), []byte(gitConfig), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(w.gitPath("info", "exclude"), []byte("/"+stateDir+"/\n"), 0o644); err != nil {
			return err
		}
	}

	paths := make([]string, 0, len(w.State.Files))
	for p := range w.State.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	objs, err := w.newGitObjects()
	if err != nil {
		return err
	}
	src, _ := cache.Dir()
	for _, p := range paths {
		w.queueBlob(objs, p, src)
	}
	tree, err := w.gitTree(objs, "", paths)
	if err != nil {
		return err
	}
	if err := objs.flush(); err != nil {
		return err
	}
	head, upstream, err := w.gitUpstream(tree, src)
	if err != nil {
		return err
	}
	if !upstream {
		if head, err = w.gitCommit(tree); err != nil {
			return err
		}
	}
	if err := w.gitShallow(upstream); err != nil {
		return err
	}
	if err := w.gitHead(head); err != nil {
		return err
	}
	if err := writeAtomic(w.gitPath("GION_HEAD"), []byte(w.State.Base+"\n")); err != nil {
		return err
	}
	return w.gitIndex(paths, nil)
}

// gitUpstream copies the cloned commit into .git and returns it when its tree
// is tree, so HEAD can be the real commit. ok is false when the workspace
// holds less than that tree, or the commit isn't to be had from .git or the
// cache.
func (w *Workspace) gitUpstream(tree plumbing.Hash, cacheDir string) (h plumbing.Hash, ok bool, err error) {
	base, isHash := plumbing.FromHex(w.State.Base)
	if !isHash {
		return plumbing.ZeroHash, false, nil
	}
	o, gerr := w.gitGet(base)
	if gerr != nil {
		if cacheDir == "" {
			return plumbing.ZeroHash, false, nil
		}
		s, err := cache.Open(cacheDir)
		if err != nil {
			return plumbing.ZeroHash, false, nil
		}
		if o, err = s.Get(base); err != nil {
			return plumbing.ZeroHash, false, nil
		}
	}
	var c object.Commit
	if o.Type() != plumbing.CommitObject || c.Decode(o) != nil || c.TreeHash != tree {
		return plumbing.ZeroHash, false, nil
	}
	if gerr != nil {
		if err := w.gitPut(o); err != nil {
			return plumbing.ZeroHash, false, err
		}
	}
	return base, true, nil
}

// gitShallow marks the cloned commit as having no parents in .git when HEAD
// is that commit, and removes the mark otherwise.
func (w *Workspace) gitShallow(upstream bool) error {
	p := w.gitPath("shallow")
	if !upstream {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(p, []byte(w.State.Base+"\n"))
}

// ErrHookFailed means a commit hook rejected the commit.
var ErrHookFailed = errors.New("hook failed")

// RunCommitHooks runs the pre-commit and commit-msg hooks of a workspace
// with a .git the way `git commit -a` would: every change is staged first, so
// hook frameworks that check staged files (pre-commit, lint-staged) see them.
// It returns the message, as commit-msg may have rewritten it. The index goes
// back to matching HEAD afterwards. Hook output goes to out.
func (w *Workspace) RunCommitHooks(msg string, out io.Writer) (string, error) {
	if !w.State.Git {
		return msg, nil
	}
	dir, err := w.hooksDir()
	if err != nil {
		return msg, err
	}
	pre, msgHook := hook(dir, "pre-commit"), hook(dir, "commit-msg")
	if pre == "" && msgHook == "" {
		return msg, nil
	}
	changes, err := w.Status()
	if err != nil || len(changes) == 0 {
		return msg, err // Commit reports "nothing to commit"
	}
	paths := make([]string, 0, len(w.State.Files))
	for p := range w.State.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if err := w.gitIndex(paths, changes); err != nil {
		return msg, err
	}
	defer w.gitIndex(paths, nil)

	if pre != "" {
		if err := w.runHook(pre, out); err != nil {
			return msg, err
		}
	}
	if msgHook != "" {
		f := w.gitPath("COMMIT_EDITMSG")
		if err := os.WriteFile(f, []byte(msg), 0o644); err != nil {
			return msg, err
		}
		if err := w.runHook(msgHook, out, f); err != nil {
			return msg, err
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return msg, err
		}
		msg = string(b)
	}
	return msg, nil
}

// hooksDir is core.hooksPath (relative to the workspace root, as in git),
// else .git/hooks.
func (w *Workspace) hooksDir() (string, error) {
	f, err := os.Open(w.gitPath("config"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	cfg := format.New()
	if err := format.NewDecoder(f).Decode(cfg); err != nil {
		return "", fmt.Errorf(".git/config: %w", err)
	}
	p := cfg.Section("core").Option("hooksPath")
	switch {
	case p == "":
		return w.gitPath("hooks"), nil
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	case filepath.IsAbs(p):
		return p, nil
	}
	return filepath.Join(w.Dir, p), nil
}

// hook returns the path of an executable hook, or "" (git skips hooks that
// aren't executable too).
func hook(dir, name string) string {
	p := filepath.Join(dir, name)
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
		return p
	}
	return ""
}

func (w *Workspace) runHook(p string, out io.Writer, args ...string) error {
	cmd := exec.Command(p, args...)
	cmd.Dir = w.Dir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrHookFailed, filepath.Base(p), err)
	}
	return nil
}

// gitTree queues the tree for dir ("" is the root) from the sorted tracked
// paths under it, and returns its id.
func (w *Workspace) gitTree(objs *gitObjects, dir string, paths []string) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	for i := 0; i < len(paths); {
		name, _, nested := strings.Cut(paths[i], "/")
		if !nested {
			f := w.State.Files[joinPath(dir, name)]
			oid, _ := plumbing.FromHex(f.OID)
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.FileMode(f.Mode), Hash: oid})
			i++
			continue
		}
		var sub []string
		for ; i < len(paths) && strings.HasPrefix(paths[i], name+"/"); i++ {
			sub = append(sub, paths[i][len(name)+1:])
		}
		h, err := w.gitTree(objs, joinPath(dir, name), sub)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
	}
	sortTreeEntries(entries)
	o := &plumbing.MemoryObject{}
	if err := (&object.Tree{Entries: entries}).Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	data, err := readAll(o)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	objs.add(pendingObject{hash: o.Hash(), typ: plumbing.TreeObject, data: data})
	return o.Hash(), nil
}

func readAll(o plumbing.EncodedObject) ([]byte, error) {
	r, err := o.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// sortTreeEntries uses git's order: directories compare as if suffixed with
// "/".
func sortTreeEntries(es []object.TreeEntry) {
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(es, func(i, j int) bool { return key(es[i]) < key(es[j]) })
}

// gitCommit returns the snapshot commit for tree, reusing HEAD's when it
// already describes the same tree and base, so HEAD only moves when the
// workspace does.
func (w *Workspace) gitCommit(tree plumbing.Hash) (plumbing.Hash, error) {
	msg := fmt.Sprintf("gion workspace of %s at %s\n\nA local snapshot of the workspace files, not a commit on the remote.\n", w.State.Remote, w.State.Base)
	if cur, err := w.gitResolveHead(); err == nil {
		if o, err := w.gitGet(cur); err == nil {
			var c object.Commit
			if c.Decode(o) == nil && c.TreeHash == tree && c.Message == msg {
				return cur, nil
			}
		}
	}
	sig := snapshotAuthor
	sig.When = time.Now()
	o := &plumbing.MemoryObject{}
	if err := (&object.Commit{Author: sig, Committer: sig, Message: msg, TreeHash: tree}).Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	return o.Hash(), w.gitPut(o)
}

// gitBranch is the local branch HEAD points at: the branch commits go to,
// else the cloned ref when it names a branch.
func (w *Workspace) gitBranch() string {
	b := w.State.Branch
	if b == "" {
		b = strings.TrimPrefix(w.State.Ref, "refs/heads/")
	}
	if _, isHash := plumbing.FromHex(b); b == "" || b == "HEAD" || strings.HasPrefix(b, "refs/") || (isHash && len(b) == 40) {
		return "gion"
	}
	return b
}

func (w *Workspace) gitResolveHead() (plumbing.Hash, error) {
	b, err := os.ReadFile(w.gitPath("HEAD"))
	if err != nil {
		return plumbing.ZeroHash, err
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
	if !ok {
		return plumbing.ZeroHash, errors.New("detached HEAD")
	}
	b, err = os.ReadFile(w.gitPath(filepath.FromSlash(ref)))
	if err != nil {
		return plumbing.ZeroHash, err
	}
	h, ok := plumbing.FromHex(strings.TrimSpace(string(b)))
	if !ok {
		return plumbing.ZeroHash, fmt.Errorf("bad ref %s", ref)
	}
	return h, nil
}

// gitHead points HEAD at the branch, and the branch at commit. A ref left by
// an earlier branch name is removed.
func (w *Workspace) gitHead(commit plumbing.Hash) error {
	ref := "refs/heads/" + w.gitBranch()
	if b, err := os.ReadFile(w.gitPath("HEAD")); err == nil {
		if old, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: "); ok && old != ref && strings.HasPrefix(old, "refs/heads/") {
			_ = os.Remove(w.gitPath(filepath.FromSlash(old)))
		}
	}
	dst := w.gitPath(filepath.FromSlash(ref))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(dst, []byte(commit.String()+"\n")); err != nil {
		return err
	}
	return writeAtomic(w.gitPath("HEAD"), []byte("ref: "+ref+"\n"))
}

// gitIndex writes an index matching HEAD, with staged changes (from Status)
// on top. Each unstaged entry carries the size and mtime gion recorded when
// it wrote the file, so git sees an edited file as changed and rehashes it,
// the same way status does. Staged entries carry the file's current stat.
func (w *Workspace) gitIndex(paths []string, staged []FileChange) error {
	idx := &index.Index{Version: 2}
	stage := map[string]FileChange{}
	for _, c := range staged {
		stage[c.Path] = c
	}
	for _, p := range paths {
		if _, ok := stage[p]; ok {
			continue
		}
		f := w.State.Files[p]
		oid, _ := plumbing.FromHex(f.OID)
		mt := time.Unix(0, f.MTime)
		idx.Entries = append(idx.Entries, &index.Entry{
			Name: p, Hash: oid, Mode: filemode.FileMode(f.Mode),
			Size: uint32(f.Size), ModifiedAt: mt, CreatedAt: mt,
		})
	}
	for _, c := range staged {
		if c.Kind == Deleted {
			continue
		}
		fi, err := os.Lstat(w.abs(c.Path))
		if err != nil {
			return err
		}
		o := &plumbing.MemoryObject{}
		o.SetType(plumbing.BlobObject)
		o.Write(c.Data)
		if err := w.gitPut(o); err != nil {
			return err
		}
		mt := fi.ModTime()
		idx.Entries = append(idx.Entries, &index.Entry{
			Name: c.Path, Hash: c.OID, Mode: c.Mode,
			Size: uint32(fi.Size()), ModifiedAt: mt, CreatedAt: mt,
		})
	}
	lock := w.gitPath("index.lock")
	out, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("writing .git/index: %w", err)
	}
	defer os.Remove(lock) // no-op after a successful rename
	err = index.NewEncoder(out, sha1.New()).Encode(idx)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(lock, w.gitPath("index"))
}

func (w *Workspace) gitObjectPath(h plumbing.Hash) string {
	hex := h.String()
	return w.gitPath("objects", hex[:2], hex[2:])
}

// gitPut writes o as a loose object unless it's there already.
func (w *Workspace) gitPut(o plumbing.EncodedObject) error {
	data, err := readAll(o)
	if err != nil {
		return err
	}
	return w.gitPutRaw(o.Hash(), o.Type(), data)
}

// gitPutRaw writes a loose object of type t, id h and content data.
func (w *Workspace) gitPutRaw(h plumbing.Hash, t plumbing.ObjectType, data []byte) error {
	dst := w.gitObjectPath(h)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "tmp_obj_")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	ow := objfile.NewWriter(tmp, format.SHA1)
	err = ow.WriteHeader(t, int64(len(data)))
	if err == nil {
		_, err = ow.Write(data)
	}
	if cerr := ow.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// gitGet reads a loose object written by gitPut.
func (w *Workspace) gitGet(h plumbing.Hash) (plumbing.EncodedObject, error) {
	f, err := os.Open(w.gitObjectPath(h))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := objfile.NewReader(f, format.SHA1)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	t, size, err := r.Header()
	if err != nil {
		return nil, err
	}
	o := &plumbing.MemoryObject{}
	o.SetType(t)
	o.SetSize(size)
	if _, err := io.Copy(o, r); err != nil {
		return nil, err
	}
	return o, nil
}

func writeAtomic(dst string, data []byte) error {
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
