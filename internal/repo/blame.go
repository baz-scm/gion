package repo

import (
	"container/heap"
	"context"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"

	"github.com/baz-scm/gion/internal/forge"
)

// BlameLine is one line of a file and the commit that last changed it.
type BlameLine struct {
	Text   string
	Commit plumbing.Hash
}

// BlameResult is a whole file's blame plus the commits it refers to.
type BlameResult struct {
	Lines   []BlameLine
	Commits map[plumbing.Hash]*object.Commit
	Via     string // "forge" or "local"
}

// BlameVia is Blame that first asks the forge (GitHub's GraphQL blame) when
// useForge is set, falling back to the local walk.
func (r *Repo) BlameVia(ctx context.Context, tip plumbing.Hash, p string, useForge bool) (BlameResult, error) {
	if useForge {
		if res, err := r.forgeBlame(ctx, tip, strings.Trim(p, "/")); err == nil {
			return res, nil
		}
	}
	return r.Blame(ctx, tip, p)
}

func (r *Repo) forgeBlame(ctx context.Context, tip plumbing.Hash, p string) (BlameResult, error) {
	res := BlameResult{Commits: map[plumbing.Hash]*object.Commit{}, Via: "forge"}
	ranges, err := forge.Blame(ctx, r.Remote, tip.String(), p)
	if err != nil {
		return res, err
	}
	c, err := r.Commit(ctx, tip)
	if err != nil {
		return res, err
	}
	e, err := r.Lookup(ctx, c.TreeHash, p)
	if err != nil {
		return res, err
	}
	data, err := r.Blob(ctx, e.Hash)
	if err != nil {
		return res, err
	}
	lines := splitLines(string(data))
	res.Lines = make([]BlameLine, len(lines))
	var hs []plumbing.Hash
	for _, rg := range ranges {
		h := plumbing.NewHash(rg.Commit)
		hs = append(hs, h)
		for l := rg.Start; l <= rg.End && l <= len(lines); l++ {
			res.Lines[l-1].Commit = h
		}
	}
	for i, l := range lines {
		res.Lines[i].Text = l
		if res.Lines[i].Commit.IsZero() {
			return res, fmt.Errorf("github blame left line %d unassigned", i+1)
		}
	}
	if err := r.Ensure(ctx, hs...); err != nil {
		return res, err
	}
	for _, h := range hs {
		if res.Commits[h] == nil {
			if res.Commits[h], err = r.Commit(ctx, h); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

// Blame finds, for each line of path at commit tip, the commit that last
// changed it. It walks the path's history newest first: a commit where the
// file is unchanged from a parent passes all its lines to that parent;
// otherwise lines that exist in a parent (by a line diff) pass to it and the
// rest are blamed on the commit. Renames are not followed.
func (r *Repo) Blame(ctx context.Context, tip plumbing.Hash, p string) (BlameResult, error) {
	p = strings.Trim(p, "/")
	res := BlameResult{Commits: map[plumbing.Hash]*object.Commit{}, Via: "local"}
	start, err := r.HistCommit(ctx, tip)
	if err != nil {
		return res, err
	}
	pe := newPathEntries(r, p)
	q := &dateQueue{}
	if err := pe.load(ctx, start, q); err != nil {
		return res, err
	}
	e := pe.get(tip)
	if e.missing() || !e.mode.IsFile() {
		return res, fmt.Errorf("%s at %s: %w (or not a file)", p, tip.String()[:12], ErrNotExist)
	}
	data, err := r.Blob(ctx, e.hash)
	if err != nil {
		return res, err
	}
	lines := splitLines(string(data))
	res.Lines = make([]BlameLine, len(lines))
	for i, l := range lines {
		res.Lines[i].Text = l
	}

	// pending[c] maps lines of c's version (by index) to final line numbers.
	type pend struct{ line, final int }
	pending := map[plumbing.Hash][]pend{}
	all := make([]pend, len(lines))
	for i := range lines {
		all[i] = pend{i, i}
	}
	pending[tip] = all
	queued := map[plumbing.Hash]bool{tip: true}
	heap.Push(q, start)
	left := len(lines)
	blame := func(c *object.Commit, ps []pend) {
		for _, x := range ps {
			if res.Lines[x.final].Commit.IsZero() {
				res.Lines[x.final].Commit = c.Hash
				left--
			}
		}
		res.Commits[c.Hash] = c
	}
	give := func(to *object.Commit, ps []pend) {
		if len(ps) == 0 {
			return
		}
		pending[to.Hash] = append(pending[to.Hash], ps...)
		if !queued[to.Hash] {
			queued[to.Hash] = true
			heap.Push(q, to)
		}
	}

	for q.Len() > 0 && left > 0 {
		c := heap.Pop(q).(*object.Commit)
		queued[c.Hash] = false
		ps := pending[c.Hash]
		delete(pending, c.Hash)
		if len(ps) == 0 {
			continue
		}
		if err := pe.load(ctx, c, q); err != nil {
			return res, err
		}
		ec := pe.get(c.Hash)
		parents := make([]*object.Commit, 0, len(c.ParentHashes))
		for _, ph := range c.ParentHashes {
			pc, err := r.HistCommit(ctx, ph)
			if err != nil {
				return res, err
			}
			parents = append(parents, pc)
		}
		if err := pe.load(ctx, c, q); err != nil { // parents are cached now
			return res, err
		}
		passed := false
		for _, pc := range parents {
			if pe.get(pc.Hash) == ec {
				give(pc, ps) // unchanged here
				passed = true
				break
			}
		}
		if passed {
			continue
		}
		mine, err := r.Blob(ctx, ec.hash)
		if err != nil {
			return res, err
		}
		for _, pc := range parents {
			pe2 := pe.get(pc.Hash)
			if pe2.missing() || !pe2.mode.IsFile() || len(ps) == 0 {
				continue
			}
			theirs, err := r.Blob(ctx, pe2.hash)
			if err != nil {
				return res, err
			}
			m := lineMap(string(theirs), string(mine))
			var keep, pass []pend
			for _, x := range ps {
				if pl, ok := m[x.line]; ok {
					pass = append(pass, pend{pl, x.final})
				} else {
					keep = append(keep, x)
				}
			}
			give(pc, pass)
			ps = keep
		}
		blame(c, ps)
	}
	return res, nil
}

// lineMap maps line indexes of dst to the equal lines of src, using a line
// diff from src to dst.
func lineMap(src, dst string) map[int]int {
	m := map[int]int{}
	si, di := 0, 0
	for _, d := range diff.Do(src, dst) {
		n := len(splitLines(d.Text))
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			for k := 0; k < n; k++ {
				m[di+k] = si + k
			}
			si, di = si+n, di+n
		case diffmatchpatch.DiffDelete:
			si += n
		case diffmatchpatch.DiffInsert:
			di += n
		}
	}
	return m
}

// splitLines splits s into lines that keep their "\n".
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	ls := strings.SplitAfter(s, "\n")
	if ls[len(ls)-1] == "" {
		ls = ls[:len(ls)-1]
	}
	return ls
}
