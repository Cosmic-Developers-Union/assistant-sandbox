# 包职责与调用链盘点(A01)

本文回答:每个包归谁负责、从公开 SDK 到 CPU、ABI、内核、存储的实际调用链是什么、
哪些能力存在重复实现。逐项链接源码;依赖关系可用文中命令重新生成复核。

## 依赖数据生成方式

模块内直接依赖图由以下命令生成(与本文表格同源):

```sh
go list -f '{{.ImportPath}} {{range .Imports}}{{.}};{{end}}' ./... \
  | awk '{printf "%s |", $1; for (i=2; i<=NF; i++) if (index($i, "Cosmic-Developers") != 0) \
    printf " %s", substr($i, 48); print ""}'
```

全量包清单:`go list ./...`(203 个包,其中约 140 个为 `userland/commands/*` POSIX 工具)。

## 分层总览

```
cmd/assistant-sandbox ─┐
cmd/assistant-sandbox-build ──> internal/build
                       │
test/posix ────────────┼──> (internal/legacy: wasm.go / session.go / executor.go)
                       │         │
                       │         ├──> deployment/wasm ──> deployment(矩阵)
                       │         │        └──> executor/wasm(wazero WASI)──> vm/executable
                       │         │
                       │         └──(旁路,尚未经根包公开) deployment/native
                       │                  ├──> contract(旧 Session 契约)+ inspect
                       │                  ├──> executor/elf(RISC-V ELF)──> abi/linux/*
                       │                  ├──> userland/shell(mvdan sh 解释器)
                       │                  └──> vm.Kernel
                       │
        ┌──────────────┴──────────────────────────────────────────┐
        │                      vm.Kernel(vm/kernel.go)             │
        └──> vm/{proc,fd,signal,memory,fs,scheduler,wait,pipe, …} │
             abi/linux/common ──> abi/linux/riscv64 ──> executor/cpu/riscv64
             filesystem/{overlay,mountfs,memfs,devfs,procfs,readonly,…} ──> vm/fs
             platform/{filesystem,entropy,modules} ──> 宿主边界
```

## 各层职责与关键源码

### 入口与 SDK(根包 + cmd)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `internal/legacy` | 已退役 WASI Runtime/Session,仅 POSIX gate 显式调用;公开 VM 入口为 `instance`/`model` | [wasm.go](../../internal/legacy/wasm.go)、[session.go](../../internal/legacy/session.go)、[executor.go](../../internal/legacy/executor.go)、[fs.go](../../internal/legacy/fs.go)、[modules.go](../../internal/legacy/modules.go)、[messages.go](../../internal/legacy/messages.go)、[stream.go](../../internal/legacy/stream.go) | `internal/protocol` |
| `cmd/assistant-sandbox` | CLI(单次执行/持续会话),调用根包 SDK | [main.go](../../cmd/assistant-sandbox/main.go) | 根包 |
| `cmd/assistant-sandbox-build` | 客体构建入口(生成 WASI bash 客体) | [main.go](../../cmd/assistant-sandbox-build/main.go) | `internal/build` |
| `internal/build` | 客体构建器:Go 工具链交叉编译 wasip1 + gzip;可重复构建 | [build.go](../../internal/build/build.go)、[interpreter.go](../../internal/build/interpreter.go) | 无(独立) |
| `internal/protocol` | SDK 与 WASI 客体间的私有线协议 | [types.go](../../internal/protocol/types.go) | 无 |
| `internal/testutil` | 测试装配(内核/能力夹具、故障注入) | [run.go](../../internal/testutil/run.go)、[fault.go](../../internal/testutil/fault.go) | 平台与 vm 基础包 |

### 契约与检查

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `contract` | 旧一代 Session 级契约:Exec/Wait/Inspect/Close 行为向量,接收工厂 | [contract.go](../../contract/contract.go)、[suite.go](../../contract/suite.go) | 无(仅标准库) |
| `inspect` | 内核只读检查(进程表、fd、预算),供 API 快照 | [inspect.go](../../inspect/inspect.go) | `vm`、`vm/proc` |
| `compat` | 预留目录(当前无 Go 源;兼容配置文档化于 docs,见 A03) | — | — |

