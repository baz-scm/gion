# CI

gion helps CI where checkout is the slow part: monorepos where a job needs one directory, jobs that only need the list of changed files, and jobs that read a few files from another repo. For a small repo that needs every file, `actions/checkout` is just as fast.

In GitHub Actions, use [gion-action](https://github.com/baz-scm/gion-action). Each of its actions installs gion itself, on Linux and macOS runners.

## Check out one directory

```yaml
- uses: baz-scm/gion-action/checkout@v1
  with:
    paths: |
      services/api
```

You get `services/api` plus the files in its parent directories (root config, lockfiles), at `$GITHUB_SHA`. Leave `paths` empty for the whole tree. Other inputs: `repository`, `ref`, `path` (the directory to put the workspace in) and `git: false` to skip the local `.git` snapshot.

For the commit id (to tag images, say), `git rev-parse HEAD` is the upstream commit in any workspace, sparse or not, so tools that read it need no changes. `git rev-parse GION_HEAD` gives the same.

There is no history, so tools that need tags or `git log` (GoReleaser, changelog generators, `git describe`) still need `actions/checkout`.

## Only the changed files

```yaml
- uses: baz-scm/gion-action/changed@v1
  id: changed
  with:
    paths: services/api
- if: steps.changed.outputs.any == 'true'
  run: make -C services/api test
```

This fetches only the folders that changed, with no checkout at all. Outputs: `files` (one per line), `json`, `count`, `any`.

On `pull_request` it compares GitHub's test merge commit with its first parent, which is exactly what the PR changes even after the base branch moves on. On `push` it compares `github.event.before`, and on `merge_group` the queue's base. For other events, set `base` and `head`.

## Everything else

The root action installs gion and puts it on `PATH`:

```yaml
- uses: baz-scm/gion-action@v1
- run: gion cat my-org/shared-config HEAD:deploy/prod.yaml > prod.yaml
```

### Commit from CI

```yaml
- run: |
    echo "${{ github.ref_name }}" > VERSION
    gion commit "$GITHUB_REPOSITORY" -m "bump version" --to main VERSION=./VERSION
  env:
    GIT_AUTHOR_NAME: ci-bot
    GIT_AUTHOR_EMAIL: ci-bot@users.noreply.github.com
```

The job needs `permissions: contents: write`. The push is compare-and-swap, so a concurrent push to `main` that touches other files is rebased over, and one that touches `VERSION` fails the step instead of being overwritten.

## Tokens

Every gion-action action exports its `token` input (default `github.token`) as `GITHUB_TOKEN`, which gion uses for github.com. `github.token` only reaches the repository running the workflow. To read other private repos or push to them, pass a PAT or GitHub App token:

```yaml
- uses: baz-scm/gion-action@v1
  with:
    token: ${{ secrets.ORG_READ_TOKEN }}
```

## Versions and caching

Each gion-action release installs a pinned gion version, so it skips looking up the latest release. `@v1` moves to the newest gion shortly after each release. Set `version:` to pin one yourself, or `version: latest`.

You don't need `actions/cache`. gion fetches only the objects a command needs, so a fresh runner is already fast, and restoring a cache of many small object files usually costs more than it saves. On self-hosted runners, `~/.gion` and the installed binary persist between jobs anyway.

## Other CI systems

Install with the script and call gion directly:

```sh
curl -fsSL https://raw.githubusercontent.com/baz-scm/gion/main/install.sh | sh
gion clone "$REPO" . --ref "$COMMIT" --path services/api
gion diff "$REPO" "$BASE" "$COMMIT" --name-only
```

`gion diff a b` compares the two commits directly (like `git diff a b`), not from their merge base. If the base branch has moved on, compare against the merge commit's first parent, as gion-action does, or against the merge base.

## gion's own CI

[gion's CI](https://github.com/baz-scm/gion/blob/main/.github/workflows/ci.yml) checks itself out with `baz-scm/gion-action/checkout@v1` in its test and release dry-run jobs. Only the real release uses `actions/checkout`, because GoReleaser needs a git repo for tags and the changelog.
