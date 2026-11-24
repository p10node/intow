#!/bin/sh
# Render packaging/homebrew/intow.rb.in for VERSION and push it to the tap.
#   scripts/update-formula.sh 0.1.0            (needs TAP_TOKEN in CI)
#   DRY_RUN=1 scripts/update-formula.sh 0.1.0  (print the formula only)
set -eu
version=${1:?usage: update-formula.sh VERSION}
repo=p10node/intow
tap=${TAP_REPO:-p10node/homebrew-tap}
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/src.tar.gz" "https://github.com/$repo/archive/refs/tags/v$version.tar.gz"
if command -v sha256sum >/dev/null 2>&1; then
  sha=$(sha256sum "$tmp/src.tar.gz" | cut -d' ' -f1)
else
  sha=$(shasum -a 256 "$tmp/src.tar.gz" | cut -d' ' -f1)
fi
sed -e "s/@VERSION@/$version/g" -e "s/@SHA256@/$sha/g" "$root/packaging/homebrew/intow.rb.in" |
  grep -v '^# Rendered by\|^# A formula built' >"$tmp/intow.rb"

if [ -n "${DRY_RUN:-}" ]; then
  cat "$tmp/intow.rb"
  exit 0
fi

git clone -q "https://x-access-token:${TAP_TOKEN:?TAP_TOKEN is not set}@github.com/$tap.git" "$tmp/tap"
mkdir -p "$tmp/tap/Formula"
cp "$tmp/intow.rb" "$tmp/tap/Formula/intow.rb"
cd "$tmp/tap"
git -c user.name="intow release" -c user.email="noreply@github.com" commit -qam "intow $version" || {
  echo "formula unchanged"
  exit 0
}
git push -q origin HEAD
echo "pushed Formula/intow.rb $version to $tap"