### 部署装配(deployment)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `deployment` | 后端能力矩阵声明(Deploy/Executors/Signals/…) | [deployment.go](../../deployment/deployment.go) | 无 |
| `deployment/wasm` | 旧 WASI 后端装配:把根包 Runtime 呈现为矩阵 | [wasm.go](../../deployment/wasm/wasm.go) | `deployment` |
| `deployment/native` | 新原生 VM 装配:memfs + 虚拟时钟 + 种子熵 + vm.Kernel + ELF 工厂 + shell 注册表;对外暴露旧契约 Session | [native.go](../../deployment/native/native.go) | `contract`、`executor/elf`、`filesystem/memfs`、`inspect`、`userland/shell`、`vm/*` 全家 |
| `deployment/layer` | 每会话可写层(基础镜像 + 会话覆盖 + 快照恢复) | [layer.go](../../deployment/layer/layer.go) | `filesystem/memfs`、`filesystem/readonly`、`vm`、`vm/image` 等 |
| `deployment/auditjsonl` | 审计日志 JSONL 序列化 | [sink.go](../../deployment/auditjsonl/sink.go) | `vm/audit`、`vm/errno` |

### 执行器(executor)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `executor`(接口) | `Executor.Execute` 统一执行入口 | [executor.go](../../executor.go) | `vm/executable` |
| `executor/wasm` | 旧后端:wazero 运行 WASI 客体,宿主侧系统调用 | [engine_host.go](../../executor/wasm/engine_host.go)、[module.go](../../executor/wasm/module.go)、[host.go](../../executor/wasm/host.go) | `vm/errno`、`vm/signal` |
| `executor/elf` | 新后端:静态 RISC-V ELF 加载与解释执行,装配 Linux ABI 适配层 | [program.go](../../executor/elf/program.go)、[mapper.go](../../executor/elf/mapper.go) | `abi/linux/*`、`executor/cpu/*`、`loader/elf`、`vm/{errno,executable,fd,memory,proc,resources,signal}` |
| `executor/cpu` | CPU 抽象契约(寄存器、内存访问、边界钩子) | [contract.go](../../executor/cpu/contract.go) | `vm/errno` |
| `executor/cpu/riscv64` | RV64 解释器:整数/乘除/AMO/LR-SC/压缩/浮点/CSR | [core.go](../../executor/cpu/riscv64/core.go)、[compressed.go](../../executor/cpu/riscv64/compressed.go)、[float.go](../../executor/cpu/riscv64/float.go) | `executor/cpu`、`vm/errno` |

### Linux ABI(abi)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `abi/linux/common` | 架构无关系统调用语义:登记表、参数复制、结构体编码、信号帧、文件/等待/时钟处理器 | [registry.go](../../abi/linux/common/registry.go)、[syscalls.go](../../abi/linux/common/syscalls.go)、[structs.go](../../abi/linux/common/structs.go)、[sigframe.go](../../abi/linux/common/sigframe.go)、[abi.go](../../abi/linux/common/abi.go) | `executor/cpu`、`vm/{clock,errno,eventfd,fd,fs,memory}` |
| `abi/linux/riscv64` | 编号到语义的适配(a0–a5/a0 返回、负 errno) | [adapter.go](../../abi/linux/riscv64/adapter.go) | `abi/linux/common`、`executor/cpu`、`vm/errno` |
| `abi/wasi` | 预留目录(preview1/preview2/component,当前无 Go 源;旧后端 WASI 适配实际位于 `executor/wasm` 的宿主调用层) | — | — |

### 装载器(loader)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `loader/elf` | 静态 ELF 校验与段装载(同页簇、W^X、预算) | [elf.go](../../loader/elf/elf.go)、[image.go](../../loader/elf/image.go) | `vm/errno`、`vm/memory` |

