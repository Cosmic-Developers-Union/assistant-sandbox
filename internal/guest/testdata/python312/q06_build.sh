#!/bin/sh
# Q06 native-extension artifact build.
#
# Requires:
#   zig 0.17.0
#   CPython 3.12.16 source tree (headers), e.g. from the Q01 pipeline
#   Alpine python3-3.12.16-r0 pyconfig.h (target configuration)
#
# Usage:
#   CPYTHON_SRC=/path/to/cpython-3.12.16 \
#   PYCONFIG=/path/to/usr/include/python3.12 \
#   ./q06_build.sh
#
# Expected sha256:
#   q06_native.cpython-312-riscv64-linux-musl.so
#     3c6fc6345919c7fb6cd0e285c965e9d7dc807a24dc2dfba8c33604efd8120e06
#   q06_bad.cpython-312-x86_64-linux-gnu.so
#     1677674219ceb9197514282ab8e7ae7dc61a9ed6cbe54bf1019167b8d0204779

set -eu
: "${CPYTHON_SRC:?set CPYTHON_SRC to the CPython 3.12.16 source tree}"
: "${PYCONFIG:?set PYCONFIG to the Alpine python3.12 include directory}"

cd "$(dirname "$0")"

zig cc -target riscv64-linux-musl -shared -fPIC -O2 \
    -I"$CPYTHON_SRC/Include" -I"$PYCONFIG" \
    -o q06_native.cpython-312-riscv64-linux-musl.so q06_native.c

zig cc -target x86_64-linux-gnu -shared -fPIC -O2 \
    -I"$CPYTHON_SRC/Include" -I"$PYCONFIG" \
    -o q06_bad.cpython-312-x86_64-linux-gnu.so q06_native.c

sha256sum q06_native.cpython-312-riscv64-linux-musl.so \
    q06_bad.cpython-312-x86_64-linux-gnu.so
