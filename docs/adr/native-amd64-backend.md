# linux/amd64 原生隔离后端 ADR（X01）

状态：已采纳（2026-11-27），作为
[VM 架构 ADR](vm-architecture.md) 的原生执行后端补充。取代本决策须新增 ADR，
并同步 `TODO.md` 的 X 组与发布矩阵。

## 决策

在现有 RV64GC Linux ABI VM 旁增加一个**显式 linux/amd64 原生后端**：

1. **宿主**：Linux x86-64。宿主 ARM64、非 Linux 或缺少必需内核能力时，
   自动选择回退到现有 RISC-V 后端；显式选择 `riscv64` 始终可用。
2. **客体**：固定构建的 ELF64/x86-64 Linux 根文件系统，包含 shell、动态
   加载器、Python、Node 和共享库。客体程序由宿主内核直接执行，不经
   RISC-V 解释器，也不调用宿主 PATH 中的解释器替代客体资产。
3. **隔离机制**：每次创建 VM 都建立 user、mount、PID、network、IPC、UTS
   和 cgroup namespace；在新 mount namespace 内 `pivot_root` 到每台 VM
   的根文件系统，挂载最小 `/proc`、`/dev` 与临时 `/work`。客体只看
   到自己 namespace 内的进程、回环网络和授权挂载。
4. **系统调用边界**：进入客体前应用 `no_new_privs`、丢弃 capabilities，并
   安装 seccomp 过滤器。过滤器拒绝再次创建/切换 namespace、挂载、ptrace、
   宿主进程注入、BPF、perf、keyring、host 设备与调试接口；文件、网络和
   进程能力由 namespace 与授权挂载限定。客体不能取得宿主文件描述符、
   环境或父进程控制端口。
5. **预算执行方**：使用委派目录下的 per-VM cgroup v2 实施
   `memory.max`、`cpu.max` 与 `pids.max`，关闭时以 cgroup kill 回收整个
   进程树。CPU/内存计量读取 `cpu.stat`、`memory.current` 与
   `pids.current`。不能取得可写 cgroup v2 委派时，原生后端创建失败并
   返回具名错误，**不得退回宿主无隔离执行**。
6. **执行与生命周期**：父进程是控制面，负责创建 cgroup、启动隔离子进程、
   等待与投递信号；子进程完成 namespace/根文件系统/seccomp 初始化后
   才 `execve` 客体 init。任何初始化步骤失败都关闭子进程并删除 cgroup，
   不能在宿主直接启动解释器。
7. **网络策略**：每台 VM 的 network namespace 只保留回环；出站由父进程
   中现有授权策略代理，客体没有宿主网络直通。X08 将接入受控 DNS/TCP
   出口，默认非回环目标拒绝。

## 信任边界

- 客体 CPU 为宿主 x86-64 CPU，客体内存/进程/文件/网络由 Linux namespace、
  mount、cgroup 与 seccomp 隔离。这是共享内核的用户态隔离，不声称是
  硬件虚拟机，也不声称能够启动原版 Linux 内核。
- 父进程仅持有控制管道、客体 stdio 与 cgroup/namespace 控制能力；不把
  宿主目录、环境变量或任意打开描述符继承给客体。显式 `--dir` 只产生
  一个授权 bind mount，符号链接与 `..` 不能越出该授权根。
- 客体可以消耗自己的 CPU/内存/进程配额并触发自身 OOM kill；不能读写
  其他 VM 的根文件系统、进程、回环端口或 cgroup。
- seccomp 与 namespace 初始化失败都属于 fail-closed 错误。自动选择只在
  全部探测和镜像校验通过后启用原生后端。

## RISC-V 指令预算的对应语义

RISC-V 的 `resources.Limits.Work` 是解释器退休指令数，原生 x86-64 没有
可直接对应的客体指令计数。原生后端接受：

- `MemoryBytes` → cgroup `memory.max`；
- `CPUs` → cgroup `cpu.max` 配额；
- `MaxProcesses` → cgroup `pids.max`；
- `ProcessTimeout` → 父进程墙钟监控与强制终止。