### 内核(vm)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `vm`(根) | Kernel:进程表、Begin/Run/Finish/Wait/Fork/Signal、关闭语义 | [kernel.go](../../vm/kernel.go)、[jobs.go](../../vm/jobs.go) | 下述全部内核子包 |
| `vm/proc` | 进程实体与状态机、PID、父子、快照 | [proc.go](../../vm/proc/proc.go)、[context.go](../../vm/proc/context.go)、[snapshot.go](../../vm/proc/snapshot.go) | `vm/{capability,errno,fd,memory,signal}` |
| `vm/fd` | 描述符表(分配、fork 复制、dup、eventfd 接入) | [table.go](../../vm/fd/table.go) | `vm/{errno,eventfd}` |
| `vm/signal` | 每进程信号状态(掩码/待处理/处置/备用栈) | [signal.go](../../vm/signal/signal.go) | `vm/errno` |
| `vm/memory` | 地址空间:区间表、Map/Unmap/Protect/Resize/Move/Clone、守卫 | [table.go](../../vm/memory/table.go)、[guard.go](../../vm/memory/guard.go)、[contract.go](../../vm/memory/contract.go) | `vm/errno` |
| `vm/fs` | 文件系统契约(System/Face/Capability/Metadata/Capacity) | [contract.go](../../vm/fs/contract.go) | 无 |
| `vm/scheduler` | 确定性调度(Sequencer 与策略) | [scheduler.go](../../vm/scheduler/scheduler.go)、[policy.go](../../vm/scheduler/policy.go) | `vm/errno`、`vm/signal` |
| `vm/wait` | 等待队列原语 | [wait.go](../../vm/wait/wait.go) | `vm/errno` |
| `vm/pipe`、`vm/eventfd`、`vm/poll` | 管道、eventfd、poll 就绪原语 | [pipe.go](../../vm/pipe/pipe.go)、[eventfd.go](../../vm/eventfd/eventfd.go)、[poll.go](../../vm/poll/poll.go) | `vm/{errno,wait}` 等 |
| `vm/tty` | TTY 行规程与屏幕模型 | [tty.go](../../vm/tty/tty.go) | `vm/{errno,fd,poll,proc,signal,wait}` |
| `vm/clock`、`vm/entropy` | 时钟源(虚拟/录制/生产)与熵源(种子/计数/录制) | [virtual.go](../../vm/clock/virtual.go)、[seeded.go](../../vm/entropy/seeded.go) | `vm/{errno,replay}` |
| `vm/resources`、`vm/identity`、`vm/image`、`vm/storage`、`vm/audit`、`vm/bridge`、`vm/replay`、`vm/capability`、`vm/executable`、`vm/job`、`vm/ipc`、`vm/language`、`vm/net`、`vm/digest`、`vm/errno` | 配额计量、身份、镜像摘要、存储、审计、桥接、录制重放、能力集、程序注册表、作业、IPC、语言运行时钩子、网络占位、内容摘要、错误码 | 各包根文件 | 见 `go list` 输出 |

### 存储后端(filesystem)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `filesystem/memfs` | 内存文件系统(目录/符号链接/硬链接/配额/句柄) | [memfs.go](../../filesystem/memfs/memfs.go)、[file.go](../../filesystem/memfs/file.go)、[capacity.go](../../filesystem/memfs/capacity.go) | `vm/{clock,errno,fs}` |
| `filesystem/overlay`、`filesystem/readonly`、`filesystem/mountfs` | 写时复制覆盖层、只读包装、挂载表 | [overlay.go](../../filesystem/overlay/overlay.go)、[readonly.go](../../filesystem/readonly/readonly.go)、[mountfs.go](../../filesystem/mountfs/mountfs.go) | `vm/{errno,fs}` |
| `filesystem/devfs`、`filesystem/procfs` | 设备文件(null/zero/random)与进程文件系统 | [devfs.go](../../filesystem/devfs/devfs.go)、[procfs.go](../../filesystem/procfs/procfs.go) | `vm/{errno,fs}`(procfs 另依赖 `vm/proc`) |
| `filesystem/persistent`、`filesystem/blobfs`、`filesystem/packagefs` | 宿主持久化存储、内容寻址 blob、包镜像 | [store.go](../../filesystem/persistent/store.go) 等 | `vm/{digest,errno,fs}` |

