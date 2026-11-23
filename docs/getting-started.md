# 独立开发入门(R09)

本文从全新检出开始,验证构建 CLI、创建 VM、运行 Bash/Python/Node
并关闭。所有命令都可在仓库根目录执行,CI 的 `getting-started` 任务
运行同一组命令。

## 1. 安装工具链

需要 Go 版本与 [go.mod](../go.mod) 的 `go` 指令一致;CI 经
`setup-go` 的 `go-version-file` 安装。检查:

```sh
make toolchain
```

构建 CLI 和 WASI 兼容客体入口:

```sh
make build
```

产物为 `assistant-sandbox` 与 `assistant-bash.wasm.gz`。CLI 的单次
执行使用 Rust/Bash 兼容入口之外的新 VM 内核;`--module` 仍被接受但
不再读取 WASI 模块。

## 2. 创建 VM 并运行 Bash

```sh
./assistant-sandbox --dir "$PWD" -c 'printf bash-ok'
```

`--dir` 只挂载授权工作区;单次命令执行结束后 CLI 负责 Shutdown。
持续会话使用 `--session` 逐行执行,或用下方 Go API。

在交互终端直接运行 `./assistant-sandbox --dir "$PWD"` 会自动进入同一
逐行会话(输入 `Ctrl-D` 退出);从管道或文件输入且未指定 `-c` 时,
仍会把 stdin 作为完整脚本读取到 EOF。

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
