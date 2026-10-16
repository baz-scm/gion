# CI

gion helps CI where checkout is the slow part: monorepos where a job needs one directory, jobs that only need the list of changed files, and jobs that read a few files from another repo. For a small repo that needs every file, `actions/checkout` is just as fast.

## Install in GitHub Actions

```yaml
env:
  GH_TOKEN: ${{ github.token }}   # gion borrows it through `gh auth token`
steps:
  - name: Install gion
    run: |
      os=$(uname -s | tr '[:upper:]' '[:lower:]')
      case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
      gh release download -R baz-scm/gion --pattern "gion_*_${os}_${arch}.tar.gz" -O - | tar -xz -C "$RUNNER_TEMP" gion
      echo "$RUNNER_TEMP" >> "$GITHUB_PATH"
```

`curl -fsSL https://raw.githubusercontent.com/baz-scm/gion/main/install.sh | sh` works too.

## Check out one directory

```yaml
  - run: gion clone "$GITHUB_REPOSITORY" . --ref "$GITHUB_SHA" --path services/api
```

You get `services/api` plus the files in its parent directories (root config, lockfiles), at the exact commit under test. Pull-request merge commits work the same way, since `$GITHUB_SHA` is a full id.

## Only the changed files

```yaml
  - run: |
      gion diff "$GITHUB_REPOSITORY" ${{ github.event.pull_request.base.sha }} "$GITHUB_SHA" --name-only > changed.txt
```

This fetches only the folders that changed, with no checkout at all. Note it compares the two commits directly (like `git diff base head`), not from their merge base.

## Read from another repo

```yaml
  - run: gion cat my-org/shared-config HEAD:deploy/prod.yaml > prod.yaml
```

## Commit from CI

```yaml
  - run: |
      echo "${{ github.ref_name }}" > VERSION
      gion commit "$GITHUB_REPOSITORY" -m "bump version" --to main VERSION=./VERSION
    env:
      GIT_AUTHOR_NAME: ci-bot
      GIT_AUTHOR_EMAIL: ci-bot@users.noreply.github.com
```

The job needs `permissions: contents: write`. The push is compare-and-swap, so a concurrent push to `main` that touches other files is rebased over, and one that touches `VERSION` fails the step instead of being overwritten.

## Caching

`~/.gion` holds only immutable objects, so caching it between runs is always safe (`actions/cache` with any key).

## gion's own CI

[gion's CI](https://github.com/baz-scm/gion/blob/main/.github/workflows/ci.yml) checks itself out with the latest gion release in its test and release dry-run jobs. Only the real release uses `actions/checkout`, because GoReleaser needs a git repo for tags and the changelog.
