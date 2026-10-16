# Working on gion

gion is a Go CLI (module `github.com/baz-scm/gion`, Go 1.26, go-git v6). Code lives in `internal/`: `wire` (git protocol), `repo` (objects, history, commits, rebase), `workspace` (sparse working directories), `cli` (commands), `search`, `index`, `forge` (GitHub APIs), `cache`, `auth`, `testgit` (test server).

## Rules

- **Use gion, not git.** Work in a sparse workspace with only what the task needs: `gion clone baz-scm/gion <dir> --path internal --path go.mod --path go.sum --path main.go` (add `docs`, `skills`, `README.md`, `.github` as needed with `gion add`). Never make a full clone or a `.git` checkout of this repo.
- **Commit straight to main** with `gion commit -m "..." --to main` (later commits in the same workspace just `gion commit`). No branches, no PRs. Keep commit messages in the existing style (`feat:`, `fix:`, `docs:`, `ci:`), and end them with a `Co-Authored-By:` line for the agent.
- **Before every push:** `test -z "$(gofmt -l .)" && go vet ./... && go test -race ./...`, then `gion status` to check nothing stray goes in.
- **Never move or delete a tag.** The Go module proxy caches tags forever. Releases are tagged only when the maintainer asks.
- **gion never shells out to git.** The `git` CLI is a test fixture only (`internal/testgit` serves a seeded repo with `git daemon`; tests compare gion's answers with git's).

## Keep these in sync when a command or flag changes

1. The usage text in `internal/cli/cli.go`.
2. `docs/reference.md`: regenerate with `go run . help --reference > docs/reference.md` (CI fails if it's stale).
3. The agent skill `skills/gion/SKILL.md` (it's embedded in the binary and printed by `gion help --md`).
4. The guide page in `docs/` that covers it, and the short command list in `README.md`.

## Testing

```sh
go test -race ./...        # needs git on PATH for the integration tests
go build -o /tmp/gion .    # try a build without installing it
```

Prefer integration tests against `testgit.Serve` over mocks, and compare with git's own output where git has the same feature (`log`, `blame`, `diff --numstat`, `merge-file`). Check user-facing changes against real GitHub repos too (`baz-scm/gion`, `chromium/chromium` for scale); delete any test branches you push.

## Docs site

`docs/` is a MkDocs Material site published to https://baz-scm.github.io/gion/ by `.github/workflows/pages.yml` on pushes to main. Build locally with `pip install "mkdocs-material==9.6.*" && mkdocs build --strict`.

## Releasing

When the maintainer asks for a release, tag main with gion:

```sh
gion tag baz-scm/gion vX.Y.Z -m "vX.Y.Z"
```

The release workflow reruns CI on that exact commit, checks the tag is on main, and GoReleaser publishes binaries and checksums. `install.sh` and `go install …@latest` pick it up. Pick the version by semver: new commands or flags are a minor bump, fixes a patch.

## Benchmarks

`bench/bench.sh [RUNS]` times gion against the fastest equivalent git command on public repos. Update `docs/performance.md` with the date, machine and versions when you rerun it.
