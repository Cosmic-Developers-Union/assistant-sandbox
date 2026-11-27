# Linux 兼容性门禁

目标范围是 S3 到 S4：可在客体中完成真实开发工作。兼容性按可执行合同
分层，不用单个“兼容 Linux”布尔值表示。

| 等级 | 合同 | 当前状态 |
| --- | --- | --- |
| S0 | Bash/BusyBox、基础文件和进程 | 通过 |
| S1 | POSIX、coreutils、管道、信号、PTY | 通过已覆盖子集 |
| S2 | CPython、Node.js、标准库、子进程 | 通过已覆盖子集 |
| S3 | 同一 VM 的 shell、Python、Node、C 编译链和子进程开发流 | 通过，CLI `--tools=dev` 默认提供 |
| S4 | 发行版镜像、`apk`、`git`、`cc`、`/proc`、`/sys`、init | 内置合同通过；Alpine 外部镜像的 PID 1 init、namespace/cgroup 与本地包仓库真实安装通过 |

## S3

```sh
make compat-s3
```

该测试在同一台 VM 中装配真实 RISC-V Bash、BusyBox、CPython、Node.js
和 TinyCC，验证：

- Bash 管道、排序、文本处理
- Python 读写文件和创建子进程
- Node.js 读写文件和创建子进程
- 客体 C 编译、静态链接和运行
- 产出的文件内容

默认 CLI 现在通过 `--tools=dev` 安装同一套开发工具：Python 3.12.16、
Node.js 22.23.5、npm 11.6.4、Git 2.56.1、apk-tools 3.0.10、TinyCC，
以及 Python 开发头文件。CLI 另外提供只读 `/sys` 基础视图（内核版本、
CPU 拓扑、transparent hugepage 与 clocksource）；`/proc` 继续由内核
动态投影。
`--tools=minimal` 只安装 Bash/BusyBox。

VM 默认 2 CPU、4 GiB;配置支持 1–8 个客体 CPU 与 256 MiB–16 GiB 内存的任意规范值
(例如 `--memory 300m`、`--memory 1.5g`)。CPU 数同时用于
`/proc/cpuinfo`、`/sys/devices/system/cpu`、`sched_getaffinity` 和
执行权令牌数;多于一个 CPU 时多个可运行客体进程/线程可在宿主上
并行执行。内存数用于 `/proc/meminfo` 并作为默认会话/进程内存预算。

已知问题：

- Python 启动已从分钟级降到秒级。开发根改为 zstd 资产并行装入后，
  C++ 快速路径空命令约 0.4 秒，`python --version` 约 0.45 秒，
  `python -S -c 'print(1)'` 约 1.38 秒；纯 Go 回退空命令约 0.54 秒，
  后两项分别约 1.10 秒和 4.67 秒。同一客体二进制在 QEMU user-mode
  TCG 下 `-S -c 'print(1)'` 约 0.21 秒，因此扣除安装后的 VM 指令层
  仍约有 4.6 倍损失；计入开发根装入时整条 CLI 约为 6.5 倍。
- 开发 CLI 默认使用标准 V8，裸 `node` 进入交互 REPL。
  严格客体 W^X 模式（`--allow-guest-rwx=false`）使用 jitless。
- npm 已打包，但 `npm --version` 在当前解释器中需要数分钟，仍需优化。
- pip 暂不打包；完整 pip CLI 导入超过 180 秒，不能作为可用开发工具发布。
- 当前 `apk` 是真实 apk-tools 二进制，但包索引/仓库未预置；离线安装
  与仓库刷新尚未达到发行版可用状态。
- C++ 快速路径只覆盖整数 ALU、乘除、访存与分支；浮点、原子、CSR 和
  系统调用仍回退单步。Linux 默认 `make build` 启用它，其他平台或
  `CGO_ENABLED=0` 使用纯 Go 解释器。
- CLI 单次执行会创建新 VM，开发根必须进入本次 VM 的 memfs；当前
  优化是并行 zstd 解包，而不是跨进程复用 VM。交互 `--session` 只在
  启动时安装一次。

## CPython 上游测试

仓库内的 CPython 3.12.16 测试资产可通过独立门禁运行。测试名直接传给
`python -m test`，例如：

```sh
make compat-python PYTHON_SUITE_TESTS="test_sys"
make compat-python PYTHON_SUITE_TESTS="test_io test_fcntl test_mmap test_tty"
```

当前结果：

- `test_sys` 通过。
- `test.test_os` 全量通过，包括 `MemfdCreateTests`、坏描述符上的
  `fpathconf` 返回 `EBADF`、`TestSendfile` 的 asyncio 收尾，以及
  `ForkTests.test_fork_warns_when_non_python_thread_exists`。
