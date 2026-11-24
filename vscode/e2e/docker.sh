#!/bin/sh
# Real-VS-Code check of the extension, in a container (needs docker and go).
#   vscode/e2e/docker.sh
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
(cd "$root" && CGO_ENABLED=0 go build -trimpath -o dist/ccanchor .)
docker run --rm -v "$root":/src:ro node:22-bookworm bash -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null
  apt-get install -y -qq xvfb xauth libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libgtk-3-0 \
    libgbm1 libasound2 libxkbfile1 libsecret-1-0 libxshmfence1 >/dev/null
  cp -r /src/vscode /w && cd /w
  npm install --no-save --silent --no-audit --no-fund @vscode/test-electron
  # Not xvfb-run: it can miss the ready signal of the X server and wait forever.
  Xvfb :99 -screen 0 1280x1024x24 -nolisten tcp >/dev/null 2>&1 &
  sleep 2
  DISPLAY=:99 CCANCHOR_BIN=/src/dist/ccanchor timeout 300 node e2e/run.js
'
