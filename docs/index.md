# gion

<sub>*gion* (**GI-un**, "gi" as in **gi**t) = **g**it + sc**ion**</sub>

**Asymmetric git client for agents.** Skip the heavy network on large repos, get only what you need.

<div class="bench" markdown>

| Task | git | gion (cold) | gion (warm) |
| --- | --- | --- | --- |
| Read one file | 4.42 s | 2.68 s | **0.74 s** (6x) |
| Find a path | 4.27 s | 19.60 s | **1.10 s** (3.9x) |
| Check out a folder | 7.87 s | 3.24 s | **1.11 s** (7.1x) |
| Grep all files | all files on disk first | 19.63 s | **3.49 s** |

<sub>Measured on chromium (66 GB, 510k files). [Full benchmarks](performance.md).</sub>

</div>

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

To push, use a repo you can write to: `gion commit -m "my change" --branch my-fix` ([Workspaces](workspaces.md)).

## Where next

- [Getting started](getting-started.md): install, credentials, first commands
- [CI](ci.md): check out one directory or a PR's changed files in GitHub Actions
- [Agents](agents.md): install the gion skill so your agent uses gion instead of `git clone`
- [Command reference](reference.md): every command and flag
