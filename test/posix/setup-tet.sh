#!/bin/sh

set -eu
cd "$(dirname "$0")"
mkdir -p .local
archive=.local/tet3.8-src.tar.gz
checksum=5b252a4b578f8893d4005169bfa6dc5ed7a7335a25159a88e068b866ca86a7c8
if [ ! -f "$archive" ]; then
 curl -fL --max-time 60 https://tetworks.opengroup.org/downloads/38/software/Sources/3.8/tet3.8-src.tar.gz -o "$archive"
fi
printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c -
if [ ! -d .local/tet3.8 ]; then tar -xzf "$archive" -C .local; fi
TET_ROOT="$(pwd)/.local/tet3.8"
export TET_ROOT
cd "$TET_ROOT"
sh configure -t lite

make -C src install CC="${TET_CC:-cc -std=gnu89 -Wno-error=implicit-function-declaration -Wno-error=incompatible-pointer-types}"

test -x "$TET_ROOT/bin/tcc"
test -r "$TET_ROOT/lib/posix_sh/tcm.sh"
printf 'TET3.8-Lite 已安装: %s\n' "$TET_ROOT"
