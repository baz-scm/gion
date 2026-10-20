<div align="center">

# gion

**Asymmetric git client for agents.**<br>
Skip the heavy network on large monorepos, get only what you need.

[![CI](https://github.com/baz-scm/gion/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/baz-scm/gion/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/baz-scm/gion?include_prereleases&sort=semver)](https://github.com/baz-scm/gion/releases)
[![Downloads](https://img.shields.io/github/downloads/baz-scm/gion/total)](https://github.com/baz-scm/gion/releases)
[![Docs](https://img.shields.io/badge/docs-baz--scm.github.io%2Fgion-blue)](https://baz-scm.github.io/gion/)
[![Go Reference](https://pkg.go.dev/badge/github.com/baz-scm/gion.svg)](https://pkg.go.dev/github.com/baz-scm/gion)
[![License](https://img.shields.io/github/license/baz-scm/gion)](LICENSE)
![Status: beta](https://img.shields.io/badge/status-beta-orange)

<sub>*gion* (**GI-un**, "gi" as in **gi**t) = **g**it + sc**ion**, after the Scion design described [below](#why).</sub>

</div>

| Task | git | gion, cold cache | gion, warm cache |
| --- | --- | --- | --- |
| Read one file (chromium, 66 GB) | 4.42 s | 2.68 s | **0.74 s** |
| Find a path among 510k files (chromium) | 4.27 s | 19.60 s | **1.10 s** |
| Check out one directory (baz) | 2.69 s | 1.27 s | **0.71 s** |
| Grep all 510k files (chromium) | needs a full checkout | not measured | **3.49 s** |

Git times use the fastest git command that gets the same answer. [Full benchmarks](https://baz-scm.github.io/gion/performance/).

Agents and CI jobs usually read a few files, search a bit and push one change, but a clone makes them download the whole repo first. gion fetches files, trees and commits from the remote only when a task asks for them:

- **Fast starts:** no clone step. Read, search, diff and log any repo right away.
- **Small footprint:** a workspace holds only the paths you ask for. History stays on the server, so many agents can share one machine.
- **Full write path:** commit, push to branches and rebase with conflict resolution. Only new objects are sent.
- **Works with your server today:** gion speaks the standard git wire protocol, so GitHub and other git servers need no setup.

```sh
gion cat chromium/chromium HEAD:base/strings/string_util.h   # one file from a 66 GB repo, ~1 s
gion grep chromium/chromium 'TrimWhitespaceASCII\('             # search all 510k files
gion log chromium/chromium --path base/strings -n 5             # history, no clone
gion clone baz-scm/baz ./baz --path demos                     # sparse workspace, no history
cd baz && $EDITOR demos/x.md && gion commit -m "edit" --branch my-fix
```

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/baz-scm/gion/main/install.sh | sh   # macOS, Linux
go install github.com/baz-scm/gion@latest                                     # or with Go
```

In GitHub Actions: `uses: baz-scm/gion-action/checkout@v1` for a sparse checkout, or `baz-scm/gion-action/changed@v1` for a PR's changed files ([CI guide](https://baz-scm.github.io/gion/ci/)).

gion borrows your `gh` login, `GITHUB_TOKEN` or git credential helper; public repos need none. Using a coding agent? Run `gion skill install` so it knows how to use gion ([more](https://baz-scm.github.io/gion/agents/)).

## Commands

| Read anything | Work in a workspace | Write |
| --- | --- | --- |
| `cat`, `ls`, `ls-refs` | `clone --path`, `add` | `commit` (workspace or remote) |
| `find`, `grep` (whole repo) | `status`, `diff` | `rebase` (with conflict resolution) |
| `diff`, `log`, `blame` | `sync`, `switch` | `tag` |

Every command takes `--md` for agent-friendly output and `-h` for its flags. See the [command reference](https://baz-scm.github.io/gion/reference/).

## Why

Git assumes every client keeps a full copy and can work offline. Coding agents and CI jobs are online all the time, usually read a handful of files and write one, and often run many at once. For them, a full clone is mostly waste.

Anthropic's [Redesigning source control](https://www.anthropic.com/engineering/redesigning-source-control) describes Scion, which makes client and server deliberately unequal: the server holds the history; the client keeps a small cache and fetches immutable objects by id. Scion replaces git to get there. gion applies the client half of that idea to existing git servers.

**gion is an independent project. It is not affiliated with or endorsed by Anthropic.**

## Docs

**[baz-scm.github.io/gion](https://baz-scm.github.io/gion/)**: [getting started](https://baz-scm.github.io/gion/getting-started/), [workspaces](https://baz-scm.github.io/gion/workspaces/), [branches & rebase](https://baz-scm.github.io/gion/branches/), [search & history](https://baz-scm.github.io/gion/search-and-history/), [agents](https://baz-scm.github.io/gion/agents/), [CI](https://baz-scm.github.io/gion/ci/), [how it works](https://baz-scm.github.io/gion/how-it-works/), [performance](https://baz-scm.github.io/gion/performance/), [limitations & FAQ](https://baz-scm.github.io/gion/limitations/).

Working on gion itself? See [AGENTS.md](AGENTS.md).

## License

[Apache 2.0](LICENSE)
