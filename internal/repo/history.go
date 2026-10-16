package repo

import (
	"container/heap"
	"context"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/baz-scm/gion/internal/forge"
)

// historyWindow is how many generations of commits one history fetch asks
// for. Commits come without trees or blobs, so a window is small.
const historyWindow = 256

// HistCommit returns commit h, fetching it together with the window of
// history behind it on a cache miss, so walking back costs one round trip
// per window instead of one per commit.
func (r *Repo) HistCommit(ctx context.Context, h plumbing.Hash) (*object.Commit, error) {
	if !r.Cache.Has(h) {
		s, err := r.session(ctx)
		if err != nil {
			return nil, err
		}
		objs, err := s.FetchHistory(ctx, []plumbing.Hash{h}, historyWindow)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			if err := r.Cache.Put(o); err != nil {
				return nil, err
			}
		}
	}
	return r.Commit(ctx, h)
}

// dateQueue pops the newest commit first, like git's default log order.
type dateQueue []*object.Commit

func (q dateQueue) Len() int { return len(q) }
func (q dateQueue) Less(i, j int) bool {
	a, b := q[i].Committer.When, q[j].Committer.When
	if a.Equal(b) {
		return q[i].Hash.String() < q[j].Hash.String() // stable for equal dates
	}
	return a.After(b)
}
func (q dateQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *dateQueue) Push(x any)   { *q = append(*q, x.(*object.Commit)) }
func (q *dateQueue) Pop() any {
	old := *q
	c := old[len(old)-1]
	*q = old[:len(old)-1]
	return c
}

// LogOptions limits a log walk.
type LogOptions struct {
	Max  int    // commits to return; 0 means 20
	Path string // only commits that changed this file or directory
	// MaxWalk caps how many commits a path-limited walk inspects, so a path
	// that rarely changes in a huge repo can't walk forever. 0 means 20000.
	MaxWalk int
	// Forge lets a path-limited log ask the forge's API (GitHub) instead of
	// walking history, which is much faster on big repos. On failure the
	// local walk is used.
	Forge bool
}

// LogResult is a log walk's commits, newest first.
type LogResult struct {
	Commits []*object.Commit
	// Truncated is set when MaxWalk stopped a path walk early.
	Truncated bool
	Walked    int
	Via       string // "forge" or "local"
}

// Log lists commits reachable from tip, newest first. With a path it uses
// git's default history simplification: a commit is shown if the path
// differs from every parent, and a merge that kept one parent's version only
// follows that parent.
func (r *Repo) Log(ctx context.Context, tip plumbing.Hash, o LogOptions) (LogResult, error) {
	if o.Max <= 0 {
		o.Max = 20
	}
	if o.MaxWalk <= 0 {
		o.MaxWalk = 20000
	}
	o.Path = strings.Trim(o.Path, "/")
	res := LogResult{Via: "local"}
	if o.Path != "" && o.Forge {
		if shas, err := forge.PathCommits(ctx, r.Remote, tip.String(), o.Path, o.Max); err == nil {
			hs := make([]plumbing.Hash, len(shas))
			for i, s := range shas {
				hs[i] = plumbing.NewHash(s)
			}
			if err := r.Ensure(ctx, hs...); err != nil {
				return res, err
			}
			for _, h := range hs {
				c, err := r.Commit(ctx, h)
				if err != nil {
					return res, err
				}
				res.Commits = append(res.Commits, c)
			}
			res.Via = "forge"
			return res, nil
		}
	}
	start, err := r.HistCommit(ctx, tip)
	if err != nil {
		return res, err
	}
	pe := newPathEntries(r, o.Path)
	q := &dateQueue{start}
	seen := map[plumbing.Hash]bool{tip: true}
	for q.Len() > 0 && len(res.Commits) < o.Max {
		c := heap.Pop(q).(*object.Commit)
		res.Walked++
		parents := make([]*object.Commit, 0, len(c.ParentHashes))
		for _, ph := range c.ParentHashes {
			p, err := r.HistCommit(ctx, ph)
			if err != nil {
				return res, err
			}
			parents = append(parents, p)
		}
		follow := parents
		if o.Path != "" {
			if res.Walked > o.MaxWalk {
				res.Truncated = true
				break
			}
			if err := pe.load(ctx, c, q); err != nil {
				return res, err
			}
			ec := pe.get(c.Hash)
			same := -1
			for i, p := range parents {
				if pe.get(p.Hash) == ec {
					same = i
					break
				}
			}
			switch {
			case same >= 0:
				follow = parents[same : same+1] // unchanged here: not shown
			case len(parents) == 0 && ec.missing():
				// root commit without the path
			default:
				res.Commits = append(res.Commits, c)
			}
		} else {
			res.Commits = append(res.Commits, c)
		}
		for _, p := range follow {
			if !seen[p.Hash] {
				seen[p.Hash] = true
				heap.Push(q, p)
			}
		}
	}
	return res, nil
}

// entry is what a path is at one commit: zero hash when absent.
type entry struct {
	hash plumbing.Hash
	mode filemode.FileMode
}

func (e entry) missing() bool { return e.hash.IsZero() }

// pathEntries finds what one path is at many commits, a batch at a time:
// each batch costs one round trip per path segment, not one per commit.
type pathEntries struct {
	r    *Repo
	segs []string
	m    map[plumbing.Hash]entry
}

func newPathEntries(r *Repo, p string) *pathEntries {
	var segs []string
	if p != "" {
		segs = strings.Split(p, "/")
	}
	return &pathEntries{r: r, segs: segs, m: map[plumbing.Hash]entry{}}
}

