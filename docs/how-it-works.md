# How it works

## Reads fetch exactly what they need

A ref is resolved on the server (protocol v2 `ls-refs`), then the commit, the folder listings (trees) along the path and the file contents (blob) are fetched by object id. Each fetch uses filter `tree:0` and depth 1, so nothing else comes along. Whole-directory operations fetch all trees under a directory in one request (`blob:none`), then the files they need in batches.

History is fetched separately: commits only, up to 256 generations per request, so `log`, `blame` and `rebase` walk history without downloading any trees they don't look at.

## Refs are never cached

Each operation resolves a ref once and then works on that commit id. Everything after that is immutable, so it's safe to cache; refs are not, so they're always asked of the server.

## The cache

All gion processes on a machine share a content-addressed cache in `~/.gion`: objects in git's loose-object format, plus path indexes for search. Writes go to a temp file and are renamed into place, so concurrent processes need no locks.

| Setting | Effect |
| --- | --- |
| `GION_CACHE_MAX=5GB` | size cap (default 1 GB; `0` means no limit) |
| `GION_CACHE=/path/objects` | move the object cache |
| `gion gc [--max SIZE]` | trim now and print the size |

About once an hour gion trims the cache to 80% of the cap, least recently used first, never touching anything used in the last 10 minutes. Deleting `~/.gion` is always safe; it refills on demand.

## Writes cost the size of the change

A commit is built from path changes: only the trees on changed paths are fetched, and only new objects are pushed (a one-file commit sends 3–4 objects). Pushes are compare-and-swap; see [Branches & rebase](branches.md#compare-and-swap-pushes).

## Forge APIs, where git can't

A few things have no git-protocol equivalent, so gion uses the forge's API when the remote is on GitHub:

| Feature | GitHub API | Elsewhere |
| --- | --- | --- |
| short commit ids | commits API | full ids only |
| `grep` across huge repos | code search | local scan (`--path` to narrow) |
| `log --path` | commits API | local history walk |
| `blame` | GraphQL blame | local history walk |

## No git binary

Everything goes through [go-git](https://github.com/go-git/go-git) and gion's own protocol code. The tests use the `git` CLI only to run a local test server and to check gion's answers against git's.

## Debugging

`GION_TRACE=1` logs each request's protocol lines and timings to stderr.

## Background

Anthropic's [Redesigning source control](https://www.anthropic.com/engineering/redesigning-source-control) describes Scion, an internal system that makes client and server deliberately unequal: the server holds the history, and the client keeps a small cache and fetches immutable objects by id. Scion replaces git to get there. gion applies the client half of that idea to existing git servers.

gion is an independent project. It is not affiliated with or endorsed by Anthropic.
