#!/usr/bin/env bash
# Benchmarks gion against the fastest comparable git command, on public
# GitHub repos. Each case runs RUNS times; the median wall time is reported.
# "cold" runs use an empty gion cache; "warm" runs reuse it. Refs are never
# cached, so every gion run still makes at least one round trip.
#
# usage: bench/bench.sh [RUNS]      (needs gion and git on PATH, gh auth for grep)
set -euo pipefail

RUNS=${1:-3}
GION=${GION:-gion}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/gion-bench.XXXXXX")
trap 'rm -rf "$WORK"' EXIT

BIG=chromium/chromium
BIG_FILE=base/strings/string_util.h
MID=baz-scm/baz
MID_DIR=demos

now() { perl -MTime::HiRes=time -e 'printf "%.3f", time'; }

# time_cmd <setup> <cmd>: runs setup untimed, then times cmd. Prints seconds.
time_cmd() {
  eval "$1" >/dev/null 2>&1
  local s e
  s=$(now)
  eval "$2" >/dev/null 2>&1 || { echo "FAIL: $2" >&2; exit 1; }
  e=$(now)
  echo "$e - $s" | bc
}

# median <setup> <cmd>
median() {
  local xs=()
  for _ in $(seq "$RUNS"); do xs+=("$(time_cmd "$1" "$2")"); done
  printf '%s\n' "${xs[@]}" | sort -n | awk '{a[NR]=$1} END {m=a[int((NR+1)/2)]; printf "%.2f s\n", m}'
}

fresh() { echo "rm -rf '$WORK/c' '$WORK/w' '$WORK/g'; export GION_CACHE='$WORK/c/objects'"; }
warm() { echo "rm -rf '$WORK/w' '$WORK/g'; export GION_CACHE='$WORK/c/objects'"; }

row() { printf '| %s | %s | %s | %s |\n' "$1" "$2" "$3" "$4"; }

echo "| Task | gion, cold cache | gion, warm cache | git baseline |"
echo "| --- | --- | --- | --- |"

row "Resolve \`HEAD\` (chromium)" \
  "$(median "$(fresh)" "$GION ls-refs $BIG --prefix HEAD")" \
  "same (refs are never cached)" \
  "$(median ":" "git ls-remote https://github.com/$BIG HEAD") \`ls-remote\`"

# Warm the cache once so warm runs measure reuse.
eval "$(fresh)"; $GION cat $BIG HEAD:$BIG_FILE >/dev/null
row "Read one file 3 dirs deep (chromium, 66 GB)" \
  "$(median "$(fresh)" "$GION cat $BIG HEAD:$BIG_FILE")" \
  "$(median "$(warm)" "$GION cat $BIG HEAD:$BIG_FILE")" \
  "$(median "rm -rf '$WORK/g'" "git clone -q --depth 1 --filter=blob:none --no-checkout https://github.com/$BIG '$WORK/g' && git -C '$WORK/g' show HEAD:$BIG_FILE") \`clone --depth 1 --filter=blob:none\` + \`show\`"

eval "$(fresh)"; $GION find $BIG string_util.h >/dev/null 2>&1
row "Find paths in all 510k files (chromium)" \
  "$(median "$(fresh)" "$GION find $BIG string_util.h")" \
  "$(median "$(warm)" "$GION find $BIG string_util.h")" \
  "$(median "rm -rf '$WORK/g'" "git clone -q --depth 1 --filter=blob:none --no-checkout https://github.com/$BIG '$WORK/g' && git -C '$WORK/g' ls-tree -r --name-only HEAD | grep -F string_util.h") \`clone --depth 1 --filter=blob:none\` + \`ls-tree\`"

# Reuses the path index from the find row above; file contents are fetched
# per run only for code-search hits not yet cached.
row "Grep all of chromium (GitHub code search + local verify)" \
  "–" \
  "$(median "$(warm)" "$GION grep $BIG 'TrimWhitespaceASCII\\('")" \
  "needs a full checkout first"

eval "$(fresh)"; $GION grep $MID 'TODO\(' >/dev/null 2>&1
row "Grep all of baz (3.5k files, local scan)" \
  "$(median "$(fresh)" "$GION grep $MID 'TODO\\('")" \
  "$(median "$(warm)" "$GION grep $MID 'TODO\\('")" \
  "$(median "rm -rf '$WORK/g'" "git clone -q --depth 1 https://github.com/$MID '$WORK/g' && git -C '$WORK/g' grep -n 'TODO('") \`clone --depth 1\` + \`git grep\`"

eval "$(fresh)"; $GION clone $MID "$WORK/w" --path $MID_DIR >/dev/null
row "Clone one directory (baz \`$MID_DIR/\`)" \
  "$(median "$(fresh)" "$GION clone $MID '$WORK/w' --path $MID_DIR")" \
  "$(median "$(warm)" "$GION clone $MID '$WORK/w' --path $MID_DIR")" \
  "$(median "rm -rf '$WORK/g'" "git clone -q --depth 1 --filter=blob:none --sparse https://github.com/$MID '$WORK/g' && git -C '$WORK/g' sparse-checkout set $MID_DIR") \`clone --depth 1 --filter=blob:none --sparse\`"

eval "$(fresh)"; $GION clone $MID "$WORK/w" >/dev/null
row "Clone the whole repo (baz, 3.5k files)" \
  "$(median "$(fresh)" "$GION clone $MID '$WORK/w'")" \
  "$(median "$(warm)" "$GION clone $MID '$WORK/w'")" \
  "$(median "rm -rf '$WORK/g'" "git clone -q --depth 1 https://github.com/$MID '$WORK/g'") \`clone --depth 1\`"
