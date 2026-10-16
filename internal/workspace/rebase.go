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

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"

	"github.com/baz-scm/gion/internal/repo"
)

const rebaseFile = "rebase.json"

// rebaseState is a stopped rebase, saved in .gion/rebase.json.
type rebaseState struct {
	repo.RebaseState
	// Touched lists files written with conflicts, to restore at the end.
	Touched []string `json:"touched,omitempty"`
	// Added lists paths that joined the workspace only to show a conflict.
	Added []string `json:"added,omitempty"`
}

// ErrRebaseInProgress means a stopped rebase must be finished first.
var ErrRebaseInProgress = errors.New("a rebase is in progress: resolve the conflicts and run `gion rebase --continue`, or `--skip` / `--abort`")

// RebaseResult reports how a rebase step ended.
type RebaseResult struct {
	Branch, Onto    string
	OldTip, NewTip  plumbing.Hash
	Picked, Skipped int
	UpToDate        bool
	// Stopped is set when a conflict paused the rebase; the files are on
	// disk with conflict markers.
	Stopped   *repo.Stopped
	Remaining int // commits left after the stopped one
	Updated   SyncResult
}

func (w *Workspace) rebasePath() string { return filepath.Join(w.Dir, stateDir, rebaseFile) }

func (w *Workspace) loadRebase() (*rebaseState, error) {
	b, err := os.ReadFile(w.rebasePath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st rebaseState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", w.rebasePath(), err)
	}
	return &st, nil
}

func (w *Workspace) saveRebase(st *rebaseState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := w.rebasePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, w.rebasePath())
}

// Rebasing reports whether a rebase is stopped in this workspace.
func (w *Workspace) Rebasing() bool {
	_, err := os.Stat(w.rebasePath())
	return err == nil
}

func (w *Workspace) guardRebase() error {
	if w.Rebasing() {
		return ErrRebaseInProgress
	}
	return nil
}

// Rebase replays the workspace branch's own commits onto onto (default: the
// remote's default branch) and pushes the result with compare-and-swap.
// The workspace must have no pending edits and be at the branch tip. On a
// conflict it stops with the files written to disk with conflict markers.
func (w *Workspace) Rebase(ctx context.Context, r *repo.Repo, onto string) (RebaseResult, error) {
	if err := w.guardRebase(); err != nil {
		return RebaseResult{}, err
	}
	branch := w.State.Branch
	if branch == "" {
		return RebaseResult{}, errors.New("this workspace has no branch yet: commit first, or `gion switch <branch>`")
	}
	if onto == "" {
		var err error
		if onto, err = r.DefaultBranch(ctx); err != nil {
			return RebaseResult{}, err
		}
	}
	onto = strings.TrimPrefix(onto, "refs/heads/")
	if onto == branch {
		return RebaseResult{}, fmt.Errorf("can't rebase %s onto itself", branch)
	}
	changes, err := w.Status()
	if err != nil {
		return RebaseResult{}, err
	}
	if len(changes) > 0 {
		return RebaseResult{}, fmt.Errorf("commit or undo your %d pending change(s) before rebasing", len(changes))
	}
	tip, err := r.Resolve(ctx, "refs/heads/"+branch)
	if err != nil {
		return RebaseResult{}, err
	}
	if tip.String() != w.State.Base {
		return RebaseResult{}, fmt.Errorf("%s has commits this workspace doesn't: run `gion sync` first", branch)
	}

	rs, err := r.StartRebase(ctx, branch, onto)
	res := RebaseResult{Branch: branch, Onto: onto, OldTip: tip}
	if errors.Is(err, repo.ErrUpToDate) {
		res.UpToDate, res.NewTip = true, tip
		return res, nil
	}
	if err != nil {
		return res, err
	}
	st := &rebaseState{RebaseState: *rs}
	who, err := repo.Identity()
	if err != nil {
		return res, err
	}
	stopped, err := r.Replay(ctx, &st.RebaseState, who.Committer)
	if err != nil {
		return res, err
	}
	return w.rebaseStep(ctx, r, st, stopped)
}

// RebaseContinue takes the resolved files of the stopped commit and carries
// on. A file still holding conflict markers stops it; a deleted file
// resolves as a deletion.
func (w *Workspace) RebaseContinue(ctx context.Context, r *repo.Repo) (RebaseResult, error) {
	st, err := w.loadRebase()
	if err != nil || st == nil {
		return RebaseResult{}, orNoRebase(err)
	}
	newTip, err := r.Commit(ctx, plumbing.NewHash(st.NewTip))
	if err != nil {
		return RebaseResult{}, err
	}
	var resolved []repo.Change
	var unresolved []string
	for _, p := range st.Conflicts {
		fi, err := os.Lstat(w.abs(p))
		if errors.Is(err, fs.ErrNotExist) {
			if _, lerr := r.Lookup(ctx, newTip.TreeHash, p); lerr == nil {
				resolved = append(resolved, repo.Change{Path: p, Delete: true})
			}
			continue
		}
		if err != nil {
			return RebaseResult{}, err
		}
		data, mode, err := readDisk(w.abs(p), fi)
		if err != nil {
			return RebaseResult{}, err
		}
		if repo.HasConflictMarkers(data) {
			unresolved = append(unresolved, p)
			continue
		}
		resolved = append(resolved, repo.Change{Path: p, Data: data, Mode: mode})
	}
	if len(unresolved) > 0 {
		return RebaseResult{}, fmt.Errorf("still has conflict markers: %s", strings.Join(unresolved, ", "))
	}
	who, err := repo.Identity()
	if err != nil {
		return RebaseResult{}, err
	}
	stopped, err := r.ResolvePick(ctx, &st.RebaseState, resolved, who.Committer)
	if err != nil {
		return RebaseResult{}, err
	}
	return w.rebaseStep(ctx, r, st, stopped)
}

