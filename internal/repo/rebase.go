package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/baz-scm/gion/internal/wire"
)

// Conflict is a file a replayed commit couldn't merge.
type Conflict struct {
	Path string
	// Kind is "content", "binary", "modify/delete" (the commit deleted a
	// file the new base changed) or "delete/modify" (the reverse).
	Kind string
	// Data is what to put on disk for resolving: the merge with conflict
	// markers for "content", otherwise the surviving version.
	Data []byte
	Mode filemode.FileMode
}

// StoredChange is a Change whose content lives in the cache, so a rebase
// stopped on a conflict can be saved and resumed by a later process.
type StoredChange struct {
	Path   string `json:"path"`
	Blob   string `json:"blob,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// RebaseState is a rebase in progress. Commits are hex ids so it can be
// saved as JSON.
type RebaseState struct {
	Branch   string `json:"branch"`
	OntoName string `json:"onto_name"`
	Onto     string `json:"onto"`     // tip of the new base
	OrigTip  string `json:"orig_tip"` // branch tip before the rebase
	NewTip   string `json:"new_tip"`  // last commit of the rebuilt chain
	// Todo is what's left to replay, oldest first. When stopped on a
	// conflict, Todo[0] is the commit being resolved.
	Todo      []string       `json:"todo"`
	Clean     []StoredChange `json:"clean,omitempty"`     // merged part of Todo[0]
	Conflicts []string       `json:"conflicts,omitempty"` // unmerged paths of Todo[0]
	Objects   []string       `json:"objects,omitempty"`   // new objects to push
	Picked    int            `json:"picked"`
	Skipped   int            `json:"skipped"`
}

func hex(h plumbing.Hash) string { return h.String() }
func unhex(s string) plumbing.Hash {
	h, _ := plumbing.FromHex(s)
	return h
}

// ErrUpToDate means the branch already contains the new base.
var ErrUpToDate = errors.New("already up to date")

// StartRebase plans replaying branch's own commits (those not reachable from
// onto) onto onto's tip. Merge commits are dropped, as git rebase does.
func (r *Repo) StartRebase(ctx context.Context, branch, onto string) (*RebaseState, error) {
	tip, err := r.Resolve(ctx, "refs/heads/"+branch)
	if err != nil {
		return nil, fmt.Errorf("branch %s: %w", branch, err)
	}
	ontoTip, err := r.Resolve(ctx, onto)
	if err != nil {
		return nil, err
	}
	only, base, err := r.Range(ctx, ontoTip, tip)
	if err != nil {
		return nil, err
	}
	if base == ontoTip {
		return nil, fmt.Errorf("%s already contains %s: %w", branch, onto, ErrUpToDate)
	}
	st := &RebaseState{Branch: branch, OntoName: onto, Onto: hex(ontoTip), OrigTip: hex(tip), NewTip: hex(ontoTip)}
	for _, c := range only {
		if len(c.ParentHashes) <= 1 {
			st.Todo = append(st.Todo, hex(c.Hash))
		}
	}
	return st, nil
}

// Stopped is a conflict that paused a rebase.
type Stopped struct {
	Commit    *object.Commit
	Conflicts []Conflict
}

// Replay picks commits from st.Todo until done or a conflict. On a
// conflict, st holds the merged part and the conflicting paths, and the
// returned Stopped says what to resolve.
func (r *Repo) Replay(ctx context.Context, st *RebaseState, committer object.Signature) (*Stopped, error) {
	for len(st.Todo) > 0 {
		c, err := r.HistCommit(ctx, unhex(st.Todo[0]))
		if err != nil {
			return nil, err
		}
		clean, conflicts, err := r.pick(ctx, unhex(st.NewTip), c, st.OntoName)
		if err != nil {
			return nil, fmt.Errorf("replaying %s: %w", short(c), err)
		}
		if len(conflicts) > 0 {
			st.Clean = st.Clean[:0]
			for _, ch := range clean {
				sc, err := r.store(ch)
				if err != nil {
					return nil, err
				}
				st.Clean = append(st.Clean, sc)
			}
			st.Conflicts = st.Conflicts[:0]
			for _, cf := range conflicts {
				st.Conflicts = append(st.Conflicts, cf.Path)
			}
			return &Stopped{Commit: c, Conflicts: conflicts}, nil
		}
		if err := r.commitPick(ctx, st, c, clean, committer); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// ResolvePick finishes the commit a conflict stopped on, with the user's
// resolutions for its conflicting paths, then keeps replaying.
func (r *Repo) ResolvePick(ctx context.Context, st *RebaseState, resolved []Change, committer object.Signature) (*Stopped, error) {
	if len(st.Todo) == 0 {
		return nil, errors.New("no commit is waiting for a resolution")
	}
	c, err := r.HistCommit(ctx, unhex(st.Todo[0]))
	if err != nil {
		return nil, err
	}
	changes, err := r.load(ctx, st.Clean)
	if err != nil {
		return nil, err
	}
	changes = append(changes, resolved...)
	if err := r.commitPick(ctx, st, c, changes, committer); err != nil {
		return nil, err
	}
	return r.Replay(ctx, st, committer)
}

// SkipPick drops the commit a conflict stopped on and keeps replaying.
func (r *Repo) SkipPick(ctx context.Context, st *RebaseState, committer object.Signature) (*Stopped, error) {
	if len(st.Todo) == 0 {
		return nil, errors.New("no commit to skip")
	}
	st.Todo, st.Clean, st.Conflicts = st.Todo[1:], nil, nil
	st.Skipped++
	return r.Replay(ctx, st, committer)
}

// commitPick builds c's changes on the chain and advances st. Changes that
// are already in the new base leave nothing to commit: the commit is
// skipped, as git rebase does.
func (r *Repo) commitPick(ctx context.Context, st *RebaseState, c *object.Commit, changes []Change, committer object.Signature) error {
	st.Todo, st.Clean, st.Conflicts = st.Todo[1:], nil, nil
	if len(changes) == 0 {
		st.Skipped++
		return nil
	}
	b, err := r.build(ctx, unhex(st.NewTip), changes, c.Message, People{Author: c.Author, Committer: committer})
	if errors.Is(err, errNothing) {
		st.Skipped++
		return nil
	}
	if err != nil {
		return err
	}
	for _, o := range b.objs {
		st.Objects = append(st.Objects, hex(o.Hash()))
	}
	st.NewTip = hex(b.commit)
	st.Picked++
	return nil
}

// PushRebase moves the branch from its old tip to the rebuilt chain. It is
// compare-and-swap: if someone pushed to the branch during the rebase, the
// push is refused and nothing is lost.
func (r *Repo) PushRebase(ctx context.Context, st *RebaseState) error {
	var objs []plumbing.EncodedObject
	for _, h := range st.Objects {
		o, err := r.Cache.Get(unhex(h))
		if err != nil {
			return fmt.Errorf("rebased object %s missing from the cache: %w", h[:12], err)
		}
		objs = append(objs, o)
	}
	p, err := pack(objs)
	if err != nil {
		return err
	}
	ref := plumbing.NewBranchReferenceName(st.Branch)
	err = wire.Push(ctx, r.Remote, []wire.Update{{Ref: ref, Old: unhex(st.OrigTip), New: unhex(st.NewTip)}}, p)
	if err != nil && r.lostRace(ctx, ref, unhex(st.OrigTip), err) {
		return fmt.Errorf("%s moved during the rebase, so it wasn't pushed: abort and rebase again: %w", st.Branch, ErrConflict)
	}
	return err
}

// pick merges what commit c changed (against its first parent) into the
// tree of onto. It returns the merged changes and any conflicts.
func (r *Repo) pick(ctx context.Context, onto plumbing.Hash, c *object.Commit, ontoName string) ([]Change, []Conflict, error) {
	oc, err := r.Commit(ctx, onto)
	if err != nil {
		return nil, nil, err
	}
	var parentTree plumbing.Hash
	parentName := "(none)"
	if len(c.ParentHashes) > 0 {
		pc, err := r.HistCommit(ctx, c.ParentHashes[0])
		if err != nil {
			return nil, nil, err
		}
		parentTree, parentName = pc.TreeHash, pc.Hash.String()[:12]
	}
	ds, err := r.Changes(ctx, parentTree, c.TreeHash, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	labels := Labels{Ours: ontoName, Base: "base " + parentName, Theirs: short(c)}

	type side struct{ base, ours, theirs *FileEntry }
	sides := make([]side, len(ds))
	var blobs []plumbing.Hash
	for i, d := range ds {
		s := side{base: d.Old, theirs: d.New}
		e, err := r.Lookup(ctx, oc.TreeHash, d.Path)
		switch {
		case errors.Is(err, ErrNotExist):
		case err != nil:
			return nil, nil, err
		case e.Mode == filemode.Dir:
			return nil, nil, fmt.Errorf("%s is a directory in %s but a file in %s", d.Path, ontoName, short(c))
		default:
			s.ours = &FileEntry{Path: d.Path, Mode: e.Mode, Hash: e.Hash}
		}
		sides[i] = s
		for _, f := range []*FileEntry{s.base, s.ours, s.theirs} {
			if f != nil {
				blobs = append(blobs, f.Hash)
			}
		}
	}
	if err := r.Ensure(ctx, blobs...); err != nil {
		return nil, nil, err
	}
	read := func(f *FileEntry) ([]byte, error) {
		if f == nil {
			return nil, nil
		}
		return r.Blob(ctx, f.Hash)
	}
	same := func(a, b *FileEntry) bool {
		return (a == nil && b == nil) || (a != nil && b != nil && a.Hash == b.Hash && a.Mode == b.Mode)
	}

	var clean []Change
	var conflicts []Conflict
	for i, d := range ds {
		s := sides[i]
		switch {
		case same(s.ours, s.base): // untouched upstream: take the commit's version
			if s.theirs == nil {
				clean = append(clean, Change{Path: d.Path, Delete: true})
			} else {
				data, err := read(s.theirs)
				if err != nil {
					return nil, nil, err
				}
				clean = append(clean, Change{Path: d.Path, Data: data, Mode: s.theirs.Mode})
			}
		case same(s.ours, s.theirs): // the same change is upstream already
		case s.theirs == nil:
			data, err := read(s.ours)
			if err != nil {
				return nil, nil, err
			}
			conflicts = append(conflicts, Conflict{Path: d.Path, Kind: "modify/delete", Data: data, Mode: s.ours.Mode})
		case s.ours == nil:
			data, err := read(s.theirs)
			if err != nil {
				return nil, nil, err
			}
			conflicts = append(conflicts, Conflict{Path: d.Path, Kind: "delete/modify", Data: data, Mode: s.theirs.Mode})
		default:
			bd, err := read(s.base)
			if err != nil {
				return nil, nil, err
			}
			od, err := read(s.ours)
			if err != nil {
				return nil, nil, err
			}
			td, err := read(s.theirs)
			if err != nil {
				return nil, nil, err
			}
			mode := s.theirs.Mode // a mode change on one side wins
			if s.base != nil && s.ours.Mode != s.base.Mode {
				mode = s.ours.Mode
			}
			if isBinary(bd) || isBinary(od) || isBinary(td) {
				conflicts = append(conflicts, Conflict{Path: d.Path, Kind: "binary", Data: od, Mode: s.ours.Mode})
				continue
			}
			merged, conflict := merge3(string(bd), string(od), string(td), labels)
			if conflict {
				conflicts = append(conflicts, Conflict{Path: d.Path, Kind: "content", Data: []byte(merged), Mode: mode})
			} else {
				clean = append(clean, Change{Path: d.Path, Data: []byte(merged), Mode: mode})
			}
		}
	}
	return clean, conflicts, nil
}

func (r *Repo) store(c Change) (StoredChange, error) {
	sc := StoredChange{Path: c.Path, Mode: uint32(c.Mode), Delete: c.Delete}
	if c.Delete {
		return sc, nil
	}
	n := &newObjects{r: r}
	h, err := n.add(plumbing.BlobObject, c.Data)
	sc.Blob = hex(h)
	return sc, err
}

func (r *Repo) load(ctx context.Context, scs []StoredChange) ([]Change, error) {
	out := make([]Change, 0, len(scs))
	for _, sc := range scs {
		c := Change{Path: sc.Path, Mode: filemode.FileMode(sc.Mode), Delete: sc.Delete}
		if !sc.Delete {
			if !r.Cache.Has(unhex(sc.Blob)) {
				return nil, fmt.Errorf("saved change for %s is missing from the cache", sc.Path)
			}
			data, err := r.Blob(ctx, unhex(sc.Blob))
			if err != nil {
				return nil, err
			}
			c.Data = data
		}
		out = append(out, c)
	}
	return out, nil
}
