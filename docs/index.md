# gion

**Asymmetric git client for agents.** Skip the heavy network on large monorepos, get only what you need.

| Task | git | gion, cold cache | gion, warm cache |
| --- | --- | --- | --- |
| Read one file (chromium, 66 GB) | 4.42 s | 2.68 s | **0.74 s** |
| Find a path among 510k files (chromium) | 4.27 s | 19.60 s | **1.10 s** |
| Check out one directory (baz) | 2.69 s | 1.27 s | **0.71 s** |
| Grep all 510k files (chromium) | needs a full checkout | not measured | **3.49 s** |

Git times use the fastest git command that gets the same answer. [Full benchmarks](performance.md).

Agents and CI jobs usually read a few files, search a bit and push one change, but a clone makes them download the whole repo first. gion fetches files, trees and commits from the remote only when a task asks for them:

- **Fast starts:** no clone step. Read, search, diff and log any repo right away.
- **Small footprint:** a workspace holds only the paths you ask for. History stays on the server, so many agents can share one machine.
- **Full write path:** commit, push to branches and rebase with conflict resolution. Only new objects are sent.
- **Works with your server today:** gion speaks the standard git wire protocol, so GitHub and other git servers need no setup.

```sh
gion cat chromium/chromium HEAD:base/strings/string_util.h   # one file from a 66 GB repo, ~1 s
gion grep chromium/chromium 'TrimWhitespaceASCII\('             # search all 510k files
gion log chromium/chromium --path base/strings -n 5             # history without a clone
gion clone baz-scm/baz ./baz --path demos                     # sparse workspace, no history
cd baz && $EDITOR demos/x.md && gion commit -m "edit" --branch my-fix
```

!!! warning "Beta"
    Commands, flags and on-disk formats (`.gion/`, the object cache) may change between releases without a migration path. Try it on branches.

## Why

Git assumes every client keeps a full copy and can work offline. Coding agents and CI jobs are online all the time, usually read a handful of files and write one, and often run many at once on the same machine. For them, a full clone is mostly waste.

Anthropic's [Redesigning source control](https://www.anthropic.com/engineering/redesigning-source-control) describes Scion, an internal system that makes client and server deliberately unequal: the server holds the history, and the client keeps a small cache and fetches immutable objects by id. Scion replaces git to get there. gion applies the client half of that idea to existing git servers.

The name: *gion* (**GI-un**, "gi" as in **gi**t) = **g**it + sc**ion**.

gion is an independent project. It is not affiliated with or endorsed by Anthropic.

## Where next

- [Getting started](getting-started.md): install, credentials, first commands
- [Workspaces](workspaces.md): edit part of a repo and push
- [Branches & rebase](branches.md): switch, push to branches, rebase and resolve conflicts
- [Search & history](search-and-history.md): `find`, `grep`, `diff`, `log`, `blame`
- [Agents](agents.md) and [CI](ci.md)
- [Command reference](reference.md)
