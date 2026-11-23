# Linux 兼容性门禁

目标范围是 S3 到 S4：可在客体中完成真实开发工作。兼容性按可执行合同
分层，不用单个“兼容 Linux”布尔值表示。

| 等级 | 合同 | 当前状态 |
| --- | --- | --- |
| S0 | Bash/BusyBox、基础文件和进程 | 通过 |
| S1 | POSIX、coreutils、管道、信号、PTY | 通过已覆盖子集 |
| S2 | CPython、Node.js、标准库、子进程 | 通过已覆盖子集 |
| S3 | 同一 VM 的 shell、Python、Node、C 编译链和子进程开发流 | 通过，CLI `--tools=dev` 默认提供 |
| S4 | 发行版镜像、`apk`、`git`、`cc`、`/proc`、`/sys`、init | 外部镜像门禁 |

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
Node.js 22.23.5、npm 11.6.4、TinyCC，以及 Python 开发头文件。
`--tools=minimal` 只安装 Bash/BusyBox。

已知问题：

- Node.js 使用 `--jitless`，启动明显慢于宿主。
- npm 已打包，但 `npm --version` 在当前解释器中需要数分钟，仍需优化。
- pip 暂不打包；完整 pip CLI 导入超过 180 秒，不能作为可用开发工具发布。

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

默认 CLI 已达到 S3 工具存在性合同，但 Python/Node 仍受解释器性能限制，
S4 的 init、`apk`、`/sys`、namespace 等合同仍未达到。

## 参考与差分

发布前还应增加 Linux 6.8 riscv64 参考环境，把同一命令、同一输入和同一
rootfs 在参考 Linux 与兼容内核中运行，比较 stdout、stderr、退出码、
errno、生成文件和信号结果。当前仓库已有 LTP/S4 外部门禁入口，差分参考
执行器仍需接入 CI。
