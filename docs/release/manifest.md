# 发布 SDK、镜像与工具链清单(U12)

本清单固定当前仓库 HEAD 的本地可重建产物。发布时必须先执行
`make release`,再将 `dist/` 与本文档一起上传到稳定 Release。

## 版本与摘要

| 产物 | 版本/来源 | SHA-256 |
| --- | --- | --- |
| Go SDK/CLI | `assistant-sandbox` HEAD | 由 `make release` 写入 `dist/SHA256SUMS` |
| WASI Bash 客体 | 构建器生成 `assistant-bash.wasm.gz` | 由 `dist/SHA256SUMS` 固定 |
| CPython 客体 | 3.12.16 Alpine 3.22 riscv64 | `f8831c8c9716c74cac6e2d950ccac7585d64e5f3dfbebad76677bd68eba32486` |
| Node.js 客体 | 22.23.5-r0 Alpine 3.22 riscv64 | `49c1cca5b16b0ada706083d8e50b003a3e79723d9d2e760cf3400e24442210df` |
| TinyCC 客体工具链 | 0.9.28rc/0.9.27 git Alpine | `dc16d9d4e2b8f3bd1301cf20519efc35fd44ab3ee7a05db3988c16d3bd73ce04` |
| GCC C++ 客体工具链 | GCC 14.2.0-r6/Binutils 2.44 | `a9a452dc848090af9661f8af25f9d541683edb7d665cf4cb1a55f958623199d5` |
| Node 头文件/node-gyp 工具链 | Node 22.23.5/GNU make 4.4.1 | `ff581db506abf7e4a29a8ffccadb44f9d2c46031391cad2084575859a3483e61` |
| Git 客体运行时 | 2.56.1-r0 Alpine edge riscv64 | `a2d0f28913ca6a290df6492cd4c85a2cbac85da7bfc3e8cde7e46b6e06a3e1c0` |
| apk-tools 客体运行时 | 3.0.10-r1 Alpine edge riscv64 | `c291067f9dfc560eb3f6d75f7915cd30bddd6b316ea156ab26ff1b6de97f55f8` |

## 许可证与 SBOM

- Go 模块清单与第三方许可见 `THIRD_PARTY.md`、`licenses/` 与 `go.sum`。
- 客体运行时/工具链的来源、许可证与固定摘要见 `THIRD_PARTY.md`。
- `make release` 生成 `dist/SBOM.txt`,内容为 `go version -m` 的模块构建
  信息;发布流水线必须将该文件与 `dist/SHA256SUMS` 一并保存。

## 新环境重建

```sh
git clone <release-repository-url>
cd assistant-sandbox
make release
make isolated-check
```

`make release` 不依赖原开发机路径;客体运行时的构建/装配入口见
`docs/runtime/toolchain.md`、`internal/guest/testdata/python312/build.sh` 和
`internal/guest/testdata/nodejs/build.sh`。下载资源必须由 Release 提供
`dist/assistant-sandbox`、`dist/assistant-bash.wasm.gz`、`dist/SHA256SUMS`
与 `dist/SBOM.txt`;缺少任一文件不得宣称完整发布。
