<div align="center">

# gion

**A small asymmetric git client for agents and CI.**<br>
Read, search, diff and push to any git repo without cloning it.

[![CI](https://github.com/baz-scm/gion/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/baz-scm/gion/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/baz-scm/gion?include_prereleases&sort=semver)](https://github.com/baz-scm/gion/releases)
[![Downloads](https://img.shields.io/github/downloads/baz-scm/gion/total)](https://github.com/baz-scm/gion/releases)
[![Docs](https://img.shields.io/badge/docs-baz--scm.github.io%2Fgion-blue)](https://baz-scm.github.io/gion/)
[![Go Reference](https://pkg.go.dev/badge/github.com/baz-scm/gion.svg)](https://pkg.go.dev/github.com/baz-scm/gion)
[![License](https://img.shields.io/github/license/baz-scm/gion)](LICENSE)
![Status: beta](https://img.shields.io/badge/status-beta-orange)

<sub>*gion* (**GI-un**, "gi" as in **gi**t) = **g**it + sc**ion**, after the Scion design described [below](#why).</sub>

</div>

gion keeps only what a task needs and fetches files, trees and commits from the remote on demand. It speaks the standard git wire protocol, so it works with GitHub and other git servers as they are today.

```sh
gion cat chromium/chromium HEAD:base/strings/string_util.h   # one file from a 66 GB repo, ~1 s
gion grep chromium/chromium 'TrimWhitespaceASCII\('             # search all 510k files
gion log chromium/chromium --path base/strings -n 5             # history, no clone
gion clone baz-scm/baz ./baz --path demos                     # sparse workspace, no history
cd baz && $EDITOR demos/x.md && gion commit -m "edit" --branch my-fix
```

> [!WARNING]
> **Beta.** Commands, flags and on-disk formats may change between releases without a migration path. Try it on branches.

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
