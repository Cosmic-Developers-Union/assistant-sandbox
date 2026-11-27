# linux/amd64 客体根文件系统（X03）

固定资产为 [`guest/hostroot/rootfs-amd64.tar.gz`](../../guest/hostroot/rootfs-amd64.tar.gz)，
清单为同目录 `rootfs-amd64.manifest`，APK 版本与 sha256 锁为
`APKS.sha256`。

## 固定输入

| 组件 | 版本/摘要 |
| --- | --- |
| Alpine 基线 | `3.22.8`，linux/amd64 manifest `sha256:9db2934886712d877c932e07249c75a59e6ca27adfbccc05261987a17bef2d63` |
| Node.js | `22.23.5-r0`，APK sha256 `4584849b54f667cbb526778b05f5f414c01d3598e6c97afba77650a1a22c8475` |
| CPython | `3.12.16-r0`，APK sha256 `4bd1bf5b980cdf906c04a507258cfae8b44b3070d82b7c4cbf77ef2552bbdb06` |
| Bash | `5.2.37-r0`，APK sha256 `8218982214f1b51f520d282b0c81d654846e974ce009e758acc6187bd64fe228` |
| musl | `1.2.5-r13`，APK sha256 `359890bc7c4dc5fee7a85192de60a471c987e4664767288578beca60a44db2c8` |
| BusyBox | `1.37.0-r20`，APK sha256 `7cc34c2dda2520b456ab4a55abc37fe245ac90f98ab9f1c7a9be3aec035df48e` |
| `assistant-init` | 静态 Go linux/amd64 helper，sha256 `2d780fe96b8b44da3c4bc604c28e67f4d2df146a9023d50fee79bd83d2806da3` |
| 根文件系统归档 | sha256 `47ee80d8292f48036698d383eafbfca20764e7cd978eab2d674b536a0e1cab2a`，43,693,759 字节 |

所有 APK 来自固定仓库
`https://dl-cdn.alpinelinux.org/alpine/v3.22/main/x86_64/`；完整 38 项直接/
传递依赖及逐个 sha256 见 `APKS.sha256`。构建脚本先下载并校验每个 APK，
再以 `apk add --no-network --allow-untrusted` 安装，避免依赖解析漂移。

## 可重复构建

```sh
guest/hostroot/build.sh /tmp/opencode/hostroot-a.tar.gz
guest/hostroot/build.sh /tmp/opencode/hostroot-b.tar.gz
sha256sum /tmp/opencode/hostroot-a.tar.gz /tmp/opencode/hostroot-b.tar.gz
cmp /tmp/opencode/hostroot-a.tar.gz /tmp/opencode/hostroot-b.tar.gz
```

两次独立构建实测：

```text
47ee80d8292f48036698d383eafbfca20764e7cd978eab2d674b536a0e1cab2a  hostroot-a.tar.gz
47ee80d8292f48036698d383eafbfca20764e7cd978eab2d674b536a0e1cab2a  hostroot-b.tar.gz
```

脚本固定 Docker 镜像摘要、APK 文件、`/etc/hostname`、`/etc/hosts`、
空 `/etc/resolv.conf`，并以 GNU tar 的排序路径、零 mtime、数字 owner/group
和 `gzip -n` 生成归档。容器 ID、Docker 元数据与随机时间不进入资产。

## ELF、加载器与共享库核对

解包后执行：

```sh
readelf -h usr/bin/node | grep -E 'Class:|Data:|Type:|Machine:'
readelf -h usr/bin/python3.12 | grep -E 'Class:|Data:|Type:|Machine:'
readelf -l usr/bin/node | grep interpreter
readelf -l usr/bin/python3.12 | grep interpreter
lib/ld-musl-x86_64.so.1 --library-path "$PWD/usr/lib:$PWD/lib" --list usr/bin/node
lib/ld-musl-x86_64.so.1 --library-path "$PWD/usr/lib:$PWD/lib" --list usr/bin/python3.12
```

