#!/bin/sh
# Install the latest ccanchor release into ~/.local/bin (or $CCANCHOR_INSTALL_DIR).
#   curl -fsSL https://raw.githubusercontent.com/p10node/ccanchor/main/scripts/install.sh | sh
# The download is checked against the release's sha256 checksums.
set -eu
REPO=p10node/ccanchor
dir=${CCANCHOR_INSTALL_DIR:-$HOME/.local/bin}

case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "ccanchor: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "ccanchor: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

version=${CCANCHOR_VERSION:-$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
  sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\([^"]*\)".*/\1/p' | head -n 1)}
[ -n "$version" ] || { echo "ccanchor: could not find the latest release" >&2; exit 1; }

asset="ccanchor_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/sums" "$base/ccanchor_${version}_checksums.txt"
want=$(grep " $asset\$" "$tmp/sums" | cut -d' ' -f1)
if command -v shasum >/dev/null 2>&1; then
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "ccanchor: checksum mismatch" >&2; exit 1; }
tar -xzf "$tmp/$asset" -C "$tmp" ccanchor
mkdir -p "$dir"
mv "$tmp/ccanchor" "$dir/ccanchor"
chmod 755 "$dir/ccanchor"

echo "installed ccanchor $version to $dir/ccanchor"
case ":$PATH:" in *":$dir:"*) ;; *) echo "note: $dir is not on your PATH" ;; esac
echo "next: run \`ccanchor install\` to add the Claude Code hook"
