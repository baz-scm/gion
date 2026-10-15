package repo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/format/packfile"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/memory"

	"github.com/baz-scm/gion/internal/wire"
)

// ErrConflict means a path we changed was also changed upstream.
var ErrConflict = errors.New("conflict")

// Change sets or deletes one file.
type Change struct {
	Path   string
	Data   []byte
	Delete bool
	// Mode for the file; zero keeps the existing mode (or regular if new).
	Mode filemode.FileMode
}

func cleanPath(p string) (string, error) {
	c := strings.Trim(path.Clean("/"+p), "/")
	if c == "" || c == "." {
		return "", fmt.Errorf("invalid path %q", p)
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == ".git" {
			return "", fmt.Errorf("invalid path %q: .git is reserved", p)
		}
	}
	return c, nil
}

// newObjects collects objects created for one commit.
type newObjects struct {
	r    *Repo
	objs []plumbing.EncodedObject
}

func (n *newObjects) add(t plumbing.ObjectType, data []byte) (plumbing.Hash, error) {
	o := n.r.Cache.New()
	o.SetType(t)
	o.SetSize(int64(len(data)))
	w, err := o.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	n.objs = append(n.objs, o)
	return o.Hash(), n.r.Cache.Put(o)
}

// buildTree applies changes (paths relative to this tree) to tree h, which
// is zero for a directory that does not exist yet. It returns the new tree id
// and whether the result is empty. Only trees on changed paths are fetched.
func (n *newObjects) buildTree(ctx context.Context, h plumbing.Hash, changes map[string]*Change) (plumbing.Hash, bool, error) {
	var entries []object.TreeEntry
	if !h.IsZero() {
		t, err := n.r.Tree(ctx, h)
		if err != nil {
			return plumbing.ZeroHash, false, err
		}
		entries = append(entries, t.Entries...)
	}
	index := map[string]int{}
	for i, e := range entries {
		index[e.Name] = i
	}

	files := map[string]*Change{}
	dirs := map[string]map[string]*Change{}
	for p, c := range changes {
		head, rest, nested := strings.Cut(p, "/")
		if !nested {
			files[head] = c
			continue
		}
		if dirs[head] == nil {
			dirs[head] = map[string]*Change{}
		}
		dirs[head][rest] = c
	}

	// Fetch every subtree we're about to descend into in one round trip.
	var prefetch []plumbing.Hash
	for name := range dirs {
		if i, ok := index[name]; ok && entries[i].Mode == filemode.Dir {
			prefetch = append(prefetch, entries[i].Hash)
		}
	}
	if err := n.r.Ensure(ctx, prefetch...); err != nil {
		return plumbing.ZeroHash, false, err
	}

	set := map[string]*object.TreeEntry{} // nil value = delete
	for name, c := range files {
		i, exists := index[name]
		if exists && entries[i].Mode == filemode.Dir {
			return plumbing.ZeroHash, false, fmt.Errorf("%s: is a directory", c.Path)
		}
		if c.Delete {
			if !exists {
				return plumbing.ZeroHash, false, fmt.Errorf("%s: %w", c.Path, ErrNotExist)
			}
			set[name] = nil
			continue
		}
		bh, err := n.add(plumbing.BlobObject, c.Data)
		if err != nil {
			return plumbing.ZeroHash, false, err
		}
		mode := filemode.Regular
		if exists {
			mode = entries[i].Mode // keep executable bit / symlink
		}
		if c.Mode != 0 {
			mode = c.Mode
		}
		set[name] = &object.TreeEntry{Name: name, Mode: mode, Hash: bh}
	}
	for name, sub := range dirs {
		var child plumbing.Hash
		if i, ok := index[name]; ok {
			if entries[i].Mode != filemode.Dir {
				return plumbing.ZeroHash, false, fmt.Errorf("%s: not a directory", name)
			}
			child = entries[i].Hash
		}
		th, empty, err := n.buildTree(ctx, child, sub)
		if err != nil {
			return plumbing.ZeroHash, false, err
		}
		if empty {
			set[name] = nil
		} else {
			set[name] = &object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: th}
		}
	}

	out := entries[:0:0]
	for _, e := range entries {
		if _, touched := set[e.Name]; !touched {
			out = append(out, e)
		}
	}
	for _, e := range set {
		if e != nil {
			out = append(out, *e)
		}
	}
	if len(out) == 0 {
		return plumbing.ZeroHash, true, nil
	}
	sortEntries(out)
	th, err := n.encodeTree(out)
	return th, false, err
}

func (n *newObjects) encodeTree(entries []object.TreeEntry) (plumbing.Hash, error) {
	o := n.r.Cache.New()
	if err := (&object.Tree{Entries: entries}).Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	n.objs = append(n.objs, o)
	return o.Hash(), n.r.Cache.Put(o)
}

// sortEntries uses git's order: directories compare as if suffixed with "/".
func sortEntries(es []object.TreeEntry) {
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(es, func(i, j int) bool { return key(es[i]) < key(es[j]) })
}

