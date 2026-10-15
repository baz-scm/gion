// Package auth borrows HTTPS credentials from tools the user already has.
// Nothing is stored: every call asks the helper again.
package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v6/plumbing/transport/http"
)

// ErrNoCredentials is returned when no helper produced a credential.
var ErrNoCredentials = errors.New("no credentials: set GITHUB_TOKEN, run `gh auth login`, or configure a git credential helper")

// ForURL resolves basic auth for u. Sources, in order:
//  1. GITHUB_TOKEN env var (github.com only, so it never leaks elsewhere)
//  2. `gh auth token --hostname <host>`
//  3. `git credential fill`
func ForURL(ctx context.Context, u *url.URL) (*http.BasicAuth, error) {
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && u.Hostname() == "github.com" {
		return tokenAuth(tok), nil
	}
	if tok, err := ghToken(ctx, u.Hostname()); err == nil && tok != "" {
		return tokenAuth(tok), nil
	}
	if a, err := gitCredential(ctx, u); err == nil {
		return a, nil
	}
	return nil, ErrNoCredentials
}

// tokenAuth wraps a token the way GitHub's smart-HTTP endpoint expects.
func tokenAuth(tok string) *http.BasicAuth {
	return &http.BasicAuth{Username: "x-access-token", Password: tok}
}

func ghToken(ctx context.Context, host string) (string, error) {
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", host).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitCredential(ctx context.Context, u *url.URL) (*http.BasicAuth, error) {
	in := fmt.Sprintf("protocol=%s\nhost=%s\npath=%s\n\n", u.Scheme, u.Host, strings.TrimPrefix(u.Path, "/"))
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader(in)
	// Never let a helper block on an interactive prompt.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	a := &http.BasicAuth{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		k, v, ok := strings.Cut(string(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "username":
			a.Username = v
		case "password":
			a.Password = v
		}
	}
	if a.Password == "" {
		return nil, ErrNoCredentials
	}
	return a, nil
}
