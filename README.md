<div align="center">

# gion

**A small asymmetric git client for agents and CI.**<br>
Read, search and push to any git repo without cloning it.

[![CI](https://github.com/baz-scm/gion/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/baz-scm/gion/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/baz-scm/gion?include_prereleases&sort=semver)](https://github.com/baz-scm/gion/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/baz-scm/gion.svg)](https://pkg.go.dev/github.com/baz-scm/gion)
[![License](https://img.shields.io/github/license/baz-scm/gion)](LICENSE)
![Status: beta](https://img.shields.io/badge/status-beta-orange)

<sub>*gion* (**GI-un**, "gi" as in **gi**t) = **g**it + sc**ion**, after the Scion design described [below](#why).</sub>

</div>

gion keeps only what a task needs and pulls files, trees and commits from the remote on demand. It speaks the standard git wire protocol, so it works against GitHub and other git servers as they are today.

```sh
gion cat chromium/chromium HEAD:base/strings/string_util.h   # one file from a 66 GB repo, no clone
gion grep chromium/chromium 'TrimWhitespaceASCII\('             # search all 509k files, exact lines
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

This puts `gion` in `$(go env GOPATH)/bin` (usually `~/go/bin`). If your shell says `command not found`, add that directory to your `PATH`:

```sh
echo 'export PATH="$PATH:$(go env GOPATH)/bin"' >> ~/.zshrc && source ~/.zshrc
```

Or download a prebuilt binary for Linux or macOS from [Releases](https://github.com/baz-scm/gion/releases). Run `gion version` to see what you have.

gion keeps a cache in `~/.gion` (see [How it works](#how-it-works)). It includes a `CACHEDIR.TAG`, so backup tools such as restic and borg skip it; on macOS, exclude it from Time Machine with `tmutil addexclusion ~/.gion`.

Credentials are borrowed, never stored. gion tries, in order: the `GITHUB_TOKEN` environment variable (for github.com only; in GitHub Actions use the built-in `secrets.GITHUB_TOKEN`), then `gh auth token`, then your `git credential` helper. With none of them, gion reads public repos anonymously; pushes and private repos need one.

## For agents

gion ships an [agent skill](skills/gion/SKILL.md) that teaches coding agents to read, search and push without cloning. The binary carries it, so the skill always matches your gion version:

```sh
gion skill install            # ~/.claude/skills (Claude Code) and ~/.agents/skills (Codex, Cursor, Amp, ...)
gion skill install --project  # the same, inside the current project
```

Rerun it after upgrading gion. Or install it with the [skills](https://skills.sh) CLI, which tracks `main`:

```sh
npx skills add baz-scm/gion
```

Agents without the skill can run `gion help --md`, which prints the same guide.

## Commands

`<remote>` is a URL or a GitHub `owner/repo`. A `<rev>` is a branch, tag, `HEAD` or commit id, optionally with `~N`/`^N` parent steps (`main~2`). Short commit ids work on GitHub (expanded through its API; the git protocol only takes full ids). Run `gion help` for the full usage text.

| Command | What it does |
| --- | --- |
| `ls-refs <remote> [--prefix p]` | List refs (protocol v2). The output matches `git ls-remote --symref`. |
| `cat <remote> <rev>:<path>` | Print a file's raw bytes. |
| `ls <remote> [<rev>[:<dir>]]` | List a directory. The output matches `git ls-tree`. |
| `find <remote> [<rev>] <pattern>` | Search file paths across the whole repo. A substring, or a glob (`*`, `?`, `**`, `[...]`); case-insensitive unless the pattern has uppercase. |
| `grep <remote> [<rev>] <regex> [--path p]...` | Search file contents and print `path:line:text`. See [Search](#search). |
| `diff <remote> [<rev>] [<rev>] [--path p]...` | What changed between two commits (`a b` or `a..b`), or what one commit changed (default `HEAD`). Prints a unified diff; `--name-only` / `--name-status` for just the paths. Only the trees and files that differ are fetched, so it's fast on any repo size. |
| `clone <remote> <dir> [--path p]...` | Create a workspace with only the chosen paths (default: everything) at one commit. It has no history and no `.git` directory. |
| `status` | Inside a workspace: added, modified and deleted files. Untracked files matched by `.gitignore` are skipped. |
| `diff [--name-only \| --name-status]` | Inside a workspace: your edits as a unified diff. |
| `add <path>...` | Inside a workspace: widen it with more files or directories at the workspace's commit. A path that doesn't exist yet just joins the workspace, so new files there are committed. |
| `sync` | Inside a workspace: bring files up to date with the branch. Files you've edited are kept; if upstream changed them too, nothing is written and the conflicting paths are listed. |
| `switch <branch>` | Inside a workspace: move to an existing branch. Only files that differ are rewritten; your edits are kept (nothing changes if they conflict). Later commits go to that branch. |
| `commit -m <msg> [--branch b \| --to b]` | Inside a workspace: push your changes. By default the first commit creates a branch (`gion/<timestamp>`, or `--branch b`) and later commits update it. `--to b` pushes onto an existing branch instead. If someone else pushed to the branch on other files, their changes are written to disk too. |
| `gc [--max SIZE]` | Trim the cache in `~/.gion` to its size cap now and print its size. |
| `skill install [--project]` | Install the agent skill for Claude Code and agents that read `.agents/skills`. |
| `tag <remote> <name> [<rev>] [-m msg]` | Create a tag on the remote, annotated if `-m` is given. Refuses to move an existing tag. |
| `commit <remote> -m <msg> <path>=<file>... [--rm p]` | Commit without any workspace. `--to <branch>` updates an existing branch instead of creating one; `--orphan` makes a root commit (e.g. in an empty repo). |

Add `--md` for Markdown output that agents can read.

## Branches

Every read command takes a branch, tag, full ref or full commit id as `<rev>`, and branches share everything they have in common in the cache, so reading a second branch only fetches what differs.

A workspace has one base and one branch it pushes to:

```sh
gion clone baz-scm/baz ./baz --path demos            # base: the default branch
cd baz
gion switch feature/x                                # move to an existing branch
$EDITOR demos/app.md
gion commit -m "update app"                          # pushes onto feature/x
gion commit -m "hotfix" --to main                    # or onto another existing branch
gion commit -m "try" --branch my-idea                # or create a new one
```

Pushing onto a branch is compare-and-swap: if the branch moved and the other commits changed different files, gion builds on the new tip and writes those files to disk; if they changed a file you edited, it refuses instead of overwriting.

## Search

`find` and `grep` work on a **path index**: every file path at a commit, built from the trees alone (no file contents). The first build for a repo fetches every tree once (for chromium: 45k trees, 510k paths, a 15 MB index); after that, moving to a newer commit fetches only the trees that changed. Searching a cached commit costs one ref lookup.

`grep` then picks which files to read:

- **Few enough files** (after `--path` filters, at most `--max-files`, default 5000): fetch them all (once; they stay cached) and scan locally.
- **Too many**: ask GitHub code search which files contain the longest literal in your regex, fetch only those, and scan them with the real regex. Results are exact lines, but GitHub only indexes the default branch and skips some files (very large ones, for example), so gion prints a note saying where candidates came from. A pattern with no 3+ character literal (like a top-level `a|b`) can't use this; narrow it with `--path`.

`--local` forces a full scan and `--forge` forces code search.

## Performance

Median of 3 runs, measured 2026-10-15 on an Apple M2 Pro (16 GB, macOS 26.6) over a home connection to github.com, gion v0.1.0-dev vs git 2.54. **Cold** means an empty gion cache; **warm** reuses it. Refs are never cached, so every gion run still makes at least one round trip. Each git baseline is the fastest git command that gets the same answer.

| Task | gion, cold | gion, warm | git |
| --- | --- | --- | --- |
| Resolve `HEAD` (chromium) | 0.69 s | (not cached) | 0.93 s: `ls-remote` |
| Read one file 3 dirs deep (chromium, 66 GB repo) | 2.68 s | **0.74 s** | 4.42 s: `clone --depth 1 --filter=blob:none` + `show` |
| Find a path among all 510k files (chromium) | 19.60 s | **1.10 s** | 4.27 s: `clone --depth 1 --filter=blob:none` + `ls-tree` |
| Grep all 510k files (chromium, via code search) | – | **3.49 s** | needs a full checkout first |
| Grep all 3.5k files (baz, local scan) | 4.68 s | **0.81 s** | 2.36 s: `clone --depth 1` + `git grep` |
| Clone one directory (baz `demos/`) | **1.27 s** | **0.71 s** | 2.69 s: `clone --depth 1 --filter=blob:none --sparse` |
| Clone the whole repo (baz, 3.5k files) | 4.73 s | **1.50 s** | 2.20 s: `clone --depth 1` |

Commits push only the new objects: a one-file commit on baz sent 3–4 objects and took 1.4–2.9 s end to end.

**Where git is faster.** A cold full clone and the first path index of a huge repo. The network isn't the bottleneck: gion stores every object as its own file in the cache (chromium's 45k trees take 181 MB as loose objects versus 18 MB in git's pack) and fetches a clone's files in sequential batches. Single-request clones, keeping received packs as-is, and parallel fetches are planned.

Reproduce with [`bench/bench.sh`](bench/bench.sh) (`bench/bench.sh 5` for 5 runs per case). Numbers depend heavily on your network and on GitHub's load at the time.

## How it works

- **Reads fetch only what they need.** A ref is resolved on the server, then exactly the commit, the trees along the path, and the blob are fetched by object id. Each fetch uses protocol v2 with filter `tree:0` and depth 1, so nothing else comes along.
- **Refs are never cached.** Each operation resolves a ref once and then uses that commit id. Everything after that is immutable, so it's safe to cache.
- **One shared cache.** All invocations on a machine share a content-addressed cache in `~/.gion` (objects in git's loose-object format, plus path indexes). Writes go to a temp file and are renamed into place, so concurrent processes need no locks. It's capped at 1 GB by default (`GION_CACHE_MAX=5GB`, or `0` for no limit): about once an hour gion trims it to 80% of the cap, least recently used first, never touching anything used in the last 10 minutes. `gion gc` trims it now and shows its size. Deleting `~/.gion` is always safe; it refills on demand. `GION_CACHE=/path/objects` moves it.
- **Commits cost the size of the change, not the repo.** A commit is built from path changes: only the trees on the changed paths are fetched, and only the new objects are pushed.
- **Commits are compare-and-swap.** `--to` and workspace commits update a branch only if it is still where we last saw it. If it moved and the other commit touched different paths, gion rebuilds on the new tip and retries. If it touched the same paths, gion stops with a conflict rather than overwrite.
- **No git binary needed.** Everything goes through [go-git](https://github.com/go-git/go-git). The tests use the `git` CLI only to run a local test server.

Set `GION_TRACE=1` to log each request's protocol lines and timings to stderr.

## Limitations

- **No merging.** `sync` and `commit` refuse when upstream changed the same files you edited; resolve by committing elsewhere or undoing the edit.
- **Commits are unsigned.**
- **Whole-repo `grep` on big repos depends on GitHub code search**, so it covers the default branch's index only, is rate-limited (about 10 searches a minute), and isn't available on other forges yet.
- **Full-repo clones are slower than `git clone --depth 1`.** Fetching only the paths you need is the intended use.

## Development

```sh
go test ./...     # integration tests need `git` on PATH
go build -o bin/gion .
gion version
```

## Releasing

Tag a commit on `main`, with gion:

```sh
gion tag baz-scm/gion v0.2.0 -m "v0.2.0"     # tags main's HEAD; add a rev to tag another commit
```

The release workflow runs the full CI on that exact commit, checks the tag is on `main`, then GoReleaser publishes binaries and checksums to GitHub Releases. If CI fails, nothing is published. `go install …@latest` picks up the new tag through the Go module proxy, which caches tags permanently, so never move or delete a pushed tag; tag a new version instead. (`gion tag` only creates tags and refuses an existing name.) Every CI run also does a GoReleaser dry run, so a broken release config fails before tag time.

## License

[Apache 2.0](LICENSE)