// RebaseSkip drops the stopped commit and carries on.
func (w *Workspace) RebaseSkip(ctx context.Context, r *repo.Repo) (RebaseResult, error) {
	st, err := w.loadRebase()
	if err != nil || st == nil {
		return RebaseResult{}, orNoRebase(err)
	}
	who, err := repo.Identity()
	if err != nil {
		return RebaseResult{}, err
	}
	stopped, err := r.SkipPick(ctx, &st.RebaseState, who.Committer)
	if err != nil {
		return RebaseResult{}, err
	}
	return w.rebaseStep(ctx, r, st, stopped)
}

// RebaseAbort puts the workspace back as it was before the rebase. Nothing
// was pushed, so the branch is untouched.
func (w *Workspace) RebaseAbort(ctx context.Context, r *repo.Repo) error {
	st, err := w.loadRebase()
	if err != nil || st == nil {
		return orNoRebase(err)
	}
	if err := w.restore(ctx, r, plumbing.NewHash(st.OrigTip), st.Touched); err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, p := range st.Added {
		drop[p] = true
	}
	kept := w.State.Paths[:0]
	for _, p := range w.State.Paths {
		if !drop[p] {
			kept = append(kept, p)
		}
	}
	w.State.Paths = kept
	if err := w.save(); err != nil {
		return err
	}
	return os.Remove(w.rebasePath())
}

func orNoRebase(err error) error {
	if err != nil {
		return err
	}
	return errors.New("no rebase in progress")
}

// rebaseStep either saves a stop (writing conflict files) or, when the
// replay finished, pushes and brings the workspace to the new tip.
func (w *Workspace) rebaseStep(ctx context.Context, r *repo.Repo, st *rebaseState, stopped *repo.Stopped) (RebaseResult, error) {
	res := RebaseResult{Branch: st.Branch, Onto: st.OntoName, OldTip: plumbing.NewHash(st.OrigTip), NewTip: plumbing.NewHash(st.NewTip), Picked: st.Picked, Skipped: st.Skipped}
	if stopped != nil {
		for _, cf := range stopped.Conflicts {
			_, tracked := w.State.Files[cf.Path]
			if !w.inScope(cf.Path) && !(tracked && w.inContext(cf.Path)) {
				w.State.Paths = append(w.State.Paths, cf.Path)
				sort.Strings(w.State.Paths)
				st.Added = append(st.Added, cf.Path)
			}
			if err := w.writeFile(cf.Path, cf.Mode, cf.Data); err != nil {
				return res, err
			}
			if !contains(st.Touched, cf.Path) {
				st.Touched = append(st.Touched, cf.Path)
			}
		}
		if err := w.save(); err != nil {
			return res, err
		}
		if err := w.saveRebase(st); err != nil {
			return res, err
		}
		res.Stopped, res.Remaining = stopped, len(st.Todo)-1
		return res, nil
	}

	if err := r.PushRebase(ctx, &st.RebaseState); err != nil {
		if err2 := w.saveRebase(st); err2 != nil {
			return res, err2
		}
		return res, err
	}
	newTip := plumbing.NewHash(st.NewTip)
	if err := w.restore(ctx, r, newTip, st.Touched); err != nil {
		return res, fmt.Errorf("pushed %s, but updating files failed (run `gion sync`): %w", st.NewTip[:12], err)
	}
	up, err := w.applyUpstream(ctx, r, plumbing.NewHash(st.OrigTip), newTip, nil)
	res.Updated = up
	if err != nil {
		return res, fmt.Errorf("pushed %s, but updating files failed (run `gion sync`): %w", st.NewTip[:12], err)
	}
	w.State.Base = st.NewTip
	if err := w.save(); err != nil {
		return res, err
	}
	if err := os.Remove(w.rebasePath()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	return res, nil
}

// restore sets each of paths on disk to its version at commit (removing it
// if absent there) and records it.
func (w *Workspace) restore(ctx context.Context, r *repo.Repo, commit plumbing.Hash, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	c, err := r.Commit(ctx, commit)
	if err != nil {
		return err
	}
	for _, p := range paths {
		e, err := r.Lookup(ctx, c.TreeHash, p)
		if errors.Is(err, repo.ErrNotExist) || (err == nil && e.Mode == filemode.Dir) {
			if err := os.Remove(w.abs(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			w.pruneEmptyDirs(p)
			delete(w.State.Files, p)
			continue
		}
		if err != nil {
			return err
		}
		data, err := r.Blob(ctx, e.Hash)
		if err != nil {
			return err
		}
		if err := w.writeFile(p, e.Mode, data); err != nil {
			return err
		}
		if err := w.record(p, e.Hash, e.Mode); err != nil {
			return err
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
