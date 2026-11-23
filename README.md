# assistant-sandbox

Go 沙箱 SDK、WASI Bash 客体和独立执行入口。模块路径为
`github.com/Cosmic-Developers-Union/assistant-sandbox`。

当前主线是 Linux ABI 虚拟机:客体 CPU/地址空间、进程线程、文件系统、
设备与网络由 `vm/`、`executor/` 实现,并在客体中运行真实 CPython 与
Node.js。新检出请先读 [独立开发入门](docs/getting-started.md),其中
给出构建、创建 VM、运行 Bash/Python/Node 与关闭的完整命令
(`make getting-started`)。

SDK 不内嵌生成物，不依赖 assistant 的内部包。`Config.ShellArchive` 接收 gzip
压缩的 WASM，宿主自行加载固定版本的 Release 资源；`Config.MessageSource`
提供本轮消息文本和 JSON 消息记录。SDK 不自行联网下载客体。

```go
archive, err := os.ReadFile("assistant-bash.wasm.gz")
if err != nil {
    return err
}
runtime, err := sandbox.New(ctx, sandbox.Config{ShellArchive: archive})
if err != nil {
    return err
}
defer runtime.Close()
result, err := runtime.Execute(ctx, workspace, "printf hello", stdout, stderr)
```

Go 版本见 [go.mod](go.mod)。在项目根目录构建和运行：

```sh
make build
./assistant-sandbox --module ./assistant-bash.wasm.gz --dir . -c 'printf hello'
make install
assistant-sandbox --dir . -c 'printf hello'
```

在终端直接运行 `assistant-sandbox --dir .` 会进入逐行交互会话；
一次性脚本请用 `-c`，或把脚本从管道传入。
默认不挂载宿主目录：只有显式传入 `--dir` 才把该目录映射到客体
`/work`；不传时使用临时内存工作区。

`make install` 将 CLI 安装到 `$(PREFIX)/bin`，客体安装到
`$(PREFIX)/share/assistant-sandbox`，默认前缀为 `$HOME/.local`。
仅安装 Go CLI 时需要通过 `--module` 指定客体文件。`--help` 和 `--dry-run`
无需加载客体。单次 CLI 执行支持 `-c` 或从标准输入读取脚本；持续会话使用 SDK 的
`Runtime.Bind` 和 `Session.Execute`。

[测试工作流](.github/workflows/test.yml)在 GitHub 标准托管 runner 上执行客体构建、
golangci-lint、Go 测试和 POSIX 开发检查；[发布工作流](.github/workflows/release.yml)
在 `v*` 标签上构建客体、多平台 CLI 和 SHA-256 清单，上传 GitHub Release。
生成物由 [.gitignore](.gitignore) 排除，源码边界检查拒绝将客体加入 Git。

开发时安装 lefthook 后执行 `lefthook install`，提交钩子执行 `golangci-lint run`。
`make lint` 执行静态检查，`make test` 先生成客体再运行测试。
完整测试和 POSIX 检查由 GitHub CI 承担。

项目原创代码采用 [GNU AGPL v3](LICENSE)（`AGPL-3.0-only`）。
第三方代码保留其原有许可，来源见 [THIRD_PARTY.md](THIRD_PARTY.md)。
拆分范围与验收见 [需求](docs/requirements/extraction.md)和[决策](docs/adr/standalone-module.md)。
