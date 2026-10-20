# Getting started

## Install

On macOS or Linux, no Go needed:

```sh
curl -fsSL https://raw.githubusercontent.com/baz-scm/gion/main/install.sh | sh
```

The script downloads the latest [release](https://github.com/baz-scm/gion/releases) binary for your OS and CPU, checks its checksum and installs it to `~/.local/bin`. Rerun it to upgrade.

| Variable | Effect |
| --- | --- |
| `GION_VERSION=v0.2.0` | install that release instead of the latest |
| `GION_INSTALL_DIR=/usr/local/bin` | install somewhere else |

With Go:

```sh
go install github.com/baz-scm/gion@latest   # lands in $(go env GOPATH)/bin
```

Check with `gion version`. If you use a coding agent, also run `gion skill install` (see [Agents](agents.md)). In GitHub Actions, use [gion-action](ci.md) instead.

## Credentials

Credentials are borrowed, never stored. gion tries, in order:

1. `GITHUB_TOKEN` (sent to github.com only)
2. `gh auth token` for the remote's host (this covers GitHub Enterprise)
3. your `git credential` helper

With none of them, gion reads public repos anonymously. Pushes and private repos need one.

## Naming things

- **`<remote>`** is a GitHub `owner/repo`, or any git URL (`https://…`, `ssh://…`, `git@host:path`, `git://…`).
- **`<rev>`** is a branch, tag, `HEAD`, full ref or commit id, with optional `~N` / `^N` parent steps (`main~2`, `HEAD^2`). Short commit ids work on GitHub, which expands them; the git protocol itself only accepts full ids.

## First commands

```sh
gion ls     baz-scm/gion HEAD:internal          # list a directory
gion cat    baz-scm/gion HEAD:go.mod            # print a file
gion find   baz-scm/gion workspace              # search paths
gion grep   baz-scm/gion 'func New\w+\('        # search contents
gion log    baz-scm/gion -n 5 --oneline         # recent commits
gion diff   baz-scm/gion main~1 main --stat     # what the last commit changed
```

Add `--md` to any command for Markdown output (what agents should use), and `-h` for its flags.

## The cache

gion keeps a content-addressed cache in `~/.gion`: objects it has fetched, and path indexes for search. It's capped at 1 GB (least recently used goes first) and shared by every gion process on the machine. Deleting it is always safe. See [How it works](how-it-works.md#the-cache) for the knobs.

The cache has a `CACHEDIR.TAG`, so backup tools like restic and borg skip it. On macOS, exclude it from Time Machine with `tmutil addexclusion ~/.gion`.
