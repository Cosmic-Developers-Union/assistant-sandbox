# 独立项目本地拆分

## 实施

依据[需求](../requirements/extraction.md)和[决策](../adr/standalone-module.md)，
将沙箱源码、客体构建器和 POSIX 开发测试整理为独立 Go 模块。
模块依赖经 `go mod tidy` 收敛，不包含原 assistant 模块。

SDK 通过 `instance` 与 `model` 提供 Linux ABI VM。历史 WASI 客体构建器、
`Config.ShellArchive` 入口和 POSIX WASI gate 已移除；发布只包含 CLI。
保留 golangci-lint 配置和 lefthook 提交检查，新增 GitHub 测试与标签发布工作流。

## 本地验证

以下命令使用 `GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=768MiB` 限制构建并发：

- `go build -buildvcs=false ./...` 成功，源码模块不依赖生成产物。
- `golangci-lint run` 输出 `0 issues.`，`golangci-lint config verify` 通过。
- `go test ./internal/metadata ./deployment/native -count=1` 通过。
- 独立 CLI 的 `--help`、`--dry-run` 和真实客体脚本 `printf sandbox-ok` 成功。
- 两份 GitHub workflow 的 YAML 解析与内联脚本 `bash -n` 检查通过。

完整 Go 测试和 POSIX 检查未在本机执行，已配置到 GitHub CI。
公开仓库为 https://github.com/Cosmic-Developers-Union/assistant-sandbox 。
独立命令为 `assistant-sandbox`。完整测试与发布由 GitHub Actions 执行。