SDK 调用方若显式传入非零 `Work`，原生创建返回
`ErrNativeWorkBudgetUnsupported`，而不是静默忽略。CLI 的自动选择层把
现有 RISC-V 默认 `Work` 转换为原生 CPU/墙钟预算后再构造后端，因此默认
CLI 不需要用户理解该差异。

## SDK 可见差异

| 能力 | RISC-V 后端 | 原生 amd64 后端 |
| --- | --- | --- |
| 客体 ELF | RV64GC，解释执行 | ELF64/x86-64，宿主内核执行 |
| `process.arch` | `riscv64` | `x64` |
| CPU 预算 | 退休指令数 `Work` | cgroup CPU 配额 + 墙钟超时 |
| 内存/进程限制 | VM 内计量 | cgroup v2 |
| 系统调用追踪 | VM syscall tracer | 初始不支持 RISC-V 风格逐调用轨迹 |
| 快照/重放 | 现有安全点机制 | 初始不支持，创建时明确拒绝 |
| 宿主要求 | Go 支持平台 | linux/amd64 + user/net/cgroup/seccomp 能力 |

公开 `model.VM`、Console、Terminal、Bash 与 Filesystem 契约保持不变；差异
通过可选能力接口和具名错误暴露，不以宿主解释器伪造客体结果。

## 能力探测与实测

探测环境：

- 宿主内核：`Linux 7.0.0-43-generic`
- 宿主架构：`x86_64`
- util-linux：`2.41.3`
- cgroup：v2，用户委派目录可写
- bubblewrap：`0.11.1`（仅作为独立对照，产品实现不依赖该外部程序）
- `kernel.unprivileged_userns_clone=1`

### 探针一：成功创建隔离 namespace 并运行 x86-64 ELF

```sh
testdata/hostnative/namespace-probe.sh
```

实测输出：

```text
== minimal guest ELF ==
  Class:                             ELF64
  Data:                              2's complement, little endian
  Type:                              EXEC (Executable file)
  Machine:                           Advanced Micro Devices X86-64
== host namespaces ==
user=user:[4026531837]
mnt=mnt:[4026531832]
pid=pid:[4026531836]
net=net:[4026531833]
ipc=ipc:[4026531839]
uts=uts:[4026531838]
cgroup=cgroup:[4026531835]
== isolated namespaces ==
arch=x86_64 pid=1
user=user:[4026533928]
mnt=mnt:[4026533929]
pid=pid:[4026533977]
net=net:[4026534050]
ipc=ipc:[4026533934]
uts=uts:[4026533932]
cgroup=cgroup:[4026533984]
```

这验证了静态 x86-64 ELF 能在新的 user/mount/PID/net/IPC/UTS/cgroup
namespace 内以 PID 1 执行，且 namespace 标识与宿主不同。探针源码为
`testdata/hostnative/namespace_probe.c`，脚本会现场静态编译，不依赖
仓库中的预构建二进制。

### 探针二：缺少必需能力时明确失败

```sh
unshare --net true
```

实测输出与退出码：

```text
unshare: unshare failed: Operation not permitted
exit status 1
```

当前非特权进程不能在没有 user namespace 的情况下创建 network namespace。
后端探测必须把这类失败转换为具名 `ErrNativeCapability`，不能改为在宿主
直接执行客体。

### cgroup v2 委派探测

```sh
base=/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service
probe="$base/assistant-sandbox-probe-$$"
mkdir "$probe"
cat "$probe/cgroup.controllers"
printf '%s\n' "$$" > "$probe/cgroup.procs"
cat "$probe/cgroup.procs"
# 进程退出后 rmdir "$probe"
```

本机输出为 `cpu memory pids`，并能把当前进程写入新 cgroup。X02 的能力
探测会用临时子进程完成同样的创建/移动/删除检查，避免把控制进程留在
测试 cgroup 中。

## 进程、信号与清理（X06）