// People is who a commit is by.
type People struct {
	Author, Committer object.Signature
}

// Identity reads git's standard variables: GIT_AUTHOR_{NAME,EMAIL,DATE} and
// GIT_COMMITTER_{NAME,EMAIL,DATE}, falling back to user.name/user.email in
// ~/.gitconfig and the current time. The committer defaults to the author's
// name and email.
func Identity() (People, error) {
	var cfgName, cfgEmail string
	if cfg, err := config.LoadConfig(config.GlobalScope); err == nil {
		cfgName, cfgEmail = cfg.User.Name, cfg.User.Email
	}
	sig := func(role, defName, defEmail string) (object.Signature, error) {
		s := object.Signature{Name: os.Getenv("GIT_" + role + "_NAME"), Email: os.Getenv("GIT_" + role + "_EMAIL"), When: time.Now()}
		if s.Name == "" {
			s.Name = defName
		}
		if s.Email == "" {
			s.Email = defEmail
		}
		if d := os.Getenv("GIT_" + role + "_DATE"); d != "" {
			t, err := parseGitDate(d)
			if err != nil {
				return s, fmt.Errorf("GIT_%s_DATE: %w", role, err)
			}
			s.When = t
		}
		return s, nil
	}
	a, err := sig("AUTHOR", cfgName, cfgEmail)
	if err != nil {
		return People{}, err
	}
	if a.Name == "" || a.Email == "" {
		return People{}, errors.New("no author: set GIT_AUTHOR_NAME and GIT_AUTHOR_EMAIL, or user.name/user.email in ~/.gitconfig")
	}
	c, err := sig("COMMITTER", a.Name, a.Email)
	if err != nil {
		return People{}, err
	}
	return People{Author: a, Committer: c}, nil
}

