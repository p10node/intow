#!/bin/sh
# Runs the test suite of the Windows build under Wine, in a container.
# It is a stand-in until CI's windows-latest job has run: Wine implements file
# IDs, OpenFileById, GetFinalPathNameByHandle and LockFileEx, which is the
# Windows-only code. Needs docker and go.
#   scripts/wine-test.sh [go test flags, e.g. -test.run TestUndo]
set -eu
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
(cd "$root" && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o dist/intow.test.exe .)
[ $# -gt 0 ] || set -- -test.v
docker build -q -t intow-wine - >/dev/null <<'DOCKER'
FROM ubuntu:24.04
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends wine wine64 >/dev/null \
    && rm -rf /var/lib/apt/lists/*
ENV WINEDEBUG=-all WINEDLLOVERRIDES=mscoree=;mshtml= WINEPREFIX=/root/.wine
RUN wine wineboot --init >/dev/null 2>&1; wineserver -w
DOCKER
docker run --rm -v "$root/dist":/dist:ro -w /root intow-wine sh -c 'cp /dist/intow.test.exe . && wine intow.test.exe "$@"' sh "$@"
