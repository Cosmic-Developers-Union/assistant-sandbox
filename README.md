# assistant-sandbox

Go 沙箱 SDK、Linux ABI 虚拟机和独立执行入口。模块路径为
`github.com/Cosmic-Developers-Union/assistant-sandbox`。

当前主线是 Linux ABI 虚拟机:客体 CPU/地址空间、进程线程、文件系统、
设备与网络由 `vm/`、`executor/` 实现,并在客体中运行真实 CPython 与
Node.js。CLI 内嵌真实 riscv64 GNU Bash 5.2.37 和 BusyBox sh;新检出
请先读 [独立开发入门](docs/getting-started.md),其中给出构建、创建
VM、运行 shell/Python/Node 与关闭的完整命令
(`make getting-started`)。

SDK 的 WASI 兼容入口不内嵌生成物，也不依赖 assistant 的内部包。
`Config.ShellArchive` 接收 gzip
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
./assistant-sandbox --dir . -c 'printf hello'
./assistant-sandbox --dir . --shell sh -c 'printf hello'
make install
assistant-sandbox --dir . -c 'printf hello'
```

CLI 默认执行客体中的真实 Bash；`--shell sh` 切换到 BusyBox sh。
`zsh` 暂不提供 riscv64 客体资产，会返回明确错误；`userland/shell` 的
内建 mvdan/sh 槽位已标记为废弃，只为旧 SDK/模型契约保留。

在终端直接运行 `assistant-sandbox --dir .` 会进入客体 Bash 会话；
交互会话通过 VM PTY 提供回显、`Ctrl-C`/`Ctrl-D` 与作业控制。Bash
当前使用 `--noediting`，CLI 在宿主侧提供上下键命令历史和命令级 Tab
补全；readline 左右编辑与参数补全暂不可用。脚本和 CI 建议使用 `-c`
或从管道传入完整脚本。
CLI 会捕获 `SIGINT`/`SIGTERM`/`SIGHUP`/`SIGQUIT` 并恢复宿主终端；
若终端模拟器自身崩溃或 CLI 被 `SIGKILL` 强制终止，可先执行
`stty sane` 或 `reset`。终端兼容问题可用 `--no-pty` 退回普通管道会话。
默认不挂载宿主目录：只有显式传入 `--dir` 才把该目录映射到客体
`/work`；不传时使用临时内存工作区。

`make install` 将 CLI 安装到 `$(PREFIX)/bin`，客体安装到
`$(PREFIX)/share/assistant-sandbox`，默认前缀为 `$HOME/.local`。
`--module` 是旧 WASI CLI 的兼容参数，新原生 VM CLI 会接受但忽略它；
`assistant-bash.wasm.gz` 仍作为 SDK/旧入口产物保留，不由新 CLI 加载。
`--help` 和 `--dry-run` 无需加载客体。单次 CLI 执行支持 `-c` 或从标准输入读取脚本；
`--session` 在同一个 VM 中运行一个持续的原生 shell 进程。

[测试工作流](.github/workflows/test.yml)在 GitHub 标准托管 runner 上执行客体构建、
golangci-lint、Go 测试和 POSIX 开发检查；[发布工作流](.github/workflows/release.yml)
在 `v*` 标签上构建客体、多平台 CLI 和 SHA-256 清单，上传 GitHub Release。
WASI 构建产物由 [.gitignore](.gitignore) 排除，源码边界检查拒绝将其加入 Git。
固定的 N10 原生 shell 资产作为 `go:embed` 输入直接纳入源码，并由
[THIRD_PARTY.md](THIRD_PARTY.md) 记录版本、摘要与许可。

开发时安装 lefthook 后执行 `lefthook install`，提交钩子执行 `golangci-lint run`。
`make lint` 执行静态检查，`make test` 先生成客体再运行测试。
完整测试和 POSIX 检查由 GitHub CI 承担。

项目原创代码采用 [GNU AGPL v3](LICENSE)（`AGPL-3.0-only`）。
第三方代码保留其原有许可，来源见 [THIRD_PARTY.md](THIRD_PARTY.md)。
拆分范围与验收见 [需求](docs/requirements/extraction.md)和[决策](docs/adr/standalone-module.md)。
