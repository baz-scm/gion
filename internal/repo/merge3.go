package repo

import (
	"strings"

	"github.com/go-git/go-git/v6/utils/diff"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// hunk replaces base lines [start, end) with lines.
type hunk struct {
	start, end int
	lines      []string
}

// hunks lists the changes from base to other as line ranges of base.
func hunks(base, other string) []hunk {
	var out []hunk
	bi := 0
	var cur *hunk
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, d := range diff.Do(base, other) {
		ls := splitLines(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			flush()
			bi += len(ls)
		case diffmatchpatch.DiffDelete:
			if cur == nil {
				cur = &hunk{start: bi, end: bi}
			}
			bi += len(ls)
			cur.end = bi
		case diffmatchpatch.DiffInsert:
			if cur == nil {
				cur = &hunk{start: bi, end: bi}
			}
			cur.lines = append(cur.lines, ls...)
		}
	}
	flush()
	return out
}

func overlaps(a, b hunk) bool {
	if a.start == b.start {
		return true // same spot, e.g. two insertions there
	}
	if a.start < b.end && b.start < a.end {
		return true
	}
	// An insertion strictly inside the other's replaced range.
	return (a.start == a.end && b.start < a.start && a.start < b.end) ||
		(b.start == b.end && a.start < b.start && b.start < a.end)
}

// Labels name the sides in conflict markers.
type Labels struct{ Ours, Base, Theirs string }

// merge3 merges ours and theirs, both edited from base, line by line. Changes
// to different lines combine; changes to the same lines combine only if they
// are identical, else the region becomes a conflict in diff3 style:
//
//	<<<<<<< ours
//	...
//	||||||| base
//	...
//	=======
//	...
//	>>>>>>> theirs
func merge3(base, ours, theirs string, l Labels) (string, bool) {
	bl := splitLines(base)
	oh, th := hunks(base, ours), hunks(base, theirs)
	var b strings.Builder
	conflict := false
	pos := 0 // next base line to copy
	i, j := 0, 0
	apply := func(hs []hunk, start, end int) string {
		var s strings.Builder
		at := start
		for _, h := range hs {
			s.WriteString(strings.Join(bl[at:h.start], ""))
			s.WriteString(strings.Join(h.lines, ""))
			at = h.end
		}
		s.WriteString(strings.Join(bl[at:end], ""))
		return s.String()
	}
	for i < len(oh) || j < len(th) {
		// Start a group with the earliest hunk, then pull in every hunk of
		// either side that overlaps the group.
		var og, tg []hunk
		var start, end int
		if j >= len(th) || (i < len(oh) && oh[i].start <= th[j].start) {
			og, start, end = append(og, oh[i]), oh[i].start, oh[i].end
			i++
		} else {
			tg, start, end = append(tg, th[j]), th[j].start, th[j].end
			j++
		}
		for grew := true; grew; {
			grew = false
			g := hunk{start: start, end: end}
			if i < len(oh) && overlaps(g, oh[i]) {
				og = append(og, oh[i])
				end = max(end, oh[i].end)
				i++
				grew = true
			}
			if j < len(th) && overlaps(g, th[j]) {
				tg = append(tg, th[j])
				end = max(end, th[j].end)
				j++
				grew = true
			}
		}
		b.WriteString(strings.Join(bl[pos:start], ""))
		pos = end
		o, t := apply(og, start, end), apply(tg, start, end)
		switch {
		case len(tg) == 0:
			b.WriteString(o)
		case len(og) == 0, o == t:
			b.WriteString(t)
		default:
			conflict = true
			b.WriteString("<<<<<<< " + l.Ours + "\n")
			b.WriteString(endLine(o))
			b.WriteString("||||||| " + l.Base + "\n")
			b.WriteString(endLine(strings.Join(bl[start:end], "")))
			b.WriteString("=======\n")
			b.WriteString(endLine(t))
			b.WriteString(">>>>>>> " + l.Theirs + "\n")
		}
	}
	b.WriteString(strings.Join(bl[pos:], ""))
	return b.String(), conflict
}

// endLine makes sure a non-empty block ends with a newline, so a marker
// never lands on the end of a content line.
func endLine(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

// HasConflictMarkers reports whether data still holds conflict markers.
func HasConflictMarkers(data []byte) bool {
	for _, l := range strings.Split(string(data), "\n") {
		// "=======" alone is also a Markdown heading underline, so only the
		// unambiguous markers count.
		if strings.HasPrefix(l, "<<<<<<< ") || strings.HasPrefix(l, ">>>>>>> ") {
			return true
		}
	}
	return false
}
