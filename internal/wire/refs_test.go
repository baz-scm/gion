package wire

import (
	"testing"

	"github.com/go-git/go-git/v6/plumbing"
)

func TestParseRefLine(t *testing.T) {
	const a = "a8d7c6cdadebc220f2c3480edc1ac3362c1c27db"
	const b = "4fe89d07dcc2804c8b562f6c7896a45643d34b2f"
	tests := []struct {
		line string
		want Ref
	}{
		{a + " refs/heads/main", Ref{Name: "refs/heads/main", OID: mustHash(a)}},
		{a + " HEAD symref-target:refs/heads/main", Ref{Name: "HEAD", OID: mustHash(a), Target: "refs/heads/main"}},
		{"unborn HEAD symref-target:refs/heads/main", Ref{Name: "HEAD", Target: "refs/heads/main"}},
		{a + " refs/tags/v1 peeled:" + b, Ref{Name: "refs/tags/v1", OID: mustHash(a), Peeled: mustHash(b)}},
	}
	for _, tt := range tests {
		got, err := parseRefLine(tt.line)
		if err != nil {
			t.Fatalf("%q: %v", tt.line, err)
		}
		if got != tt.want {
			t.Errorf("%q: got %+v, want %+v", tt.line, got, tt.want)
		}
	}
	for _, bad := range []string{"", "HEAD", "zz refs/heads/main", a + " refs/tags/v1 peeled:zz"} {
		if _, err := parseRefLine(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

func mustHash(s string) (h plumbing.Hash) {
	h, ok := plumbing.FromHex(s)
	if !ok {
		panic(s)
	}
	return h
}
