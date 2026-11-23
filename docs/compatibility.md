# Linux 兼容性门禁

目标范围是 S3 到 S4：可在客体中完成真实开发工作。兼容性按可执行合同
分层，不用单个“兼容 Linux”布尔值表示。

| 等级 | 合同 | 当前状态 |
| --- | --- | --- |
| S0 | Bash/BusyBox、基础文件和进程 | 通过 |
| S1 | POSIX、coreutils、管道、信号、PTY | 通过已覆盖子集 |
| S2 | CPython、Node.js、标准库、子进程 | 通过已覆盖子集 |
| S3 | 同一 VM 的 shell、Python、Node、C 编译链和子进程开发流 | 通过，CLI `--tools=dev` 默认提供 |
| S4 | 发行版镜像、`apk`、`git`、`cc`、`/proc`、`/sys`、init | 工具与 `/sys` 基础已进入 CLI；发行版 init 与完整系统视图仍待外部门禁 |

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

已知问题：

- Python 启动已从分钟级降到秒级。开发根改为 zstd 资产并行装入后，
  C++ 快速路径空命令约 0.4 秒，`python --version` 约 0.45 秒，
  `python -S -c 'print(1)'` 约 1.38 秒；纯 Go 回退空命令约 0.54 秒，
  后两项分别约 1.10 秒和 4.67 秒。同一客体二进制在 QEMU user-mode
  TCG 下 `-S -c 'print(1)'` 约 0.21 秒，因此扣除安装后的 VM 指令层
  仍约有 4.6 倍损失；计入开发根装入时整条 CLI 约为 6.5 倍。
- Node.js 使用 `--jitless`，C++ 快速路径下
  `node -e 'console.log(1)'` 约 11.6 秒，纯 Go 回退约 20 秒。
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

## 外部 LTP

LTP 资产不随仓库分发。提供 riscv64 LTP rootfs 后运行：

```sh
COMPAT_LTP_TARBALL=/path/to/ltp-rootfs.tar.gz \
COMPAT_LTP_ARGS="-g ltp-syscalls" \
make compat-ltp
```

没有设置 `COMPAT_LTP_TARBALL` 时测试明确跳过，不声明通过。

## S4 开发镜像

```sh
COMPAT_S4_IMAGE=/path/to/development-rootfs.tar.gz \
make compat-s4
```

S4 门禁检查：

- `/sbin/init`
- `apk`
- `git`
- `cc`
- `/proc/self/status`
- `/sys`

默认 CLI 已提供 S4 合同中的 `git`、`apk`、`cc`、Python、Node、
`/proc/self/status`、`/sys/kernel` 基础视图与 BusyBox `/sbin/init`
applet，但发行版 init 启动流程、完整 sysfs/设备枚举、namespace/cgroup
与可用包仓库仍未达到。不能仅凭 CLI 工具存在性声明 S4 完成。

## 参考与差分

发布前还应增加 Linux 6.8 riscv64 参考环境，把同一命令、同一输入和同一
rootfs 在参考 Linux 与兼容内核中运行，比较 stdout、stderr、退出码、
errno、生成文件和信号结果。当前仓库已有 LTP/S4 外部门禁入口，差分参考
执行器仍需接入 CI。