func (pe *pathEntries) get(commit plumbing.Hash) entry { return pe.m[commit] }

// load makes sure c, its parents, and (to batch the work) the commits
// waiting in q and their cached ancestors have entries.
func (pe *pathEntries) load(ctx context.Context, c *object.Commit, q *dateQueue) error {
	need := func(h plumbing.Hash) bool { _, ok := pe.m[h]; return !ok }
	if !need(c.Hash) && !anyMissing(c.ParentHashes, need) {
		return nil
	}
	const batch = 256
	var todo []*object.Commit
	added := map[plumbing.Hash]bool{}
	add := func(x *object.Commit) {
		if need(x.Hash) && !added[x.Hash] {
			added[x.Hash] = true
			todo = append(todo, x)
		}
	}
	add(c)
	for _, ph := range c.ParentHashes {
		if p, err := pe.r.Commit(ctx, ph); err == nil {
			add(p)
		}
	}
	// Walk cached history behind c (and queued commits) for more work to
	// batch; nothing here hits the network.
	frontier := append([]*object.Commit{c}, (*q)...)
	for i := 0; i < len(frontier) && len(todo) < batch; i++ {
		x := frontier[i]
		add(x)
		for _, ph := range x.ParentHashes {
			if pe.r.Cache.Has(ph) && len(frontier) < 4*batch {
				if p, err := pe.r.Commit(ctx, ph); err == nil {
					frontier = append(frontier, p)
				}
			}
		}
	}
	return pe.resolve(ctx, todo)
}

func anyMissing(hs []plumbing.Hash, need func(plumbing.Hash) bool) bool {
	for _, h := range hs {
		if need(h) {
			return true
		}
	}
	return false
}

// resolve looks up the path in every commit's tree, level by level.
func (pe *pathEntries) resolve(ctx context.Context, cs []*object.Commit) error {
	cur := make(map[plumbing.Hash]plumbing.Hash, len(cs)) // commit -> tree at this level
	for _, c := range cs {
		cur[c.Hash] = c.TreeHash
	}
	if len(pe.segs) == 0 {
		for _, c := range cs {
			pe.m[c.Hash] = entry{hash: c.TreeHash, mode: filemode.Dir}
		}
		return nil
	}
	for i, seg := range pe.segs {
		var trees []plumbing.Hash
		for _, t := range cur {
			trees = append(trees, t)
		}
		if err := pe.r.Ensure(ctx, trees...); err != nil {
			return err
		}
		next := map[plumbing.Hash]plumbing.Hash{}
		for ch, th := range cur {
			t, err := pe.r.Tree(ctx, th)
			if err != nil {
				return err
			}
			var found *object.TreeEntry
			for j := range t.Entries {
				if t.Entries[j].Name == seg {
					found = &t.Entries[j]
					break
				}
			}
			switch {
			case found == nil:
				pe.m[ch] = entry{}
			case i == len(pe.segs)-1:
				pe.m[ch] = entry{hash: found.Hash, mode: found.Mode}
			case found.Mode == filemode.Dir:
				next[ch] = found.Hash
			default:
				pe.m[ch] = entry{} // a file where a directory was expected
			}
		}
		cur = next
	}
	return nil
}

// Range returns the commits reachable from tip but not from upstream
// (git's upstream..tip), oldest first, and the best merge base. It paints
// history from both ends, newest first, and stops once every commit still
// in the queue is reachable from both.
func (r *Repo) Range(ctx context.Context, upstream, tip plumbing.Hash) (only []*object.Commit, base plumbing.Hash, err error) {
	const fromTip, fromUp = 1, 2
	flags := map[plumbing.Hash]int{}
	q := &dateQueue{}
	push := func(h plumbing.Hash, f int) error {
		if flags[h]|f == flags[h] {
			return nil
		}
		// New or newly reachable from the other side: (re)queue it so the
		// flag spreads to its ancestors.
		flags[h] |= f
		c, err := r.HistCommit(ctx, h)
		if err != nil {
			return err
		}
		heap.Push(q, c)
		return nil
	}
	if err := push(tip, fromTip); err != nil {
		return nil, base, err
	}
	if err := push(upstream, fromUp); err != nil {
		return nil, base, err
	}
	stale := func() bool {
		for _, c := range *q {
			if flags[c.Hash] != fromTip|fromUp {
				return false
			}
		}
		return true
	}
	var order []plumbing.Hash
	done := map[plumbing.Hash]int{}
	for q.Len() > 0 && !stale() {
		c := heap.Pop(q).(*object.Commit)
		f := flags[c.Hash]
		if done[c.Hash] == f {
			continue // popped before with the same flags
		}
		if done[c.Hash] == 0 {
			order = append(order, c.Hash)
		}
		done[c.Hash] = f
		if f == fromTip|fromUp && base.IsZero() {
			base = c.Hash
		}
		for _, ph := range c.ParentHashes {
			if err := push(ph, f); err != nil {
				return nil, base, err
			}
		}
	}
	if base.IsZero() && q.Len() > 0 {
		base = heap.Pop(q).(*object.Commit).Hash
	}
	for i := len(order) - 1; i >= 0; i-- {
		if flags[order[i]] == fromTip {
			c, err := r.Commit(ctx, order[i])
			if err != nil {
				return nil, base, err
			}
			only = append(only, c)
		}
	}
	return only, base, nil
}

// short is a commit's abbreviated id and subject, for messages.
func short(c *object.Commit) string {
	subject, _, _ := strings.Cut(c.Message, "\n")
	return fmt.Sprintf("%s %s", c.Hash.String()[:12], subject)
}
