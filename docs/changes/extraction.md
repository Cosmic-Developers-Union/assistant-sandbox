# 独立项目本地拆分

## 实施

依据[需求](../requirements/extraction.md)和[决策](../adr/standalone-module.md)，
将沙箱源码、客体构建器和 POSIX 开发测试整理为独立 Go 模块。
模块依赖经 `go mod tidy` 收敛，不包含原 assistant 模块。

构造器通过 `Config.ShellArchive` 接收客体，通过 `Config.MessageSource` 接收
宿主消息。消息适配与 agent 发布流程的集成测试继续由内部项目负责。
保留 golangci-lint 配置和 lefthook 提交检查，新增 GitHub 测试与标签发布工作流。

## 本地验证

以下命令使用 `GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=768MiB` 限制构建并发：

- `go run ./cmd/assistant-sandbox-build` 成功生成 WASI 客体。
- 移走生成的 gzip 客体后，`go build -buildvcs=false ./...` 成功；验证源码模块编译不依赖生成物。
- `golangci-lint run` 输出 `0 issues.`，`golangci-lint config verify` 通过。
- `go test . ./internal/build ./vm -run '^(TestMessageSnapshotAndWASIRefresh|TestNewRequiresExplicitArchive|TestDependencySourceDownloadsBeforeLocating|TestVMProductionCodeHasNoAmbientHostImports)$' -count=1` 通过。
- 独立 CLI 的 `--help`、`--dry-run` 和真实客体脚本 `printf sandbox-ok` 成功。
- 两份 GitHub workflow 的 YAML 解析与内联脚本 `bash -n` 检查通过。

完整 Go 测试和 POSIX 检查未在本机执行，已配置到 GitHub CI。
公开仓库为 https://github.com/Cosmic-Developers-Union/assistant-sandbox 。
独立命令为 `assistant-sandbox`，客体构建入口为 `cmd/assistant-sandbox-build`，
构建器位于 `internal/build`。完整测试与发布由 GitHub Actions 执行。
