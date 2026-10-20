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

// ErrRateLimited means GitHub refused a request for exceeding a rate limit.
// Code search allows about 10 requests a minute per user.
type ErrRateLimited struct {
	Status  int
	Message string
	Retry   time.Duration // how long until GitHub accepts requests again; 0 if unknown
}

func (e *ErrRateLimited) Error() string {
	msg := fmt.Sprintf("GitHub code search rate limit hit (HTTP %d", e.Status)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	msg += ")"
	if e.Retry > 0 {
		msg += fmt.Sprintf("; retry in %s", e.Retry.Round(time.Second))
	}
	return msg
}

// rateLimited reports whether resp is GitHub refusing a request for a rate
// limit, primary or secondary. A plain 403 (no access) is not one.
func rateLimited(resp *http.Response, message string, now time.Time) *ErrRateLimited {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	e := &ErrRateLimited{Status: resp.StatusCode, Message: message}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		e.Retry = time.Duration(s) * time.Second
	} else if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		e.Retry = time.Unix(reset, 0).Sub(now)
	}
	if resp.StatusCode == http.StatusTooManyRequests || e.Retry > 0 || strings.Contains(strings.ToLower(message), "rate limit") {
		return e
	}
	return nil
}

// codeSearchQuery is the search query for literal in repo, limited to dir
// (a directory path, or "" for the whole repo).
func codeSearchQuery(literal, repo, dir string) string {
	q := fmt.Sprintf("%q repo:%s", literal, repo)
	if dir != "" {
		q += " path:" + dir
	}
	return q
}

// CodeSearch returns paths of files on the default branch that GitHub's code
// search says contain literal, under dir if it isn't "" (fewer results means
// fewer pages, and each page counts against the rate limit). GitHub caps
// results at 1000 and doesn't index every file (very large files, for one),
// so callers treat the result as candidates and verify them.
func CodeSearch(ctx context.Context, remote, literal, dir string) (paths []string, total int, err error) {
	repo, ok := GitHubRepo(remote)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	api, _ := url.Parse("https://github.com/")
	cred, err := auth.ForURL(ctx, api)
	if err != nil {
		return nil, 0, err
	}
	q := codeSearchQuery(literal, repo, dir)
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
		if rl := rateLimited(resp, body.Message, time.Now()); rl != nil {
			return nil, 0, rl
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

// githubGet sends an authenticated GET to the GitHub API and decodes JSON.
func githubGet(ctx context.Context, path string, out any) error {
	api, _ := url.Parse("https://github.com/")
	cred, err := auth.ForURL(ctx, api)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+cred.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// PathCommits lists up to max commits reachable from sha that changed path,
// newest first, as GitHub computes them (git log -- path).
func PathCommits(ctx context.Context, remote, sha, path string, max int) ([]string, error) {
	repo, ok := GitHubRepo(remote)
	if !ok {
		return nil, ErrUnsupported
	}
	var out []string
	for page := 1; len(out) < max; page++ {
		per := min(100, max-len(out))
		v := url.Values{"sha": {sha}, "path": {path}, "per_page": {strconv.Itoa(per)}, "page": {strconv.Itoa(page)}}
		var cs []struct {
			SHA string `json:"sha"`
		}
		if err := githubGet(ctx, "/repos/"+repo+"/commits?"+v.Encode(), &cs); err != nil {
			return nil, err
		}
		for _, c := range cs {
			out = append(out, c.SHA)
		}
		if len(cs) < per {
			break
		}
	}
	return out, nil
}

// BlameRange says lines Start..End (1-based, inclusive) last changed in Commit.
type BlameRange struct {
	Start, End int
	Commit     string
}

// Blame asks GitHub's GraphQL API for the blame of path at sha.
func Blame(ctx context.Context, remote, sha, path string) ([]BlameRange, error) {
	repo, ok := GitHubRepo(remote)
	if !ok {
		return nil, ErrUnsupported
	}
	owner, name, _ := strings.Cut(repo, "/")
	api, _ := url.Parse("https://github.com/")
	cred, err := auth.ForURL(ctx, api)
	if err != nil {
		return nil, err
	}
	q := `query($owner:String!,$name:String!,$rev:String!,$path:String!){repository(owner:$owner,name:$name){object(expression:$rev){... on Commit{blame(path:$path){ranges{startingLine endingLine commit{oid}}}}}}}`
	body, _ := json.Marshal(map[string]any{"query": q, "variables": map[string]string{"owner": owner, "name": name, "rev": sha, "path": path}})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.github.com/graphql", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github graphql: %s", resp.Status)
	}
	var out struct {
		Data struct {
			Repository struct {
				Object *struct {
					Blame *struct {
						Ranges []struct {
							StartingLine int `json:"startingLine"`
							EndingLine   int `json:"endingLine"`
							Commit       struct {
								OID string `json:"oid"`
							} `json:"commit"`
						} `json:"ranges"`
					} `json:"blame"`
				} `json:"object"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("github graphql: %s", out.Errors[0].Message)
	}
	o := out.Data.Repository.Object
	if o == nil || o.Blame == nil {
		return nil, fmt.Errorf("github: no blame for %s at %s", path, sha)
	}
	rs := make([]BlameRange, len(o.Blame.Ranges))
	for i, x := range o.Blame.Ranges {
		rs[i] = BlameRange{Start: x.StartingLine, End: x.EndingLine, Commit: x.Commit.OID}
	}
	return rs, nil
}