Node 与 Python 均为 `ELF64 / little endian / DYN / Advanced Micro Devices
X86-64`，两者的 `PT_INTERP` 都是 `/lib/ld-musl-x86_64.so.1`。musl
`--list` 实测把 Node 的 `libz`、`libada`、`libsimdjson`、`libsimdutf`、
ICU、OpenSSL、libstdc++ 等和 Python 的 `libpython3.12`、musl 全部解析到
解包目录内的 `/usr/lib` 或 `/lib`，没有使用宿主库。

在 user/mount namespace 内 chroot 实测：

```text
$ chroot rootfs /usr/bin/node --version
v22.23.5
$ chroot rootfs /usr/bin/python3.12 --version
Python 3.12.16
$ chroot rootfs /bin/bash --noprofile --norc -c 'printf "%s\n" shell-ok'
shell-ok
```

## 隔离 init

镜像内 `/usr/bin/assistant-init` 由 `guest/hostroot/init/main.go` 以
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 -trimpath -ldflags '-s -w -buildid='`
静态构建。父进程创建 namespace 后启动该 helper；helper 在受信控制面
完成 mount 传播检查、bind/pivot_root、旧根脱离、`/proc` 挂载，并显式
bind `/dev/null`、`zero`、`full`、`random`、`urandom`，并把 `/dev/tty`
链接到当前控制终端描述符；显式 PTY 会话在 exec 前获取 controlling terminal。
随后设置
guest hostname、`no_new_privs`、清空 bounding/effective capabilities、
安装 seccomp denylist。helper 固定 `GOMAXPROCS=1` 并锁定执行线程，避免
很小的 `pids.max` 被 Go 运行时线程耗尽，同时保证线程相关的
`no_new_privs`/capabilities/seccomp 状态由同一线程带入 `execve`。独立 gate
管道保证父进程先把子进程写入 per-VM
cgroup，再放行隔离初始化。控制管道设置 CLOEXEC；helper 报告 READY 后
`execve` 客体程序，父进程观察到管道 EOF 才返回成功，避免信号在 exec
前丢失。配置出站策略时，同一 helper 以 `netproxy` 模式启动客体 HTTP
代理，经私有 Unix socket 把目标交给父进程裁决。

## 损坏镜像

Go 包 `guest/hostroot` 在 linux/amd64 构建中嵌入归档，并在解包前校验
固定 sha256、gzip/tar 完整性和必需文件。测试
`TestCorruptArchiveFailsBeforeExtraction` 确认损坏输入返回
`ErrInvalidArchive` 且目标目录保持为空；原生后端启动将复用同一校验，
不会在镜像损坏时退回宿主解释器。

```sh
go test ./guest/hostroot -count=1 -v
```

## 许可证

- Node.js：MIT；APK 与源码许可说明见 <https://nodejs.org/en/about>。
- CPython 与 pyc：PSF-2.0；见 <https://docs.python.org/3/license.html>。
- Bash：GPL-3.0-or-later；Readline：GPL-3.0-or-later。
- BusyBox：GPL-2.0-only；musl：MIT。
- ICU 76.1：Unicode-3.0；OpenSSL 3.5：Apache-2.0；bzip2：bzip2 license。
- zlib：Zlib；Expat、c-ares、nghttp2、libffi、simdutf：MIT。
- SQLite：public domain；simdjson：Apache-2.0；Zstandard：
  BSD-3-Clause OR GPL-2.0-or-later；NCurses：X11。
- GCC/libstdc++ 运行库受 GCC Runtime Library Exception 约束；其余传递
  依赖许可见 `APKS.sha256` 对应的 Alpine 包页面与镜像 `/lib/apk/db/installed`。

完整来源/许可汇总同步登记于 [`THIRD_PARTY.md`](../../THIRD_PARTY.md)。
