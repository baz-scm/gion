// Package forge talks to forge-specific APIs for things the git protocol
// can't do. Only GitHub is supported so far.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/baz-scm/gion/internal/auth"
	"github.com/baz-scm/gion/internal/wire"
)

// ErrUnsupported means the remote isn't on a forge we have an API for.
var ErrUnsupported = errors.New("code search is only available for github.com remotes")

// GitHubRepo returns "owner/repo" for a github.com remote.
func GitHubRepo(remote string) (string, bool) {
	u := wire.RemoteURL(remote)
	for _, prefix := range []string{"https://github.com/", "ssh://git@github.com/", "git@github.com:"} {
		if rest, ok := strings.CutPrefix(u, prefix); ok {
			rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".git")
			if strings.Count(rest, "/") == 1 {
				return rest, true
			}
		}
	}
	return "", false
}

// CodeSearch returns paths of files on the default branch that GitHub's code
// search says contain literal. GitHub caps results at 1000 and doesn't index
// every file (very large files, for one), so callers treat the result as
// candidates and verify them.
func CodeSearch(ctx context.Context, remote, literal string) (paths []string, total int, err error) {
	repo, ok := GitHubRepo(remote)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	api, _ := url.Parse("https://github.com/")
	cred, err := auth.ForURL(ctx, api)
	if err != nil {
		return nil, 0, err
	}
	q := fmt.Sprintf("%q repo:%s", literal, repo)
	seen := map[string]bool{}
	for page := 1; page <= 10; page++ {
		v := url.Values{"q": {q}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/search/code?"+v.Encode(), nil)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Authorization", "Bearer "+cred.Password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		var body struct {
			Total int `json:"total_count"`
			Items []struct {
				Path string `json:"path"`
			} `json:"items"`
			Message string `json:"message"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			wait := ""
			if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				wait = fmt.Sprintf("; retry in %s", time.Until(time.Unix(reset, 0)).Round(time.Second))
			}
			return nil, 0, fmt.Errorf("github code search rate limited (%s)%s", body.Message, wait)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, 0, fmt.Errorf("github code search: %s: %s", resp.Status, body.Message)
		}
		if err != nil {
			return nil, 0, err
		}
		total = body.Total
		for _, it := range body.Items {
			if !seen[it.Path] {
				seen[it.Path] = true
				paths = append(paths, it.Path)
			}
		}
		if len(body.Items) < 100 || len(seen) >= total {
			break
		}
	}
	return paths, total, nil
}

// ResolveCommit expands an abbreviated commit id to the full one using the
// forge's API; the git protocol can't look up id prefixes.
func ResolveCommit(ctx context.Context, remote, short string) (string, error) {
	repo, ok := GitHubRepo(remote)
	if !ok {
		return "", errors.New("short commit ids can only be resolved on github.com remotes; use the full 40-character id")
	}
	api, _ := url.Parse("https://github.com/")
	cred, err := auth.ForURL(ctx, api)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+repo+"/commits/"+url.PathEscape(short), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.sha") // body is just the id
	req.Header.Set("Authorization", "Bearer "+cred.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var body strings.Builder
	if _, err := io.Copy(&body, io.LimitReader(resp.Body, 4096)); err != nil {
		return "", err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return strings.TrimSpace(body.String()), nil
	case http.StatusNotFound, http.StatusUnprocessableEntity:
		return "", fmt.Errorf("no commit %s on github.com/%s (or the prefix is ambiguous)", short, repo)
	}
	return "", fmt.Errorf("github: resolving %s: %s", short, resp.Status)
}
