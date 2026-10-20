#!/bin/sh
# Re-record the docs demos with VHS (brew install vhs). Run from the repo root.
# push and sync touch demo/* branches on baz-scm/gion; they're deleted at the end.
set -e
repo=baz-scm/gion
cleanup() {
  for b in demo/recording demo/sync; do
    gh api -X DELETE "repos/$repo/git/refs/heads/$b" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT
cleanup

for t in cat log diff workspace; do vhs "demos/$t.tape"; done

vhs demos/push.tape
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless=new --disable-gpu \
  --hide-scrollbars --window-size=1280,800 --blink-settings=preferredColorScheme=0 \
  --screenshot=docs/assets/demos/push-github.png "https://github.com/$repo/tree/demo/recording" 2>/dev/null

gh api "repos/$repo/git/refs" -f ref=refs/heads/demo/sync \
  -f sha="$(gh api "repos/$repo/commits/ef2dee7505b7" -q .sha)" >/dev/null
vhs demos/sync.tape
