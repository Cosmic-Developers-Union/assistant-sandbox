# 独立开发入门(R09)

本文从全新检出开始,验证构建 CLI、创建 VM、运行 shell/Python/Node
并关闭。所有命令都可在仓库根目录执行,CI 的 `getting-started` 任务
运行同一组命令。

## 1. 安装工具链

需要 Go 版本与 [go.mod](../go.mod) 的 `go` 指令一致;CI 经
`setup-go` 的 `go-version-file` 安装。Linux 默认构建还要求 C++17
编译器(`g++`)以启用批量整数执行核心;无 C++ 编译器时可用
`CGO_ENABLED=0 make build` 退回纯 Go。检查:

```sh
make toolchain
```

构建 CLI 和 WASI 兼容客体入口:

```sh
make build
```

产物为 `assistant-sandbox` 与 `assistant-bash.wasm.gz`。CLI 的单次
执行使用新的 Linux ABI VM 内核;`--module` 只作为旧 WASI CLI 的兼容
参数被接受,新 CLI 不会读取 WASI 模块。`assistant-bash.wasm.gz` 保留
给 SDK/旧入口使用。CLI 自身内嵌真实 riscv64 GNU Bash 5.2.37 与
BusyBox 1.37.0 sh,不依赖宿主 shell。

`make build` 在 Linux 上生成静态链接的 C++ 加速 CLI;`CGO_ENABLED=0`
构建不带 C++ 快速路径,适合无可移植 C++ 工具链的环境。

## 2. 创建 VM 并运行 Bash

```sh
./assistant-sandbox --dir "$PWD" -c 'printf bash-ok'
./assistant-sandbox --dir "$PWD" --shell sh -c 'printf sh-ok'
```

默认 `--shell bash` 执行内嵌的 GNU Bash;`--shell sh` 执行 BusyBox sh。
`zsh` 暂不支持,因为没有对应的 riscv64 客体资产。旧的
`userland/shell` mvdan/sh 槽位已废弃,只保留给既有 SDK/模型契约。

VM 默认 2 个 CPU、4 GiB 内存。`--cpus` 支持 1–8,`--memory` 支持
256 MiB–16 GiB 的任意规范值:

```sh
./assistant-sandbox --cpus 4 --memory 1.5g -c 'nproc; free -h'
```

`--dir` 只挂载显式授权的宿主工作区;省略 `--dir` 时 `/work` 是临时
内存目录,不会把当前目录映射进 VM。单次命令执行结束后 CLI 负责
Shutdown。持续会话使用 `--session` 运行一个原生 shell 进程,或用
下方 Go API。

在交互终端直接运行 `./assistant-sandbox --dir "$PWD"` 会自动进入同一
原生 Bash 会话(输入 `Ctrl-D` 退出);从管道或文件输入且未指定 `-c`
时,仍会把 stdin 作为完整脚本读取到 EOF。交互会话经 VM PTY 支持终端
回显、控制字符与作业控制;Bash 使用内嵌 readline 提供左右编辑、
命令历史和 Tab 补全,Python/Node REPL 的 readline 也直接工作。CLI 会在
`SIGINT`/`SIGTERM`/`SIGHUP`/`SIGQUIT`
时恢复宿主终端;若终端模拟器崩溃或进程被 `SIGKILL`,先运行
`stty sane` 或 `reset`。终端兼容问题可用 `--no-pty` 退回管道会话。
可重复的脚本路径应使用 `-c`。

## 3. 运行真实 CPython 与 Node.js

固定版本运行时资产随 `internal/guest/testdata` 提供;以下测试在
客体内核中执行真实 riscv64 ELF,不调用宿主解释器:

```sh
go test ./internal/guest/ -run TestPython312MinimalStartup -count=1
go test ./internal/guest/ -run TestNode22MinimalStartup -count=1
```

Python 测试解包固定 CPython 3.12.16 APK;Node 测试解包固定
Node.js 22.23.5 运行时。完整语言场景见
[`internal/guest/python_q*_test.go`](../internal/guest) 与
[`internal/guest/node_q*_test.go`](../internal/guest)。

## 4. 用 Go API 创建、使用和关闭 VM

```go
ctx := context.Background()
machine, err := native.NewModelVM(ctx, native.Options{Seed: 1})
if err != nil {
	panic(err)
}
defer func() { _ = machine.Shutdown(ctx) }()

if err := machine.Filesystem().WriteFile(ctx, "note.txt", []byte("hello")); err != nil {
	panic(err)
}
terminal, err := machine.Connect(ctx)
if err != nil {
	panic(err)
}
result, err := terminal.Bash().Run(ctx, "printf bash-ok")
if err != nil {
	panic(err)
}
fmt.Println(result.Output) // bash-ok
```

可运行版本见
[`deployment/native/example_test.go`](../deployment/native/example_test.go)。
错误用 `model.ErrClosed` 等判别;`Shutdown` 幂等并等待客体资源回收。

## 5. 一键复验

```sh
make getting-started
```

该目标依次构建 CLI、执行 Bash 命令、运行公开 model 示例、运行
Python/Node 最小启动测试;CI 的 `getting-started` 任务执行同一目标。
