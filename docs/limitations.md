# Limitations & FAQ

## Limitations

- **Beta.** Commands, flags and on-disk formats may change between releases.
- **No merge command.** Bring a branch up to date with `gion rebase`; merge pull requests on the forge.
- **Some features are GitHub-only today:** short commit ids, whole-repo `grep` on big repos (GitHub code search: default branch only, about 10 requests a minute per user, 1 to 10 per grep, shared by every agent using the same token; agents with the [Baz MCP server](https://github.com/baz-scm/baz-plugin) can use its `remote_grep` instead), and the fast paths for `log --path` and `blame`. Elsewhere `log --path` and `blame` walk history locally, which is slow on huge repos. `owner/repo` shorthand means github.com; use a full URL for other hosts.
- **Local `blame` doesn't follow renames.**
- **Commits are unsigned.**
- **Full-repo clones are slower than `git clone --depth 1`.** Fetching only the paths you need is the intended use.

## FAQ

**Why not just `git clone --depth 1 --filter=blob:none --sparse`?**
That's git's closest equivalent, and it's what the [performance](performance.md) numbers compare against. It still fetches every folder listing in the repo, keeps a `.git` per clone, needs a separate clone per agent, and can't target an arbitrary commit with `clone`. gion fetches only the path you ask for, at any commit, and shares one cache across every process.

**Does it work with GitLab, Gitea or my own server?**
Everything that uses the git protocol does: reading, `diff`, `log`, `blame`, workspaces, commits, tags and `rebase`, against any server with protocol v2 and partial fetch. The GitHub-only parts are listed above.

**Can I use git in a gion workspace?**
For local tools, yes: a workspace has a `.git` that pre-commit, `git diff`, `git status` and `git ls-files` work with (see [Workspaces](workspaces.md#pre-commit-and-other-git-based-tools)). It holds one commit and no history (`HEAD` is the upstream commit; in a sparse one git reads a local snapshot of the workspace's files in its place), so tools that need real commits or tags (like GoReleaser releases or `git describe`) need a real clone, and you push with `gion commit`, not `git push`.

**Is it safe to run many gion processes at once?**
Yes. The cache is lock-free (write a temp file, rename it), and pushes are compare-and-swap.

**Where are my credentials stored?**
Nowhere. gion borrows `GITHUB_TOKEN`, `gh auth token` or your git credential helper on each run.
