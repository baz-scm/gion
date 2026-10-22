package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"

	"github.com/baz-scm/gion/internal/forge"
	"github.com/baz-scm/gion/internal/index"
	"github.com/baz-scm/gion/internal/repo"
)

// Strategies for choosing which files to scan.
const (
	Auto  = "auto"  // local if the candidate set is small enough, else forge
	Local = "local" // fetch and scan every candidate
	Forge = "forge" // ask the forge's code search, scan only its hits
)

// GrepOptions configures Grep.
type GrepOptions struct {
	Pattern  string
	Fold     bool          // case-insensitive
	Paths    []PathMatcher // any must match; none means all files
	ForgeDir string        // a directory every path lies under, passed to code search; "" for none
	MaxFiles int           // Auto switches to Forge above this
	Strategy string
}

// Match is one matching line.
type Match struct {
	Path string
	Line int
	Text string
}

// GrepResult is what Grep found and how.
type GrepResult struct {
	Matches  []Match
	Strategy string // Local or Forge
	Scanned  int    // files actually read
	Note     string // caveats worth showing the user
}

const maxFileSize = 2 << 20 // skip files bigger than 2 MiB

// Grep searches file contents of idx. Results are always verified locally
// with the real regexp; the forge only narrows which files get read.
func Grep(ctx context.Context, r *repo.Repo, idx *index.Index, o GrepOptions) (GrepResult, error) {
	expr := o.Pattern
	if o.Fold {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return GrepResult{}, err
	}

	var cands []index.Entry
	for _, e := range idx.Entries {
		if e.Mode == filemode.Symlink || e.Mode == filemode.Submodule {
			continue
		}
		if len(o.Paths) == 0 || anyMatch(o.Paths, e.Path) {
			cands = append(cands, e)
		}
	}

	res := GrepResult{Strategy: o.Strategy}
	if res.Strategy == Auto {
		res.Strategy = Local
		if len(cands) > o.MaxFiles {
			res.Strategy = Forge
		}
	}
	if res.Strategy == Forge {
		lit := longestLiteral(o.Pattern)
		if len(lit) < 3 {
			return res, fmt.Errorf("%d files to search, and the pattern has no literal text of 3+ characters for code search to look up. %s; otherwise narrow with --path, or force a full scan with --local", len(cands), bazHint(r.Remote))
		}
		paths, total, err := forge.CodeSearch(ctx, r.Remote, lit, o.ForgeDir)
		if errors.Is(err, forge.ErrUnsupported) {
			return res, fmt.Errorf("%d files to search and %v. %s; otherwise narrow with --path or use --local", len(cands), err, bazHint(r.Remote))
		}
		var rl *forge.ErrRateLimited
		if errors.As(err, &rl) {
			wait := "wait a minute"
			if rl.Retry > 0 {
				wait = fmt.Sprintf("wait %s", rl.Retry.Round(time.Second))
			}
			return res, fmt.Errorf("%v\n%d files to search; GitHub allows about 10 code searches a minute. %s. Otherwise %s and rerun, narrow with --path so it scans locally, use --local to scan every file, or gion clone the folders you need and search them there", rl, len(cands), bazHint(r.Remote), wait)
		}
		if err != nil {
			return res, err
		}
		hit := map[string]bool{}
		for _, p := range paths {
			hit[p] = true
		}
		var narrowed []index.Entry
		for _, e := range cands {
			if hit[e.Path] {
				narrowed = append(narrowed, e)
			}
		}
		cands = narrowed
		res.Note = fmt.Sprintf("candidates from GitHub code search for %q (default branch index", lit)
		if total > len(paths) {
			res.Note += fmt.Sprintf(", %d of %d hits returned", len(paths), total)
		}
		res.Note += "); files GitHub doesn't index, or changed off the default branch, can be missed. " + bazHint(r.Remote)
	}

	hashes := make([]plumbing.Hash, len(cands))
	for i, e := range cands {
		hashes[i] = e.OID
	}
	if err := r.Ensure(ctx, hashes...); err != nil {
		return res, err
	}
	for _, e := range cands {
		data, err := r.Blob(ctx, e.OID)
		if err != nil {
			return res, err
		}
		res.Scanned++
		if len(data) > maxFileSize || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			continue // too big, or binary
		}
		for i, line := range bytes.Split(data, []byte("\n")) {
			if re.Match(line) {
				res.Matches = append(res.Matches, Match{Path: e.Path, Line: i + 1, Text: strings.TrimRight(string(line), "\r")})
			}
		}
	}
	return res, nil
}

func anyMatch(ms []PathMatcher, p string) bool {
	for _, m := range ms {
		if m(p) {
			return true
		}
	}
	return false
}

// longestLiteral returns the longest run of literal text every match of the
// regexp must contain, or "" if there's none (e.g. an alternation at the top).
func longestLiteral(pattern string) string {
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return ""
	}
	return lit(re.Simplify())
}

func lit(re *syntax.Regexp) string {
	switch re.Op {
	case syntax.OpLiteral:
		return string(re.Rune)
	case syntax.OpCapture, syntax.OpPlus:
		return lit(re.Sub[0])
	case syntax.OpConcat:
		best, run := "", ""
		for _, s := range re.Sub {
			if s.Op == syntax.OpLiteral {
				run += string(s.Rune)
				continue
			}
			if len(run) > len(best) {
				best = run
			}
			run = ""
			if l := lit(s); len(l) > len(best) {
				best = l
			}
		}
		if len(run) > len(best) {
			best = run
		}
		return best
	}
	return ""
}

// bazHint points agents at Baz's indexed search, which has no forge rate
// limit and takes a full regex, for when code search can't help.
func bazHint(remote string) string {
	name, ok := forge.GitHubRepo(remote)
	if !ok {
		name = remote
	}
	return fmt.Sprintf("Baz searches whole repos it indexes with no GitHub rate limit: use the Baz MCP server's remote_grep (repository %q) if you have it, or install it from https://github.com/baz-scm/baz-plugin", name)
}
