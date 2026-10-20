<div align="center">

# gion

<sub>*gion* (**GI-un**, "gi" as in **gi**t) = **g**it + sc**ion**</sub>

**Asymmetric git client for agents.**<br>
Skip the heavy network on large repos, get only what you need.

[![CI](https://github.com/baz-scm/gion/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/baz-scm/gion/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/baz-scm/gion?include_prereleases&sort=semver)](https://github.com/baz-scm/gion/releases)
[![Downloads](https://img.shields.io/github/downloads/baz-scm/gion/total)](https://github.com/baz-scm/gion/releases)
[![Docs](https://img.shields.io/badge/docs-baz--scm.github.io%2Fgion-blue)](https://baz-scm.github.io/gion/)
[![Go Reference](https://pkg.go.dev/badge/github.com/baz-scm/gion.svg)](https://pkg.go.dev/github.com/baz-scm/gion)
[![License](https://img.shields.io/github/license/baz-scm/gion)](LICENSE)
![Status: beta](https://img.shields.io/badge/status-beta-orange)

</div>

| Task | git | gion (cold) | gion (warm) |
| --- | --- | --- | --- |
| Read one file | 4.42 s | 2.68 s | **0.74 s** (6x) |
| Find a path | 4.27 s | 19.60 s | **1.10 s** (3.9x) |
| Check out a folder | 7.87 s | 3.24 s | **1.11 s** (7.1x) |
| Grep all files | all files on disk first | 19.63 s | **3.49 s** |

<sub>Measured on chromium (66 GB, 510k files). [Full benchmarks](https://baz-scm.github.io/gion/performance/).</sub>

## Why

Git assumes every client keeps a full copy and can work offline. Agents and CI jobs are online all the time, usually read a few files, search a bit and push one change, and often run many at once on one machine. For them, a full clone is mostly waste. gion fetches files, trees and commits from the remote only when a task asks for them:

- **Fast starts:** no clone step. Read, search, diff and log any repo right away.
- **Small footprint:** a workspace holds only the paths you ask for. History stays on the server, so many agents can share one machine.
- **Full write path:** commit, push to branches and rebase with conflict resolution. Only new objects are sent.
- **Works with your server today:** gion speaks the standard git wire protocol, so GitHub and other git servers need no setup.

```sh
# Read Chromium's README from a 66 GB repo (~1 s)
gion cat chromium/chromium HEAD:README.md
```

```sh
# Search all 510k files (~3 s)
gion grep chromium/chromium 'Hello, world'
```

```sh
# The last 5 changes to the README, without a clone (~5 s)
gion log chromium/chromium --path README.md -n 5
```

```sh
# Check out only the docs folder (~5 s)
gion clone chromium/chromium ./chromium --path docs
```

```sh
# Edit a file and see what changed (instant)
cd chromium && echo "hello" >> docs/README.md && gion diff
```

To push, use a repo you can write to: `gion commit -m "my change" --branch my-fix` ([Workspaces](https://baz-scm.github.io/gion/workspaces/)).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/baz-scm/gion/main/install.sh | sh   # macOS, Linux
go install github.com/baz-scm/gion@latest                                     # or with Go
```

gion borrows your `gh` login, `GITHUB_TOKEN` or git credential helper; public repos need none.

- **CI:** in GitHub Actions, `uses: baz-scm/gion-action/checkout@v1` for a sparse checkout, or `baz-scm/gion-action/changed@v1` for a PR's changed files ([CI guide](https://baz-scm.github.io/gion/ci/)).
- **Agents:** run `gion skill install` so your coding agent uses gion instead of `git clone` ([agents guide](https://baz-scm.github.io/gion/agents/)).

## Commands

| Read anything | Work in a workspace | Write |
| --- | --- | --- |
| `cat`, `ls`, `ls-refs` | `clone --path`, `add` | `commit` (workspace or remote) |
| `find`, `grep` (whole repo) | `status`, `diff` | `rebase` (with conflict resolution) |
| `diff`, `log`, `blame` | `sync`, `switch` | `tag` |

Every command takes `--md` for agent-friendly output and `-h` for its flags. See the [command reference](https://baz-scm.github.io/gion/reference/).

## Docs

**[baz-scm.github.io/gion](https://baz-scm.github.io/gion/)**: [getting started](https://baz-scm.github.io/gion/getting-started/), [CI](https://baz-scm.github.io/gion/ci/), [agents](https://baz-scm.github.io/gion/agents/), [workspaces](https://baz-scm.github.io/gion/workspaces/), [branches & rebase](https://baz-scm.github.io/gion/branches/), [search & history](https://baz-scm.github.io/gion/search-and-history/), [command reference](https://baz-scm.github.io/gion/reference/), [how it works](https://baz-scm.github.io/gion/how-it-works/), [performance](https://baz-scm.github.io/gion/performance/), [limitations & FAQ](https://baz-scm.github.io/gion/limitations/).

Working on gion itself? See [AGENTS.md](AGENTS.md).

## License

[Apache 2.0](LICENSE)