### 宿主边界(platform)与客体(guest)

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `platform/filesystem` | 授权目录导入(os.Root 边界、能力、配额) | [filesystem.go](../../platform/filesystem/filesystem.go) | `vm/fs` |
| `platform/entropy`、`platform/modules` | 宿主熵源(native/wasi 双构建)、扩展模块宿主 | [entropy_native.go](../../platform/entropy/entropy_native.go)、[modules.go](../../platform/modules/modules.go) | `vm/errno` |
| `guest`、`guest/cmd` | WASI 客体(bash 替身 + boot),由 internal/build 构建 | [boot.go](../../guest/boot.go)、[main.go](../../guest/cmd/main.go) | `executor/elf`、`platform/*`、`userland/shell`、`vm/*` |
| `userland/shell` | mvdan.sh 解释器装配为客体/注册表程序(旧后端用户态) | [shell.go](../../userland/shell/shell.go)、[loop.go](../../userland/shell/loop.go) | `internal/protocol`、`userland/commandio`、`userland/commands`、`vm/*` |
| `userland/commands/*`(~140 包) | POSIX 工具的 Go 内建实现(cat/ls/grep/awk/…),仅旧 WASI 后端使用 | [registry.go](../../userland/commands/registry.go) | `userland/commandio` 等 |
| `userland/toolcompat/*` | 上述工具的 libc 行为兼容层(locale/clock/vfs/goawk/bre) | [tool.go](../../userland/toolcompat/tool.go)、[flags.go](../../userland/toolcompat/flags.go) | `vm/{errno}` 等 |
| `userland/commandio` | 命令 I/O 约定(路径、流、退出码) | [io.go](../../userland/commandio/io.go) | `vm/{errno,fs,proc}` |

## 主调用链

以 `deployment/native` 装配的真实执行链为例(静态 RISC-V ELF):

1. `native.New`([native.go:60](../../deployment/native/native.go)) 组装 `vm.Services`(memfs、fd、身份、虚拟时钟、种子熵、计量)。
2. `vm.New`([kernel.go](../../vm/kernel.go)) 创建 Kernel;`kernel.Begin` 登记进程,`RunRegistered` 驱动程序。
3. 程序为 ELF 时经 `executor/elf.Factory` → [program.go](../../executor/elf/program.go) 用 [loader/elf](../../loader/elf/elf.go) 装载段到 `vm/memory`。
4. `executor/cpu/riscv64.Core.Step` 解释指令;`ECALL` 陷入 [abi/linux/riscv64/adapter.go](../../abi/linux/riscv64/adapter.go) 按编号分派到 [abi/linux/common](../../abi/linux/common/registry.go)。
5. 系统调用语义落到内核能力端口:文件 → `filesystem/*` 实现 `vm/fs` 契约;描述符 → `vm/fd`;信号 → `vm/signal`;等待 → `vm/proc` + `vm/wait`。

退役 WASI 链(仅 POSIX gate):`internal/legacy.New` → `executor/wasm`(wazero)→ `abi/wasi` → 同一套 `vm/{errno,signal}`;公开 VM 链为 `instance/model`。

## 重复实现清单(待 C/R 组收敛)

| # | 重复 | 位置 | 处置方向 |
| --- | --- | --- | --- |
| 1 | 两代执行后端 | `executor/wasm`(wazero/WASI)与 `executor/elf`(RISC-V 解释器) | A02 定位主后端;R02/R06 收敛公开入口 |
| 2 | 两代公开契约 | `contract`(Session:Exec/Wait/Close)与新模型 VM(Console/Connect,见 A06) | C 组/R 组统一所有权;A06/A07 迁入并工厂化 |
| 3 | 两代部署装配 | `deployment/wasm` 与 `deployment/native` | C10 拆分镜像/VM/连接;R06 迁移或删除旧入口 |
| 4 | Shell 双实现 | `userland/shell`(mvdan.sh,旧后端+native 注册表)与未来真实客体 bash | N10 打包真实 Bash 后由差分替代 |
| 5 | POSIX 工具双实现 | `userland/commands/*`(Go 内建)与真实 ELF coreutils | N10 落地后 Go 内建仅为旧后端兼容层 |
| 6 | 文件系统两套入口 | `vm/fs`(客体契约)与 `platform/filesystem`(宿主授权导入) | 语义不同非重复;I01 统一客体路径解析后边界固定 |
| 7 | 熵/时钟双路径 | `vm/{clock,entropy}`(注入)与 `platform/entropy`(宿主源) | C04 统一为 VM 拥有的设备注册表 |

## 结论

- 分层现状:`abi`、`loader`、`executor/cpu` 已与后端解耦;`vm` 内核能力端口化完成度较高。
- 公开 VM 入口已收敛到 `instance`/`model`;旧 WASI Runtime 仅保留在 `internal/legacy` 供 POSIX gate(R06)。
- 旧 WASI 路径已移出公开入口,只由 `test/posix` 显式调用;其余 R 组入口统一走新 VM。
