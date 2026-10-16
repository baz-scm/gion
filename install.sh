#!/bin/sh
# Installs the latest gion release binary. No Go needed.
#
#   curl -fsSL https://raw.githubusercontent.com/baz-scm/gion/main/install.sh | sh
#
# Environment:
#   GION_VERSION      release to install, e.g. v0.2.0 (default: latest)
#   GION_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
set -eu

repo=baz-scm/gion
dir=${GION_INSTALL_DIR:-$HOME/.local/bin}

err() { echo "gion install: $*" >&2; exit 1; }
command -v curl >/dev/null || err "curl is required"
command -v tar >/dev/null || err "tar is required"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) err "unsupported OS $(uname -s); see https://github.com/$repo/releases" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) err "unsupported CPU $(uname -m); see https://github.com/$repo/releases" ;;
esac

version=${GION_VERSION:-}
if [ -z "$version" ]; then
  # The latest-release page redirects to .../tag/<version>; no API token or
  # rate limit involved.
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
    err "couldn't reach github.com"
  version=${url##*/}
fi
case "$version" in v*) ;; *) err "couldn't find the latest release (got '$version')" ;; esac

archive="gion_${version#v}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading gion $version for $os/$arch..."
curl -fsSL -o "$tmp/$archive" "$base/$archive" || err "download failed: $base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || err "download failed: $base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || err "$archive is not in checksums.txt"
if command -v sha256sum >/dev/null; then
  got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$got" = "$want" ] || err "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" gion
mkdir -p "$dir"
mv "$tmp/gion" "$dir/gion"
chmod 755 "$dir/gion"

echo "Installed $("$dir/gion" version) to $dir/gion"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    case "${SHELL:-}" in
      */zsh) rc=~/.zshrc ;;
      */bash) rc=~/.bashrc ;;
      *) rc=~/.profile ;;
    esac
    echo
    echo "$dir is not on your PATH. Add it:"
    echo "  echo 'export PATH=\"\$PATH:$dir\"' >> $rc && . $rc"
    ;;
esac
echo
echo "Next: run 'gion skill install' so coding agents know how to use gion."