- `test.test_asyncio.test_sock_lowlevel` 通过 42 个用例（3 个用例按
  环境跳过），覆盖 EPoll/Poll/Select 三套选择器、UDP echo、AF_UNIX、
  1 MB HTTP 大块收发与 socket 选项。
- `PYTHON_SUITE_TESTS=all` 会启动 492 个 test file；当前单核解释器下
  需要数小时。完整套件仍有与本次修复无关的已知缺口，包括
  `test_asyncio.test_base_events`（12 errors）、
  `test_asyncio.test_events`（异步子进程信号路径超时）、
  `test_asyncio.test_sendfile`（54 errors），以及 `setitimer`、
  `SO_REUSEPORT`、IPv6 dual-stack 等明确的 ENOTSUP 项。

`fpathconf` 的描述符校验位于 guest musl 装载器：上游 musl 对已知
`pathconf` 名称直接返回常量表值，不检查 fd。仓库装载器附带一处
`fcntl(fd, F_GETFD)` 校验补丁，补丁与构建摘要见
[`internal/guest/testdata/lib/README.md`](../internal/guest/testdata/lib/README.md)。

本目录的测试还修复并将以下行为锁入回归：交互式 Python 的 WINCH
递归、`rt_sigaction` 的 oldact、`O_PATH`、`O_NONBLOCK`、FIFO
创建/打开、`fchown/fchownat`、`getpriority/setpriority`、硬链接目录项
与指向目录的符号链接删除、UDP `bind(…, 0)` 的临时端口回读、
AF_UNIX `sendto/recvfrom`、`SO_SNDBUF/SO_RCVBUF/SO_REUSEADDR`，以及
有限超时 `epoll_pwait` 的就绪唤醒。

## 外部 LTP

LTP 资产不随仓库分发。提供 riscv64 LTP rootfs 后运行：

```sh
COMPAT_LTP_TARBALL=/path/to/ltp-rootfs.tar.gz \
COMPAT_LTP_ARGS="-g ltp-syscalls" \
make compat-ltp
```

没有设置 `COMPAT_LTP_TARBALL` 时测试明确跳过，不声明通过。

## S4 开发合同

内置合同：

```sh
make compat-s4
```

该门禁使用内置开发根验证 `/sbin/init`、`apk`、`git`、`cc`、Python、
Node、`/proc/self/status`、`/proc/self/cgroup`、`/proc/self/ns/*`、
`/sys/kernel`、`/sys/fs/cgroup`、CPU 拓扑与 `lo` 网络属性，并用固定
APK 仓库执行真实的 `apk add` / `apk del`：校验索引与包签名、解包到
VM 根文件系统、检查 APK 数据库记录，再验证卸载后文件消失。
随后用以下命令验证外层 VM 内可以再启动一台 sandbox：

```sh
make sandbox-in-sandbox
```

嵌套验证构建 riscv64 CLI，在外层 VM 中由内层 BusyBox sh 执行
`printf sandbox-in-sandbox-ok`；它只证明 VM 递归启动链路，不声明完整
嵌套性能。

可选外部发行版镜像：

```sh
COMPAT_S4_IMAGE=/path/to/development-rootfs.tar.gz \
COMPAT_S4_REPO=/path/to/apk-repository \
make compat-s4
```

外部镜像门禁检查：

- `/sbin/init` 作为 PID 1 启动；一个用例使用控制用 inittab 验证
  sysinit 与 respawn，另一个用例用镜像自带 inittab 验证 getty 启动
  且 tty 打开失败不会形成 respawn 风暴
- `/etc/inittab` 与可用 `/bin/sh`
- `/dev/console`、`/dev/tty1` 可打开并接入 init 标准流
- `apk`
- `/proc/self/status`
- `/proc/self/cgroup` 与 `/proc/<pid>/ns/*`（VM 内稳定、跨 VM 不复用）
- `/sys/kernel` 与动态 `/sys/fs/cgroup`：
  `/proc/<pid>/cgroup` 映射到 `/sys/fs/cgroup/assistant-sandbox`，
  `cgroup.procs`、`pids.current`、`memory.current/max` 来自内核与计量
- 可选本地 APK 仓库的带签名真实安装与卸载

CI 使用固定 Alpine minirootfs 3.22.8 与仓库测试夹具验证上述合同。
仓库夹具执行安装/卸载但显式关闭包安装脚本；脚本执行属于后续扩展。
namespace/cgroup 是每 VM 的只读身份视图；`unshare`/`setns` 等
命名空间切换、独立虚拟控制台和完整设备枚举仍未接入。

## 参考与差分

发布前还应增加 Linux 6.8 riscv64 参考环境，把同一命令、同一输入和同一
rootfs 在参考 Linux 与兼容内核中运行，比较 stdout、stderr、退出码、
errno、生成文件和信号结果。当前仓库已有 LTP/S4 外部门禁入口，差分参考
执行器仍需接入 CI。
