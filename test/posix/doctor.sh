#!/bin/sh

set -eu
cd "$(dirname "$0")"
for tool in go git perl cc make curl tar sha256sum; do
 if command -v "$tool" >/dev/null 2>&1; then printf '控制环境 %-12s 已找到\n' "$tool"
 else printf '控制环境 %-12s 缺少\n' "$tool"
 fi
done
if [ -x .local/tet3.8/bin/tcc ]; then TET_ROOT="$(pwd)/.local/tet3.8" .local/tet3.8/bin/tcc -V
else printf 'TET3-Lite 未安装；运行 make setup\n'
fi
for suite in VSC-PCTS2016 VSX-PCTS2016; do
 if [ -d ".local/suites/$suite" ] && [ -n "$(ls -A ".local/suites/$suite")" ]; then
  printf '%s 目录有内容；仍需按授权套件文档验证版本与目标适配\n' "$suite"
 else printf '%s 尚未提供授权套件\n' "$suite"
 fi
done
printf 'VSX 目标状态：尚未配置 assistant-wasi 的 POSIX C ABI/系统接口执行器\n'
