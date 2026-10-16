# Workspaces

A workspace is a directory with the files you chose at one commit, plus `.gion/state.json`. There is no `.git` and no history; the remote branch is the only source of truth.

```sh
gion clone baz-scm/baz ./baz --path services/api --path libs/auth
cd baz
$EDITOR services/api/handler.py
gion status
gion diff
gion commit -m "fix handler" --branch my-fix
```

## What a sparse clone contains

- Every file under each `--path` (a directory or a single file). Without `--path` you get the whole repo.
- The files directly inside each **parent directory** of those paths, the root included. That's where tools look for config, so `.gitignore`, `pyproject.toml`, `package.json`, `Cargo.toml`, `go.mod`, `conftest.py` and lint configs are there without naming them.

Edits to those parent-directory files are committed. New files you create next to them (scratch notes, logs) are not, unless you `gion add` them.

## Widening

```sh
gion add libs/shared          # fetch it at the workspace's commit and track it
gion add services/new-thing   # doesn't exist yet: it just joins the workspace
```

`add` never overwrites a file already on disk that differs from the repo; it lists those and changes nothing.

## Status, diff and .gitignore

`gion status` lists added, modified and deleted files inside the workspace's paths. Files outside them are ignored, so a sparse workspace never reports the rest of the repo as deleted. Untracked files matched by `.gitignore` (including nested ones and your global excludes file) are skipped; tracked files are always checked, as in git.

`gion diff [<path>...]` shows your edits as a unified diff.

## Committing

```sh
gion commit -m "msg" --branch my-fix   # first commit: create the branch
gion commit -m "more"                  # later commits update it
gion commit -m "hotfix" --to main      # push onto another existing branch
```

The first commit creates a branch (`gion/<timestamp>` if you don't name one). Pushes are compare-and-swap: if someone pushed to the branch in the meantime and changed other files, gion builds on top of their commit and writes their files to disk too; if they changed a file you edited, it stops and lists it. See [Branches & rebase](branches.md).

Identity comes from git's usual variables (`GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_*`, `*_DATE`) or `user.name` / `user.email` in `~/.gitconfig`.

## Keeping up with upstream

`gion sync` brings unedited files up to date with the branch. Files you've edited are kept; if upstream changed them too, nothing is written and the conflicting paths are listed. To replay your commits onto a newer base, use [`gion rebase`](branches.md#rebase).

## Running tests

A workspace runs whatever the files on disk allow. Add the test directories and the code they import with `gion add`; if that's most of the repo, push the branch and let CI run them.
