// Package search finds paths and contents in a repository without a clone.
package search

import (
	"regexp"
	"sort"
	"strings"
)

// PathMatcher matches repository paths.
type PathMatcher func(path string) bool

// CompilePath turns a pattern into a matcher, fd-style:
//   - with glob characters (* ? [ ]) it's a glob: "*" and "?" stay within a
//     path segment, "**" crosses them; without a "/" it matches the file name
//     only, with one it matches the whole path
//   - otherwise it's a substring of the path
//
// Matching is case-insensitive unless the pattern has an uppercase letter.
func CompilePath(pattern string) (PathMatcher, error) {
	fold := strings.ToLower(pattern) == pattern
	if !strings.ContainsAny(pattern, "*?[") {
		needle := pattern
		if fold {
			return func(p string) bool { return strings.Contains(strings.ToLower(p), needle) }, nil
		}
		return func(p string) bool { return strings.Contains(p, needle) }, nil
	}
	re, err := globRegexp(pattern, fold)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(pattern, "/") {
		return func(p string) bool { return re.MatchString(p[strings.LastIndex(p, "/")+1:]) }, nil
	}
	return re.MatchString, nil
}

// CompileGlob compiles a glob that always matches the whole path (for --path
// filters). A pattern without glob characters matches that path or anything
// below it.
func CompileGlob(pattern string) (PathMatcher, error) {
	pattern = strings.Trim(pattern, "/")
	if !strings.ContainsAny(pattern, "*?[") {
		return func(p string) bool { return p == pattern || strings.HasPrefix(p, pattern+"/") }, nil
	}
	re, err := globRegexp(pattern, false)
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

// LiteralDirs returns, for --path patterns as CompileGlob takes them, the
// paths every match must lie under: each pattern up to its first segment
// with glob characters. Paths nested in another are dropped. It returns nil
// if any pattern could match anywhere (e.g. "*.go" or "**/x").
func LiteralDirs(patterns []string) []string {
	var dirs []string
	for _, p := range patterns {
		var lit []string
		for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
			if strings.ContainsAny(seg, "*?[") {
				break
			}
			lit = append(lit, seg)
		}
		d := strings.Join(lit, "/")
		if d == "" {
			return nil
		}
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var out []string
outer:
	for _, d := range dirs {
		for _, o := range out {
			if d == o || strings.HasPrefix(d, o+"/") {
				continue outer
			}
		}
		out = append(out, d)
	}
	return out
}

func globRegexp(glob string, fold bool) (*regexp.Regexp, error) {
	var b strings.Builder
	if fold {
		b.WriteString("(?i)")
	}
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?") // "**/" also matches zero dirs
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + class + "]")
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
