# Search & history

## Paths: `find`

```sh
gion find chromium/chromium string_util.h        # substring
gion find chromium/chromium 'base/**/*_test.cc'  # glob: * ? ** [...]
```

Matching is case-insensitive unless the pattern has an uppercase letter.

`find` and `grep` use a **path index**: every file path at a commit, built from the folder listings alone (no file contents). The first build for a repo fetches every folder once (chromium: 45k trees, 510k paths, a 15 MB index). Later, moving to a newer commit fetches only the folders that changed, and a commit already indexed costs one ref lookup.

## Contents: `grep`

```sh
gion grep chromium/chromium 'TrimWhitespaceASCII\('
gion grep baz-scm/baz 'TODO' --path bff/src -i -l
```

Output is `path:line:text`. gion picks how to search:

- **Few enough files** (after `--path`, at most `--max-files`, default 5000): fetch them all (once; they stay cached) and scan locally with the regex.
- **More than that**: ask GitHub code search which files contain the regex's longest literal, fetch only those, and scan them with the real regex. Lines are exact, but GitHub indexes only the default branch and skips some files (very large ones, for example), so gion notes where candidates came from. A regex with no 3+ character literal (like a top-level `a|b`) can't use this; narrow it with `--path`.

`--local` forces a full scan and `--forge` forces code search.

## Changes: `diff`

```sh
gion diff baz-scm/gion main~3 main             # unified diff
gion diff baz-scm/gion v0.1.1..v0.2.0 --stat    # lines added/removed per file
gion diff baz-scm/gion v0.1.1..v0.2.0 --name-status
gion diff baz-scm/gion HEAD --path internal    # what the last commit changed there
gion diff                                      # in a workspace: your edits
```

Only folders and files that differ are fetched, so a diff costs the size of the change, not of the repo.

## Commits: `log`

```sh
gion log chromium/chromium -n 20 --oneline
gion log chromium/chromium --path base/strings/string_util.h -n 5
gion log                                       # in a workspace: its commit's history
```

Commits are fetched as history without trees or files, up to 256 per round trip, so plain logs are fast on any repo (chromium: 1.3 s for 20 commits).

`--path` keeps the commits that changed that file or directory, with git's default history simplification (a merge that kept one parent's version is followed down that parent only). On GitHub with credentials, the commits API answers directly (chromium: ~3 s). Elsewhere, or with `--local`, gion walks history and checks the path in batches of commits, which is slow for a rarely changed path in a huge repo; it stops after 20,000 commits and says so.

## Lines: `blame`

```sh
gion blame chromium/chromium HEAD:base/strings/string_util.h -L 1,40
gion blame src/app.py                          # in a workspace
```

For each line, the commit that last changed it. On GitHub with credentials, gion uses GitHub's blame API (chromium, 13 years of history: ~8 s). Otherwise, or with `--local`, it walks the file's history and diffs versions line by line; that matches `git blame` in tests, but doesn't follow renames.
