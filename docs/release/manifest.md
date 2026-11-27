# 发布 SDK、镜像与工具链清单(U12)

本清单固定当前仓库 HEAD 的本地可重建产物。发布时必须先执行
`make release`,再将 `dist/` 与本文档一起上传到稳定 Release。GitHub
Release 工作流使用 `testdata/release/build-release.sh` 生成五平台二进制、
`SBOM.txt` 与 `SHA256SUMS`，先创建 draft，再从空目录下载复验，全部通过
后才发布。平台与后端运行时验证范围见
[`platform-matrix.md`](platform-matrix.md)。

发布构建必须注入标签版本，并检查 `dist/assistant-sandbox --version`
输出与标签一致：

```sh
make release VERSION=vX.Y.Z
./dist/assistant-sandbox --version
```

## 版本与摘要

| 产物 | 版本/来源 | SHA-256 |
| --- | --- | --- |
| Go SDK/CLI | `assistant-sandbox` HEAD | 由 `make release` 写入 `dist/SHA256SUMS` |
| CPython 客体 | 3.12.16 Alpine 3.22 riscv64（zstd tar） | `be182df1d7aa55ebe0f63ee6967a142f63ab0bb7291b6fcb2e5156a3af0b46b1` |
| Node.js 客体 | 22.23.5-r0 Alpine 3.22 riscv64（zstd tar） | `23f6695490aee03ed372669896e37640e13b71a6b66cc2a5298d7fa69779294b` |
| TinyCC 客体工具链 | 0.9.28rc/0.9.27 git Alpine | `dc16d9d4e2b8f3bd1301cf20519efc35fd44ab3ee7a05db3988c16d3bd73ce04` |
| GCC C++ 客体工具链 | GCC 14.2.0-r6/Binutils 2.44 | `a9a452dc848090af9661f8af25f9d541683edb7d665cf4cb1a55f958623199d5` |
| Node 头文件/node-gyp 工具链 | Node 22.23.5/GNU make 4.4.1 | `ff581db506abf7e4a29a8ffccadb44f9d2c46031391cad2084575859a3483e61` |
| Git 客体运行时 | 2.56.1-r0 Alpine edge riscv64（zstd tar） | `133cec2d195ff3814ace2d023e115a5880ccab3b0851e1c2952973099e9fb1b1` |
| apk-tools 客体运行时 | 3.0.10-r1 Alpine edge riscv64（zstd tar） | `fa9e89010241f9d9ee9531a23af45b4414f584d0b84988a2538bb94c3da68818` |

## 许可证与 SBOM

- Go 模块清单与第三方许可见 `THIRD_PARTY.md`、`licenses/` 与 `go.sum`。
- 客体运行时/工具链的来源、许可证与固定摘要见 `THIRD_PARTY.md`。
- `make release` 生成 `dist/SBOM.txt`,内容为 `go version -m` 的模块构建
  信息;发布流水线必须将该文件与 `dist/SHA256SUMS` 一并保存。
- Release 工作流通过 `gh release download` 下载到空目录，运行
  `testdata/release/verify-download.sh` 核对摘要、架构、版本与 SBOM，
  再以 `PATH=/nonexistent` 在特权容器中对下载的 linux/amd64 默认 CLI
  执行真实 PTY 验收；不传后端参数，也不依赖宿主 Node/Python。

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
`dist/assistant-sandbox`、`dist/SHA256SUMS` 与 `dist/SBOM.txt`;
缺少任一文件不得宣称完整发布。
