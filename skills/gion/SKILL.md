---
name: gion
description: Read, search, diff and push to remote git repositories (GitHub owner/repo or any git URL) without cloning them, using the gion CLI. Use whenever a task involves a repository that isn't already checked out, or only part of a big repo is needed — instead of git clone.
---

# gion: work with git repos without cloning them

gion reads files, trees and commits from the remote on demand and pushes commits straight to it. There is no `.git` and no history on disk. Prefer it over `git clone` for any repo you don't already have checked out. If the repo is already checked out locally and up to date, just use that checkout and git.

Add `--md` to every command: output is Markdown made for agents.

## Setup

- Install: `go install github.com/baz-scm/gion@latest` (binary lands in `$(go env GOPATH)/bin`), then `gion skill install` to keep this skill in sync with the binary.
- Auth (only needed for private repos and pushes): `gh auth login`, or set `GITHUB_TOKEN`, or a git credential helper. gion never stores credentials.
- `<remote>` is `owner/repo` (GitHub) or any git URL. `<rev>` is a branch, tag, `HEAD`, a commit id (short ids work on GitHub), with optional `~N`/`^N` (`main~2`).

## Read and search: no workspace needed

```sh
gion ls   owner/repo HEAD:src --md              # list a directory
gion cat  owner/repo HEAD:src/app.go            # raw file bytes
gion find owner/repo 'handler' --md             # search paths in the whole repo (substring or glob)
gion grep owner/repo 'func New\w+\(' --md       # search contents in the whole repo
gion grep owner/repo 'TODO' --path src --md     # limit to paths
gion diff owner/repo main~1 main --md           # what changed between commits
gion diff owner/repo HEAD --name-status         # what the last commit changed
gion log  owner/repo main -n 20 --md            # recent commits
gion log  owner/repo --path src/api --md        # commits that changed a path
gion blame owner/repo HEAD:src/app.go -L 40,60 --md   # who last changed these lines
gion ls-refs owner/repo                         # branches and tags
```

Start here: `ls`/`find` to orient, `grep` to locate, `cat` to read. This costs seconds even on huge repos.

## Edit and push

**Few files, no workspace:** write the new contents locally, then

```sh
gion commit owner/repo -m "msg" --branch my-fix src/a.go=./a.go --rm src/old.go --md
```

`--branch b` creates `b` from `HEAD` (`--onto <rev>` for another base). `--to b` updates an existing branch.

**Real coding: a sparse workspace.** Clone only the directories the task touches. The files in their parent directories (root included) come along, so `.gitignore`, `pyproject.toml`, `package.json`, `go.mod`, `conftest.py` and lint configs are there; edits to them are committed, new files next to them are not unless you `gion add` them.

```sh
gion clone owner/repo ./ws --path src/api --path go.mod --md
cd ws
# edit with your normal file tools
gion status --md                 # what changed
gion diff --md                   # your edits as a diff (gion diff <path> for one file or dir)
gion add src/shared --md         # need another directory? widen the workspace
gion commit -m "msg" --branch my-fix --md      # first push creates the branch
gion commit -m "more" --md                     # later pushes update it
gion sync --md                   # pull upstream changes into unedited files
gion switch other-branch --md    # move to an existing branch, keeping edits
gion log --md                    # history of what's checked out; gion blame <file> --md
gion rebase --md                 # replay your branch onto the default branch and push it
```

**Rebase conflicts.** `gion rebase` stops on a conflict and lists the files. Each has markers: `<<<<<<< <new base>` (upstream's version), `||||||| base` (before either change), `=======`, then `>>>>>>> <commit>` (your change). Edit the file to the final content, keeping both intents where they don't contradict (or delete the file to accept a deletion), check with `gion diff --md`, then `gion rebase --continue --md`. `--skip` drops that commit; `--abort` restores everything. Nothing is pushed until it finishes, and the push is compare-and-swap. Without a workspace, `gion rebase owner/repo my-branch --md` works when there are no conflicts.

Pushes are compare-and-swap: if someone else pushed to the branch on other files, gion builds on top and writes their files to disk; if they touched your files, it stops and lists them. The `--md` output of a new branch includes a link to open a PR.

## Rules

- Never `git clone` or `gion clone` without `--path` just to read or search: use `cat`, `find`, `grep`.
- Inside a sparse workspace, your file tools only see the cloned paths. Search the rest of the repo with `gion grep <remote>` / `gion find <remote>`, and `gion add` what you need to edit.
- Push to a branch, not the default branch, unless told to.
- To run tests in a workspace, `gion add` the test directories and the code they import. If that's most of the repo, push the branch and let CI run them.
- Pushes can't be undone by force-push: check `gion status --md` before each commit so ignored or generated files don't slip in.
- `gion` has no merge: bring a branch up to date with `gion rebase`. If a conflict's right resolution isn't clear from the code, ask the user instead of guessing.
- Run `gion help` for every flag.
