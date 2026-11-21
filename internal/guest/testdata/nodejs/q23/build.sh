#!/usr/bin/env bash
# Q23:构建 riscv64 目标架构 N-API 扩展,并额外产出 x86_64 ELF 用于
# 错误 ABI 诊断测试。需要 zig 0.17.0 与 Node v22.23.5 源码头文件。
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
NODE_SRC=${NODE_SRC:-/tmp/node-v22.23.5}
CFLAGS=(-O2 -fPIC -shared -I"$NODE_SRC/src" -I"$NODE_SRC/deps/uv/include" -I"$NODE_SRC/deps/v8/include")
zig cc -target riscv64-linux-musl "${CFLAGS[@]}" -o "$HERE/q23_addon.node" "$HERE/addon.c"
zig cc -target x86_64-linux-gnu "${CFLAGS[@]}" -o "$HERE/q23_addon_x86_64.node" "$HERE/addon.c"
sha256sum "$HERE/q23_addon.node" "$HERE/q23_addon_x86_64.node"
