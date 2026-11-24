#!/bin/sh
# Install the latest intow release into ~/.local/bin (or $INTOW_INSTALL_DIR).
#   curl -fsSL https://raw.githubusercontent.com/p10node/intow/main/scripts/install.sh | sh
# The download is checked against the release's sha256 checksums.
set -eu
REPO=p10node/intow
dir=${INTOW_INSTALL_DIR:-$HOME/.local/bin}

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "intow: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "intow: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

version=${INTOW_VERSION:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
  sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\([^"]*\)".*/\1/p' | head -n 1)}
[ -n "$version" ] || { echo "intow: could not find the latest release" >&2; exit 1; }

asset="intow_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/sums" "$base/intow_${version}_checksums.txt"
want=$(grep " $asset\$" "$tmp/sums" | cut -d' ' -f1)
if command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "intow: checksum mismatch" >&2; exit 1; }
tar -xzf "$tmp/$asset" -C "$tmp" intow
mkdir -p "$dir"
mv "$tmp/intow" "$dir/intow"
chmod 755 "$dir/intow"

echo "installed intow $version to $dir/intow"
case ":$PATH:" in *":$dir:"*) ;; *) echo "note: $dir is not on your PATH" ;; esac
echo "next: run \`intow install\` to add the Claude Code hook"
