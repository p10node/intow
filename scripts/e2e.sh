#!/bin/sh
# End-to-end test against a real, logged-in Claude Code.
#
# Loads ./plugin with --plugin-dir and serves the release asset from a local
# file:// "release" with its own checksum file, so the plugin's
# download-and-verify path is exercised.
# Then it renames and moves a folder and checks that the next session in the
# new place sees the old memory and can resume the old session.
# Costs three small Haiku requests. Removes everything it creates under ~/.claude.
#
#   CLAUDE_BIN=/path/to/claude scripts/e2e.sh
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
claude=${CLAUDE_BIN:-claude}
version=$(sed -n 's/^[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$root/plugin/.claude-plugin/plugin.json" | head -n 1)
cfg=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
work="$HOME/.ccanchor-e2e-$$" # not a temp dir: those are never tracked
state_existed=no
[ -d "$cfg/ccanchor" ] && state_existed=yes
ls "$cfg/plugins/data" 2>/dev/null | sort >"/tmp/ccanchor-e2e-data-before.$$" || true

enc() { printf '%s' "$1" | sed 's/[^a-zA-Z0-9]/-/g'; }
pass=0 failed=0
check() { if eval "$2"; then echo "PASS  $1"; pass=$((pass + 1)); else echo "FAIL  $1"; failed=$((failed + 1)); fi; }

cleanup() {
  for d in "$cfg/projects/$(enc "$work/projA")" "$cfg/projects/$(enc "$work/moved/projB")"; do
    if [ -d "$d" ]; then
      for f in "$d"/*.jsonl; do
        [ -e "$f" ] || continue
        id=$(basename "$f" .jsonl)
        rm -rf "$cfg/session-env/$id" "$cfg/file-history/$id"
      done
      rm -rf "$d"
    fi
  done
  if [ "$state_existed" = no ]; then rm -rf "$cfg/ccanchor"; else "$root/dist/ccanchor" forget "$work" >/dev/null 2>&1 || true; fi
  ls "$cfg/plugins/data" 2>/dev/null | sort | comm -13 "/tmp/ccanchor-e2e-data-before.$$" - | while read -r d; do
    case $d in *ccanchor*) rm -rf "${cfg:?}/plugins/data/$d" ;; esac
  done
  rm -f "/tmp/ccanchor-e2e-data-before.$$"
  rm -rf "$work"
}
trap cleanup EXIT

echo "== build ccanchor $version and a local release"
rel="$work/release/v$version"
mkdir -p "$rel" "$work/build"
case $(uname -m) in arm64 | aarch64) arch=arm64 ;; *) arch=amd64 ;; esac
os=$(uname -s | tr '[:upper:]' '[:lower:]')
(cd "$root" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$work/build/ccanchor" .)
mkdir -p "$root/dist" && cp "$work/build/ccanchor" "$root/dist/ccanchor"
asset="ccanchor_${version}_${os}_${arch}.tar.gz"
tar -czf "$rel/$asset" -C "$work/build" ccanchor
(cd "$rel" && shasum -a 256 "$asset" >"ccanchor_${version}_checksums.txt")

run() { # run <dir> <prompt> [extra args...]
  dir=$1 prompt=$2
  shift 2
  (cd "$dir" && CCANCHOR_RELEASE_BASE="file://$work/release" CCANCHOR_CHECKSUMS="$rel/ccanchor_${version}_checksums.txt" \
    "$claude" -p "$prompt" \
    --plugin-dir "$root/plugin" --setting-sources project --model haiku "$@" </dev/null 2>&1)
}

echo "== 1. first session in projA"
mkdir -p "$work/projA"
out=$(run "$work/projA" "Reply with exactly: OK")
check "first session answers" '[ "$(printf %s "$out" | tail -n 1)" = OK ]'
check "registry tracks projA" 'grep -q "\"$work/projA\"" "$cfg/ccanchor/registry.json"'
old_dir="$cfg/projects/$(enc "$work/projA")"
first=$(basename "$(ls "$old_dir"/*.jsonl | head -n 1)" .jsonl)
mkdir -p "$old_dir/memory"
printf -- '- [Codeword](codeword.md) — project codeword\n' >"$old_dir/memory/MEMORY.md"
printf -- '---\nname: codeword\ndescription: project codeword\n---\nThe project codeword is PAPAYA-42.\n' >"$old_dir/memory/codeword.md"

echo "== 2. rename + move projA -> moved/projB, start Claude there"
mkdir -p "$work/moved"
mv "$work/projA" "$work/moved/projB"
new_dir="$cfg/projects/$(enc "$work/moved/projB")"
out=$(run "$work/moved/projB" "What is the project codeword? Answer with just the codeword.")
echo "   claude: $out"
check "memory followed the folder" 'printf %s "$out" | grep -q PAPAYA-42'
check "old project dir is gone" '[ ! -d "$old_dir" ]'
check "old session moved" '[ -f "$new_dir/$first.jsonl" ]'
check "cwd rewritten" '! grep -q "\"cwd\":\"$work/projA" "$new_dir/$first.jsonl"'

echo "== 3. resume the first session from the new place"
out=$(run "$work/moved/projB" "Quote my very first message in this conversation verbatim." --resume "$first")
echo "   claude: $(printf %s "$out" | tr '\n' ' ')"
check "old session resumes" 'printf %s "$out" | grep -q "Reply with exactly: OK"'

echo "== $pass passed, $failed failed"
[ "$failed" -eq 0 ]
