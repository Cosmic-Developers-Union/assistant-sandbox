#!/usr/bin/env bash
# Q15:从固定 Alpine 3.22 riscv64 包集合组装客体 Node/V8 运行时。
# 运行环境必须是 riscv64 Alpine 3.22(CI 用 qemu-riscv64 模拟);
# 产物只包含 Node、musl 加载器、运行时共享库与 ICU 数据。
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT=${1:-"$HERE/node22-runtime.tar.gz"}
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT

# 版本和配置固定于 manifest;apk 负责解析同版本的运行时依赖。
apk add --root "$ROOT" --initdb --no-cache --no-scripts \
	nodejs=22.23.5-r0

rm -rf "$ROOT/lib/apk" "$ROOT/var" "$ROOT/etc/apk" "$ROOT/usr/share/man" \
	"$ROOT/usr/share/doc" "$ROOT/usr/bin/npm" "$ROOT/usr/lib/node_modules"
find "$ROOT" -type f -name '*.pyc' -delete
find "$ROOT" -type f -name '*.la' -delete

tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
	-C "$ROOT" -czf "$OUT" .
sha256sum "$OUT"

