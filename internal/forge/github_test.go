package forge

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

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

func TestCodeSearchQuery(t *testing.T) {
	if got, want := codeSearchQuery("Hello", "o/r", ""), `"Hello" repo:o/r`; got != want {
		t.Errorf("no dir: %q, want %q", got, want)
	}
	if got, want := codeSearchQuery("Hello", "o/r", "base/strings"), `"Hello" repo:o/r path:base/strings`; got != want {
		t.Errorf("dir: %q, want %q", got, want)
	}
}

func TestRateLimited(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	resp := func(status int, h map[string]string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, c := range []struct {
		name    string
		resp    *http.Response
		message string
		limited bool
		retry   time.Duration
	}{
		{"ok", resp(200, nil), "", false, 0},
		{"429 with Retry-After", resp(429, map[string]string{"Retry-After": "30"}), "", true, 30 * time.Second},
		{"403 primary limit", resp(403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Unix()+42, 10)}), "API rate limit exceeded", true, 42 * time.Second},
		{"403 secondary limit", resp(403, nil), "You have exceeded a secondary rate limit", true, 0},
		{"403 no access", resp(403, map[string]string{"X-RateLimit-Remaining": "9"}), "Resource not accessible by integration", false, 0},
	} {
		e := rateLimited(c.resp, c.message, now)
		if (e != nil) != c.limited {
			t.Errorf("%s: limited = %v, want %v", c.name, e != nil, c.limited)
			continue
		}
		if e != nil && e.Retry != c.retry {
			t.Errorf("%s: retry = %s, want %s", c.name, e.Retry, c.retry)
		}
	}
}