// parseGitDate accepts ISO 8601 / RFC 3339 (git's %aI) and "@<unix> [+hhmm]"
// (git's internal raw format).
func parseGitDate(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if rest, ok := strings.CutPrefix(s, "@"); ok {
		secs, tz, _ := strings.Cut(rest, " ")
		var unix int64
		if _, err := fmt.Sscanf(secs, "%d", &unix); err == nil {
			t := time.Unix(unix, 0)
			if tz != "" {
				if z, err := time.Parse("-0700", tz); err == nil {
					t = t.In(z.Location())
				}
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported date %q: use RFC 3339 or @<unix> [+hhmm]", s)
}

// built is a commit ready to push.
type built struct {
	commit plumbing.Hash
	objs   []plumbing.EncodedObject
}

// build makes a commit on parent; a zero parent makes a root commit.
func (r *Repo) build(ctx context.Context, parent plumbing.Hash, changes []Change, msg string, who People) (built, error) {
	pc := &object.Commit{}
	if !parent.IsZero() {
		var err error
		if pc, err = r.Commit(ctx, parent); err != nil {
			return built{}, err
		}
	}
	byPath := map[string]*Change{}
	for i := range changes {
		byPath[changes[i].Path] = &changes[i]
	}
	n := &newObjects{r: r}
	root, empty, err := n.buildTree(ctx, pc.TreeHash, byPath)
	if err != nil {
		return built{}, err
	}
	if empty {
		if root, err = n.encodeTree(nil); err != nil {
			return built{}, err
		}
	}
	if !parent.IsZero() && root == pc.TreeHash {
		return built{}, errors.New("nothing to commit: changes match the base")
	}

	c := &object.Commit{
		Author:    who.Author,
		Committer: who.Committer,
		Message:   msg,
		TreeHash:  root,
	}
	if !parent.IsZero() {
		c.ParentHashes = []plumbing.Hash{parent}
	}
	o := r.Cache.New()
	if err := c.Encode(o); err != nil {
		return built{}, err
	}
	n.objs = append(n.objs, o)
	if err := r.Cache.Put(o); err != nil {
		return built{}, err
	}
	return built{commit: o.Hash(), objs: n.objs}, nil
}

func pack(objs []plumbing.EncodedObject) ([]byte, error) {
	st := memory.NewStorage()
	hs := make([]plumbing.Hash, 0, len(objs))
	for _, o := range objs {
		h, err := st.SetEncodedObject(o)
		if err != nil {
			return nil, err
		}
		hs = append(hs, h)
	}
	var buf bytes.Buffer
	if _, err := packfile.NewEncoder(&buf, st, false).Encode(hs, 10); err != nil {
		return nil, fmt.Errorf("encode pack: %w", err)
	}
	return buf.Bytes(), nil
}

// CommitOptions describes one commit-and-push.
type CommitOptions struct {
	Changes []Change
	Message string
	Who     People
	// Onto is the base rev for a new branch. Ignored when To is set.
	Onto string
	// Branch creates a new branch; fails if it exists.
	Branch string
	// Orphan makes Branch a root commit with no parent (e.g. the first
	// commit in an empty repository). Onto is ignored.
	Orphan bool
	// To updates an existing branch with compare-and-swap, rebasing onto
	// upstream moves that don't touch our paths.
	To string
	// Base is the commit the changes were made against. With To, an
	// upstream tip that differs from Base is accepted only if it left our
	// paths alone. Zero means "the tip when we start".
	Base plumbing.Hash
}

// CommitResult is what was pushed.
type CommitResult struct {
	Commit   plumbing.Hash
	Parent   plumbing.Hash
	Ref      plumbing.ReferenceName
	Created  bool
	Attempts int
	Objects  int
}

const maxAttempts = 3

// testHookBeforePush lets tests move the remote between build and push.
var testHookBeforePush func(attempt int)

// CommitAndPush builds a commit from changes and pushes it. There is no local
// branch: the remote ref is the only state.
func (r *Repo) CommitAndPush(ctx context.Context, o CommitOptions) (CommitResult, error) {
	for i := range o.Changes {
		p, err := cleanPath(o.Changes[i].Path)
		if err != nil {
			return CommitResult{}, err
		}
		o.Changes[i].Path = p
	}
	if len(o.Changes) == 0 {
		return CommitResult{}, errors.New("no changes")
	}

	if o.To == "" {
		ref := plumbing.NewBranchReferenceName(o.Branch)
		var base plumbing.Hash
		if !o.Orphan {
			var err error
			if base, err = r.Resolve(ctx, o.Onto); err != nil {
				return CommitResult{}, err
			}
		}
		b, err := r.build(ctx, base, o.Changes, o.Message, o.Who)
		if err != nil {
			return CommitResult{}, err
		}
		if err := r.push(ctx, ref, plumbing.ZeroHash, b); err != nil {
			if wire.IsRefRejected(err) {
				if _, rerr := r.Resolve(ctx, ref.String()); rerr == nil {
					return CommitResult{}, fmt.Errorf("branch %s already exists; pick another --branch or use --to", o.Branch)
				}
			}
			return CommitResult{}, err
		}
		return CommitResult{Commit: b.commit, Parent: base, Ref: ref, Created: true, Attempts: 1, Objects: len(b.objs)}, nil
	}

	ref := plumbing.NewBranchReferenceName(o.To)
	firstBase := o.Base
	for attempt := 1; ; attempt++ {
		tip, err := r.Resolve(ctx, ref.String())
		if err != nil {
			return CommitResult{}, err
		}
		if firstBase.IsZero() {
			firstBase = tip
		} else if tip != firstBase {
			if err := r.checkUntouched(ctx, firstBase, tip, o.Changes); err != nil {
				return CommitResult{}, err
			}
		}
		b, err := r.build(ctx, tip, o.Changes, o.Message, o.Who)
		if err != nil {
			return CommitResult{}, err
		}
		if testHookBeforePush != nil {
			testHookBeforePush(attempt)
		}
		err = r.push(ctx, ref, tip, b)
		if err == nil {
			return CommitResult{Commit: b.commit, Parent: tip, Ref: ref, Attempts: attempt, Objects: len(b.objs)}, nil
		}
		if !r.lostRace(ctx, ref, tip, err) {
			return CommitResult{}, err
		}
		if attempt == maxAttempts {
			return CommitResult{}, fmt.Errorf("%s kept moving; gave up after %d attempts: %w", o.To, attempt, err)
		}
		time.Sleep(time.Duration(attempt*attempt) * 200 * time.Millisecond)
	}
}

// lostRace reports whether a push failed only because ref moved off expected.
func (r *Repo) lostRace(ctx context.Context, ref plumbing.ReferenceName, expected plumbing.Hash, err error) bool {
	if !wire.IsRefRejected(err) {
		return false
	}
	now, rerr := r.Resolve(ctx, ref.String())
	return rerr == nil && now != expected
}

func (r *Repo) push(ctx context.Context, ref plumbing.ReferenceName, old plumbing.Hash, b built) error {
	p, err := pack(b.objs)
	if err != nil {
		return err
	}
	return wire.Push(ctx, r.Remote, []wire.Update{{Ref: ref, Old: old, New: b.commit}}, p)
}

// checkUntouched fails if upstream changed any of our paths between from and to.
func (r *Repo) checkUntouched(ctx context.Context, from, to plumbing.Hash, changes []Change) error {
	fc, err := r.Commit(ctx, from)
	if err != nil {
		return err
	}
	tc, err := r.Commit(ctx, to)
	if err != nil {
		return err
	}
	for _, c := range changes {
		a, aerr := r.Lookup(ctx, fc.TreeHash, c.Path)
		b, berr := r.Lookup(ctx, tc.TreeHash, c.Path)
		if errors.Is(aerr, ErrNotExist) && errors.Is(berr, ErrNotExist) {
			continue
		}
		if aerr != nil && !errors.Is(aerr, ErrNotExist) {
			return aerr
		}
		if berr != nil && !errors.Is(berr, ErrNotExist) {
			return berr
		}
		if a.Hash != b.Hash || a.Mode != b.Mode {
			return fmt.Errorf("%s changed upstream since %s: %w", c.Path, from.String()[:12], ErrConflict)
		}
	}
	return nil
}