`NativeVM.Exec` 返回 `NativeProcess`，提供 `Wait`、`Signal` 与显式
`Cancel`；等待上下文到期只取消等待，不隐式杀掉进程。正常退出、非零
退出与 SIGKILL 分别记录退出码/信号。PID namespace init 收到 SIGTERM
时遵循 Linux PID 1 语义，客体 shell 可通过 trap 处理并返回约定退出码。

`Shutdown`/`Close` 幂等杀死全部活动 init 并等待宿主句柄，Linux 在 PID
namespace init 退出时清理全部后代；随后删除私有根目录。授权 bind
mount 只存在于子进程 mount namespace，随进程树退出释放。回归用例用
后台孙进程持续写授权目录的心跳，确认关闭后心跳不再变化。

## 文件系统与授权挂载（X05）

每台 `NativeVM` 在创建时解包独立根目录，默认 `/work` 与 `/tmp` 都是
该 VM 私有状态；同一 VM 的多次 Exec 共享它，不同 VM 互不可见。
`NativeVM.Mount(host, guest)` 在父进程解析宿主符号链接、验证目录并
登记授权关系；init 在 pivot_root 前按客体路径顺序 bind mount。客体
进程退出后 mount namespace 销毁，宿主授权目录改动保持可见。

挂载目标经清理并限制在客体根内；客体侧绝对符号链接仍解析到客体根，
相对符号链接的 `..` 在 bind mount 根被截断，不能访问宿主授权目录的
父目录。未登记目录、宿主环境和其他 fd 都不会进入客体。

## 隔离进程启动（X04）

`instance.NativeVM` 为每台 VM 解包一份私有根目录，并用
`os.StartProcess` 一次性创建 user/mount/PID/net/IPC/UTS/cgroup namespace。
子进程只获得显式 stdin/stdout/stderr 与 fd 3 控制管道，环境由固定最小
集合加调用方显式 `Env` 组成，不继承宿主环境或其他打开描述符。

镜像内静态 `assistant-init` 接收私有根目录路径，先设置 mount 传播、
bind/pivot_root、脱离旧根、挂载新 PID namespace 的 `/proc`，再设置
hostname、`no_new_privs`、清空 bounding/effective capabilities 并安装
seccomp denylist。只有控制管道返回 `READY` 后父进程才登记进程；任何
pivot、挂载、权限或 seccomp 失败都返回具名初始化错误并杀死子进程，
没有宿主解释器回退路径。

## 后端选择入口（X02）

`instance.Open(ctx, Options)` 是后端选择与生命周期入口，返回
`Selection{Backend, Instance}`。`Options.Backend` 支持：

- 零值：在 linux/amd64 且原生工厂已注册、全部能力探测通过时自动选择原生；
  否则回退 RISC-V。
- `riscv64`：跳过原生探测，直接创建现有 RV64 VM。
- `linux/amd64`：要求原生工厂已注册且宿主能力齐全，否则返回
  `ErrNativeArchitecture`、`ErrNativeCapability` 或 `ErrBackendUnavailable`。

能力探测在临时子进程中实际创建 user/mount/PID/net/IPC/UTS/cgroup
namespace，并检查 seccomp 状态与可写 cgroup v2 委派。探测不执行客体
程序。工厂返回 error 且携带已分配实例时，选择层会在 5 秒清理上下文内
调用 `Close`，避免初始化半途失败泄漏资源。

## 原型边界与后续任务

仓库与 `/home/ge/Projects`、`/tmp/opencode` 中未找到 TODO 所述独立 x86
命名空间原型源码或日志，`0.611 s` 观测无法复核。因此本 ADR 不把它当作
实现证据；X01 新增上述可重复探针，产品性能仍以 X12 发布构建为准。

- X02：把能力探测、后端选择和生命周期接入现有 VM 构造路径。
- X03：固定 amd64 根文件系统与摘要/许可证。
- X04–X06：实现 pivot_root、清理继承资源、进程树与信号生命周期。
- X07–X08：cgroup 预算与受控出站。
- X09–X10：PTY 会话、Node/Python 与固定原生扩展。
- X11–X13：默认 CLI、3× 性能门禁与双后端 CI/发布。
