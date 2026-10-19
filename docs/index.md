# gion

**A small asymmetric git client for agents and CI.** Read, search, diff and push to any git repo without cloning it.

gion keeps only what a task needs and fetches files, trees and commits from the remote on demand. It speaks the standard git wire protocol, so it works with GitHub and other git servers as they are today.

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
