package cli

import "testing"

func TestDefaultCloneDir(t *testing.T) {
	for in, want := range map[string]string{
		"vercel/next.js":                         "next.js",
		"baz-scm/gion":                           "gion",
		"baz-scm/gion.git":                       "gion",
		"baz-scm/gion/":                          "gion",
		"https://github.com/baz-scm/gion":        "gion",
		"https://github.com/baz-scm/gion.git":    "gion",
		"https://gitlab.com/group/sub/repo.git/": "repo",
		"git@github.com:baz-scm/gion.git":        "gion",
		"git@host:repo":                          "repo",
		"ssh://git@host:2222/owner/repo.git":     "repo",
		"file:///srv/git/project.git":            "project",
		"git://localhost:9418/seed":              "seed",
		"https://host/":                          "host",
		"":                                       "",
		"/":                                      "",
		".git":                                   "",
		"owner/..":                               "",
	} {
		if got := defaultCloneDir(in); got != want {
			t.Errorf("defaultCloneDir(%q) = %q, want %q", in, got, want)
		}
	}
}
