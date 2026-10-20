package search

import "testing"

func TestCompilePath(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"util", "base/strings/string_util.h", true},
		{"UTIL", "base/strings/string_util.h", false}, // uppercase => case-sensitive
		{"Util", "base/strings/String_Util.h", true},
		{"*.h", "base/strings/string_util.h", true}, // no "/" => file name only
		{"*.h", "base/strings/string_util.cc", false},
		{"string_*.h", "base/strings/string_util.h", true},
		{"base/*.h", "base/strings/string_util.h", false}, // * stays in a segment
		{"base/**/*.h", "base/strings/string_util.h", true},
		{"base/**/*.h", "base/x.h", true}, // **/ matches zero dirs
		{"**/BUILD.gn", "a/b/BUILD.gn", true},
		{"file?.go", "x/file1.go", true},
		{"file[0-2].go", "x/file3.go", false},
		{"file[!0-2].go", "x/file3.go", true},
	}
	for _, c := range cases {
		m, err := CompilePath(c.pattern)
		if err != nil {
			t.Fatalf("%q: %v", c.pattern, err)
		}
		if got := m(c.path); got != c.want {
			t.Errorf("CompilePath(%q)(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestCompileGlob(t *testing.T) {
	m, _ := CompileGlob("base/strings")
	if !m("base/strings/x.h") || !m("base/strings") || m("base/stringsx/y") {
		t.Error("plain path should match itself and below only")
	}
	m, _ = CompileGlob("**/*_test.go")
	if !m("a/b/c_test.go") || !m("c_test.go") || m("a/c.go") {
		t.Error("glob mismatch")
	}
}

func TestLongestLiteral(t *testing.T) {
	cases := map[string]string{
		"TODO":                 "TODO",
		`func\s+NewEncoder\(`:  "NewEncoder(",
		`(foo|bar)baz`:         "baz",
		`foo|bar`:              "",
		`a.b`:                  "a",
		`MaybeLogFatal\b`:      "MaybeLogFatal",
		`[A-Z]+_SIZE_LIMIT \d`: "_SIZE_LIMIT ",
	}
	for p, want := range cases {
		if got := longestLiteral(p); got != want {
			t.Errorf("longestLiteral(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestLiteralDirs(t *testing.T) {
	got := LiteralDirs([]string{"base/strings/", "base/*/x.h", "a-c", "base/strings/y", "a/b/**/*.go"})
	want := []string{"a-c", "a/b", "base"}
	if len(got) != len(want) {
		t.Fatalf("LiteralDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("LiteralDirs = %v, want %v", got, want)
		}
	}
	if d := LiteralDirs([]string{"base", "*.go"}); d != nil {
		t.Errorf("unanchored glob: %v, want nil", d)
	}
}
