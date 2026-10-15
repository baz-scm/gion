package forge

import "testing"

func TestGitHubRepo(t *testing.T) {
	for in, want := range map[string]string{
		"baz-scm/gion":                        "baz-scm/gion",
		"https://github.com/baz-scm/gion.git": "baz-scm/gion",
		"git@github.com:baz-scm/gion.git":     "baz-scm/gion",
		"ssh://git@github.com/baz-scm/gion":   "baz-scm/gion",
		"https://gitlab.com/a/b.git":          "",
		"git://127.0.0.1:9418/r.git":          "",
	} {
		got, ok := GitHubRepo(in)
		if got != want || ok != (want != "") {
			t.Errorf("GitHubRepo(%q) = %q, %v", in, got, ok)
		}
	}
}
