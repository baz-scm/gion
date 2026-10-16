# Agents

gion is built for coding agents: everything takes `--md` for Markdown output, nothing needs a clone, and many agents on one machine share one cache.

## The skill

gion ships an [agent skill](https://github.com/baz-scm/gion/blob/main/skills/gion/SKILL.md) that teaches agents when to use gion instead of `git clone`, which commands to run, and how to resolve rebase conflicts. The binary carries it, so it always matches your gion version:

```sh
gion skill install            # ~/.claude/skills (Claude Code) and ~/.agents/skills (Codex, Cursor, Amp, ...)
gion skill install --project  # the same, inside the current project
```

Rerun it after upgrading. Or install it with the [skills](https://skills.sh) CLI, which tracks `main`:

```sh
npx skills add baz-scm/gion
```

Agents without the skill can run `gion help --md`, which prints the same guide.

## Recommended workflows

**Answer questions about a repo** (no disk at all): `ls` and `find` to orient, `grep` to locate, `cat` to read, `log` / `blame` / `diff` for history.

**Small edits**: write the new file contents locally and commit them straight to a branch:

```sh
gion commit owner/repo -m "fix typo" --branch fix-typo docs/intro.md=./intro.md --md
```

**Real changes**: a sparse workspace with only the directories the task touches. Search the rest of the repo remotely; `gion add` anything else you need to edit.

```sh
gion clone owner/repo ./ws --path src/api --md
cd ws
# edit, then:
gion status --md && gion diff --md
gion commit -m "msg" --branch agent/task --md
```

**Many agents in parallel**: give each its own workspace (or none). They share `~/.gion`, so the second agent on the same repo fetches almost nothing. Pushes to the same branch rebase automatically when they touch different files.

## Rules worth giving an agent

- Don't `git clone` a repo just to read or search it.
- In a sparse workspace, local file tools only see the cloned paths; search the rest with `gion grep <remote>`.
- Check `gion status --md` before each commit: pushes can't be force-pushed away.
- Push to a branch, not the default branch, unless told to.
- On a rebase conflict whose right answer isn't clear from the code, ask a human.
