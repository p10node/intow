#!/bin/sh
# Make the plugin run release VERSION: write the release's sha256 checksums to
# plugin/checksums.txt and set the version in plugin.json, as one commit on the
# default branch. The plugin launcher only runs a download that matches the
# checksums pinned here, so replacing a release asset does not change what
# users run, and nobody sees the new plugin version before its binaries exist.
#   scripts/pin-plugin.sh 0.1.0             (after goreleaser; reads dist/, commits and pushes)
#   DRY_RUN=1 scripts/pin-plugin.sh 0.1.0   (edit the working tree only)
set -eu
version=${1:?usage: pin-plugin.sh VERSION}
branch=${RELEASE_BRANCH:-main}
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
sums="$root/dist/ccanchor_${version}_checksums.txt"
grep -q '\.tar\.gz$' "$sums" 2>/dev/null || { echo "pin-plugin: no checksums in $sums (run goreleaser first)" >&2; exit 1; }

tree=$root
if [ -z "${DRY_RUN:-}" ]; then
  # A separate worktree of the branch: the tag's checkout stays as it is.
  tmp=$(mktemp -d)
  trap 'git -C "$root" worktree remove --force "$tmp/tree" >/dev/null 2>&1; rm -rf "$tmp"' EXIT
  git -C "$root" fetch -q origin "$branch"
  git -C "$root" worktree add -q --detach "$tmp/tree" FETCH_HEAD
  tree=$tmp/tree
fi

{
  echo "# sha256 of the release assets for the version in .claude-plugin/plugin.json."
  echo "# Written by scripts/pin-plugin.sh at release time. scripts/ccanchor runs no other download."
  grep '\.tar\.gz$' "$sums"
} >"$tree/plugin/checksums.txt"
manifest="$tree/plugin/.claude-plugin/plugin.json"
sed 's/^\([[:space:]]*"version"[[:space:]]*:[[:space:]]*"\)[^"]*"/\1'"$version"'"/' "$manifest" >"$manifest.new"
mv "$manifest.new" "$manifest"
grep -q "\"version\": \"$version\"" "$manifest" || { echo "pin-plugin: could not set the version in $manifest" >&2; exit 1; }

if [ -n "${DRY_RUN:-}" ]; then
  git -C "$tree" --no-pager diff --stat -- plugin
  exit 0
fi
git -C "$tree" add plugin/checksums.txt plugin/.claude-plugin/plugin.json
if git -C "$tree" diff --cached --quiet; then
  echo "plugin already at $version"
  exit 0
fi
git -C "$tree" -c user.name="ccanchor release" -c user.email="noreply@github.com" commit -q -m "Plugin $version"
git -C "$tree" push -q origin "HEAD:$branch"
echo "pinned plugin $version on $branch"
