# goawk 引擎（客体化副本）

本目录是 **qiangli/coreutils 快照里 `third_party/goawk` 本地 MIT fork 的客体化副本**，
只服务 `userland/commands/awk`：

- 上游：`github.com/qiangli/coreutils` commit `54a8fb26235e6500f538d4b084632405be4d3758`
  的 `third_party/goawk`（`github.com/benhoyt/goawk` 的 fork，commit `ab9c4869bc2d`）。
- 许可：MIT，Copyright (c) 2022 Ben Hoyt；原文随副本保留在 `LICENSE.txt`，
  归属另存 [LICENSE.txt](LICENSE.txt)。
- 复制范围：`interp/`、`lexer/`、`parser/`、`regex/`、`internal/{ast,compiler,resolver}`；
  未复制上游根便利包 `goawk.go` 与 `internal/cover`（覆盖率采集与命令行无关）。
- import 路径按本模块重写；**唯一的功能增补是 `interp/guest.go`**（见下）。

## 本仓库的增补（`interp/guest.go`）

1. `DenyHostOpenFile`：`Config.OpenFile` 的客体桩，任何打开都报 ENOTSUP。
2. `OpenInputFunc` / `Config.OpenInput`：**只读**输入缝，让 ARGV 里的输入文件
   操作数（`awk 'prog' file`）走客体 vfs。上游没有这个字段；`OpenInput == nil`
   时行为与上游逐字一致（回退到 `OpenFile`）。

另外两处是**为满足客体约束而做的等价改写**，不新增能力：
`interp/vm.go` 的无参 `srand()` 不再读宿主时钟（保留当前种子）、
`interp/io.go` 去掉 shell 命令的 `WaitDelay`（`NoExec` 下不可达）。

宿主访问面因此收窄为：`NoExec` + `NoFileWrites` + `OpenFile` 报错桩，
唯一开口是只读输入缝。`vm/wasm_test.go` 的边界表逐文件列出这些 import 与理由。
