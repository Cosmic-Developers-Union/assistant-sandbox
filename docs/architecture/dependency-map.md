# 包职责与调用链盘点(A01)

本文回答:每个包归谁负责、从 CLI/SDK 到 CPU、ABI、内核、存储的实际调用链是
什么。逐项链接源码;依赖关系可用文中命令重新生成复核。

## 依赖数据生成方式

模块内直接依赖图由以下命令生成:

```sh
go list -f '{{.ImportPath}} {{range .Imports}}{{.}};{{end}}' ./... \
  | awk '{printf "%s |", $1; for (i=2; i<=NF; i++) if (index($i, "Cosmic-Developers") != 0) \
    printf " %s", substr($i, 48); print ""}'
```

## 分层总览

```
cmd/assistant-sandbox
  ├── instance ──> guest/native、guest/devroot、platform/filesystem、vm/*
  └── deployment/native ──> contract、inspect、executor/elf、userland/shell、vm/*
                              ├── executor/elf ──> loader/elf ──> vm/memory
                              └── abi/linux/riscv64 ──> abi/linux/common ──> vm/*

vm.Kernel ──> vm/{proc,fd,signal,memory,fs,scheduler,wait,pipe,…}
          ├── filesystem/{overlay,mountfs,memfs,devfs,procfs,readonly,…}
          └── platform/{filesystem,entropy,network} ──> 宿主边界
```

## 入口与 SDK

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `cmd/assistant-sandbox` | Linux ABI VM CLI(单次执行/持续会话);运行内嵌 riscv64 Bash/BusyBox | [main.go](../../cmd/assistant-sandbox/main.go) | `guest/native`、`instance`、`platform/filesystem` |
| `instance` | 创建并管理 VM、CPU/内存配置、开发根与生命周期 | [vm.go](../../instance/vm.go) | `guest/devroot`、`guest/rootfs`、`vm/*` |
| `model` | 面向 SDK 的 VM/Console/Terminal 契约 | [model](../../model) | 无(仅标准库) |
| `deployment/native` | 旧 SDK 契约的 native 装配:memfs、虚拟时钟、种子熵、ELF 工厂与 shell 注册表 | [native.go](../../deployment/native/native.go) | `contract`、`executor/elf`、`userland/shell`、`vm/*` |

## 契约与检查

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `contract` | Session 级契约:Exec/Wait/Inspect/Close 行为向量 | [contract.go](../../contract/contract.go)、[suite.go](../../contract/suite.go) | 无(仅标准库) |
| `inspect` | 内核只读检查(进程表、fd、预算) | [inspect.go](../../inspect/inspect.go) | `vm`、`vm/proc` |
| `internal/metadata` | 模块、依赖许可与宿主能力边界审计 | [hostcap_test.go](../../internal/metadata/hostcap_test.go) | 无(测试基础设施) |

## 执行后端

| 包 | 职责 | 关键源码 | 直接内部依赖 |
| --- | --- | --- | --- |
| `executor/elf` | 将静态 Linux ELF 接入内核进程 | [program.go](../../executor/elf/program.go) | `loader/elf`、`vm/*` |
| `executor/cpu/riscv64` | RV64GC 解释器与受控本地加速路径 | [core.go](../../executor/cpu/riscv64/core.go) | `vm/memory` |
| `loader/elf` | ELF 校验、段映射与装载 | [elf.go](../../loader/elf/elf.go) | `vm/{errno,memory}` |
| `abi/linux/riscv64` | riscv64 系统调用号与 ABI 适配 | [adapter.go](../../abi/linux/riscv64/adapter.go) | `abi/linux/common`、`executor/cpu/riscv64` |
| `abi/linux/common` | asm-generic 系统调用语义与内核端口分派 | [registry.go](../../abi/linux/common/registry.go) | `vm/*` |

## 内核、文件系统与宿主边界

| 包 | 职责 | 关键源码 |
| --- | --- | --- |
| `vm` | 进程、调度、等待、信号、管道、FD、内存与资源计量 | [kernel.go](../../vm/kernel.go) |
| `filesystem/memfs`、`filesystem/overlay`、`filesystem/mountfs` | 内存文件系统、写时复制覆盖层与挂载表 | [memfs](../../filesystem/memfs)、[overlay](../../filesystem/overlay)、[mountfs](../../filesystem/mountfs) |
| `filesystem/devfs`、`filesystem/procfs` | 设备文件(null/zero/random)与进程文件系统 | [devfs.go](../../filesystem/devfs/devfs.go)、[procfs.go](../../filesystem/procfs/procfs.go) |
| `platform/filesystem` | 授权宿主目录导入(`os.Root` 边界、能力、配额) | [filesystem.go](../../platform/filesystem/filesystem.go) |
| `platform/entropy`、`platform/network` | 宿主熵源与显式网络出口 | [entropy](../../platform/entropy)、[network](../../platform/network) |

## 客体与用户态

| 包 | 职责 | 关键源码 |
| --- | --- | --- |
| `guest/native` | CLI 内嵌的 riscv64 Bash/BusyBox sh、musl 与终端库资产安装 | [shell.go](../../guest/native/shell.go) |
| `guest/devroot`、`guest/rootfs` | `--tools=dev` 开发根与 SDK 根文件系统装配 | [install.go](../../guest/devroot/install.go)、[rootfs.go](../../guest/rootfs/rootfs.go) |
| `userland/shell` | mvdan.sh 解释器槽位;保留给旧 SDK/模型契约 | [shell.go](../../userland/shell/shell.go)、[slot.go](../../userland/shell/slot.go) |
| `userland/commands/*` | POSIX 工具的 Go 内建实现,供旧契约路径使用 | [registry.go](../../userland/commands/registry.go) |
| `userland/commandio`、`userland/toolcompat/*` | 命令 I/O 约定与 libc 行为兼容层 | [commandio](../../userland/commandio)、[toolcompat](../../userland/toolcompat) |

## 主调用链

1. `cmd/assistant-sandbox` 经 `instance.New` 创建 VM,把显式 `--dir` 挂载到
   `/work`,再从内嵌资产启动真实 Bash 或 BusyBox sh。
2. `vm.New` 创建 Kernel;`kernel.Begin` 登记进程,`RunRegistered` 驱动程序。
3. 程序为 ELF 时经 `executor/elf.Factory` 与 `loader/elf` 装载段到 `vm/memory`。
4. `executor/cpu/riscv64.Core.Step` 解释指令;`ECALL` 陷入
   `abi/linux/riscv64` 并分派到 `abi/linux/common`。
5. 系统调用语义落到内核能力端口:文件 → `filesystem/*` 实现 `vm/fs`;
   描述符 → `vm/fd`;信号 → `vm/signal`;等待 → `vm/proc` + `vm/wait`。

历史 WASI 链(`internal/legacy` → `executor/wasm` → wazero)及其
`assistant-bash.wasm.gz` 发布物已删除;当前只有 Linux ABI 执行路径。
