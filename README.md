# gion

A small asymmetric git client for always-online agents and CI. It keeps only what a task needs and pulls files, trees and commits from the remote on demand. It speaks the standard git wire protocol, so it works against GitHub and other git servers as they are today.

```sh
gion cat chromium/chromium HEAD:base/strings/string_util.h   # one file from a 66 GB repo, no clone
gion clone baz-scm/baz ./baz --path demos                     # sparse workspace, no history, no .git
cd baz && $EDITOR demos/x.md && gion commit -m "edit" --md    # push from the workspace
```

> [!WARNING]
> **Beta.** gion works against GitHub today, but commands, flags and the on-disk formats (`.gion/`, the object cache) may change between releases without a migration path. Try it on branches, not on anything you can't recreate.

## Why

Git assumes every client keeps a full copy and can work offline. Coding agents and CI jobs are online all the time. They usually read a handful of files and write one, and many of them run at once on the same machine. For them, a full clone is mostly waste.

Anthropic's [Redesigning source control](https://www.anthropic.com/engineering/redesigning-source-control) describes Scion, an internal system that makes the client and server deliberately unequal. The server holds the history. The client keeps a small cache and fetches immutable objects by id when it needs them. Scion replaces git to get there, and moving to it means changing review, CI and hosting together.

gion applies the client half of that idea to existing git servers.

**gion is an independent project. It is not affiliated with or endorsed by Anthropic.**

## Install

```sh
go install github.com/baz-scm/gion@latest
```

Credentials are borrowed, never stored. gion tries, in order: the `GION_TOKEN` environment variable, then `gh auth token`, then your `git credential` helper.

## Commands

`<remote>` is a URL or a GitHub `owner/repo`. Run `gion help` for the full usage text.

| Command | What it does |
| --- | --- |
| `ls-refs <remote> [--prefix p]` | List refs (protocol v2). The output matches `git ls-remote --symref`. |
| `cat <remote> <rev>:<path>` | Print a file's raw bytes. |
| `ls <remote> [<rev>[:<dir>]]` | List a directory. The output matches `git ls-tree`. |
| `clone <remote> <dir> [--path p]...` | Create a workspace with only the chosen paths (default: everything) at one commit. It has no history and no `.git` directory. |
| `status` | Inside a workspace: added, modified and deleted files. Untracked files matched by `.gitignore` are skipped. |
| `sync` | Inside a workspace: bring files up to date with the branch. Files you've edited are kept; if upstream changed them too, nothing is written and the conflicting paths are listed. |
| `commit -m <msg> [--branch b]` | Inside a workspace: push your changes. The first commit creates a branch (`gion/<timestamp>` by default); later commits update it. If someone else pushed to the branch on other files, their changes are written to disk too. |
| `commit <remote> -m <msg> <path>=<file>... [--rm p]` | Commit without any workspace. `--to <branch>` updates an existing branch instead of creating one; `--orphan` makes a root commit (e.g. in an empty repo). |

Add `--md` for Markdown output that agents can read.

## How it works

- **Reads fetch only what they need.** A ref is resolved on the server, then exactly the commit, the trees along the path, and the blob are fetched by object id. Each fetch uses protocol v2 with filter `tree:0` and depth 1, so nothing else comes along.
- **Refs are never cached.** Each operation resolves a ref once and then uses that commit id. Everything after that is immutable, so it's safe to cache.
- **One shared cache.** All invocations on a machine share a content-addressed cache in git's loose-object format (`$GION_CACHE`, or your user cache directory). Writes go to a temp file and are renamed into place, so concurrent processes need no locks.
- **Commits cost the size of the change, not the repo.** A commit is built from path changes: only the trees on the changed paths are fetched, and only the new objects are pushed.
- **Commits are compare-and-swap.** `--to` and workspace commits update a branch only if it is still where we last saw it. If it moved and the other commit touched different paths, gion rebuilds on the new tip and retries. If it touched the same paths, gion stops with a conflict rather than overwrite.
- **No git binary needed.** Everything goes through [go-git](https://github.com/go-git/go-git). The tests use the `git` CLI only to run a local test server.

Set `GION_TRACE=1` to log each request's protocol lines and timings to stderr.

## Limitations

- **No merging.** `sync` and `commit` refuse when upstream changed the same files you edited; resolve by committing elsewhere or undoing the edit.
- **Commits are unsigned.**
- **Searching file contents across a whole repo isn't supported yet.** The plan: index all file paths from the trees (about 18 MB for chromium), scan contents for scoped searches, and use the forge's code search for whole-repo searches.
- **Full-repo clones are slower than `git clone --depth 1`.** Fetching only the paths you need is the intended use.

## Development

```sh
go test ./...     # integration tests need `git` on PATH
go build -o bin/gion .
```

## License

[Apache 2.0](LICENSE)
