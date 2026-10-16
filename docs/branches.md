# Branches & rebase

## Reading branches

Every read command takes a branch, tag, full ref or commit as `<rev>`. Branches share whatever they have in common in the cache, so reading a second branch only fetches what differs.

## A workspace's branch

A workspace has one base commit and one branch it pushes to.

```sh
gion clone baz-scm/baz ./baz --path demos     # base: the default branch
cd baz
gion switch feature/x                         # move to an existing branch
$EDITOR demos/app.md
gion commit -m "update app"                   # pushes onto feature/x
gion commit -m "hotfix" --to main             # or onto another existing branch
gion commit -m "try" --branch my-idea         # or create a new one
```

`switch` rewrites only the files that differ between the two branches and keeps your edits; if the target branch changed a file you edited, it changes nothing and lists it.

## Compare-and-swap pushes

gion never force-pushes a commit. A push says "move this branch from the commit I saw to my new one", and the server refuses if the branch moved. When that happens:

- if the other commits changed **different files**, gion rebuilds your commit on the new tip and retries;
- if they changed **a file you changed**, it stops with a conflict instead of overwriting their work.

Without a workspace, `gion commit <remote> -m msg --to <branch> path=localfile...` works the same way.

## Rebase

`gion rebase` replays your branch's own commits onto a newer base and pushes the result.

```sh
gion rebase              # onto the default branch
gion rebase release/2.1  # onto another branch
```

The workspace must have no pending edits and be at its branch's tip (`gion sync` first if it isn't). Each commit is replayed with a three-way, line-by-line merge: changes to different lines combine; identical changes are kept once; commits whose changes are already upstream are dropped; merge commits are dropped, as in git.

Nothing is pushed until every commit has been replayed. Then the branch moves in one compare-and-swap push from its old tip to the new chain, so if someone pushed to your branch during the rebase, the push is refused and nothing is lost.

### Conflicts

When a commit can't be merged, the rebase stops and lists the files. Content conflicts look like this (diff3 style, with the original in the middle):

```text
<<<<<<< main
import { AsyncLocalStorage } from "node:async_hooks"
import tracer from "./tracer.js"
||||||| base b0cb5bb68242
import tracer from "./tracer.js"
=======
import tracer from "./tracer.js" // traced
>>>>>>> 8c75a53b62e8 test: tag the tracer import
```

- `<<<<<<<` is the new base (here `main`), `|||||||` is the version before either change, and `>>>>>>>` is the commit being replayed.
- Edit the file to its final content, or delete it to accept a deletion. Check with `gion diff`.
- `gion rebase --continue` takes your resolution (it refuses while markers remain) and carries on.
- `gion rebase --skip` drops this commit; `gion rebase --abort` puts every file back as it was.

Other kinds: *modify/delete* (your commit deletes a file the new base changed; the base's version is on disk, delete it to keep the deletion), *delete/modify* (the reverse), and *binary* (changed on both sides; the base's version is on disk).

A conflicting file outside a sparse workspace's paths is written anyway and joins the workspace. While a rebase is stopped, `commit`, `sync`, `switch` and `add` refuse, and `gion status` says so.

### Without a workspace

```sh
gion rebase baz-scm/baz my-branch            # onto the default branch
gion rebase baz-scm/baz my-branch --onto release/2.1
```

This succeeds only when there are no conflicts. Otherwise nothing is pushed and gion prints the workspace commands that let you resolve them.

## No merge

There's no `gion merge`. Bring a branch up to date with `rebase`, and merge pull requests on the forge.
