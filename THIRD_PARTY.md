# 第三方来源与许可

模块依赖(与 [go.mod](go.mod)/[go.sum](go.sum) 固定版本一一对应,
由 [许可核对测试](internal/metadata/licenses_test.go) 在 CI 强制):

- mvdan.cc/sh/v3 v3.14.0，BSD-3-Clause。来源：https://github.com/mvdan/sh/tree/v3.14.0 ，完整许可见 [mvdan-sh-LICENSE](licenses/mvdan-sh-LICENSE)。
- github.com/mattn/go-runewidth v0.0.23，MIT。来源：https://github.com/mattn/go-runewidth/tree/v0.0.23 ，完整许可见 [go-runewidth-LICENSE](licenses/go-runewidth-LICENSE)。
- github.com/spf13/pflag v1.0.10，BSD-3-Clause。来源：https://github.com/spf13/pflag/tree/v1.0.10 ，完整许可见 [spf13-pflag-LICENSE](licenses/spf13-pflag-LICENSE)。
- golang.org/x/crypto v0.56.0，BSD-3-Clause(Go 许可)。来源：https://cs.opensource.google/go/x/crypto/+/v0.56.0 ，完整许可见 [x-crypto-LICENSE](licenses/x-crypto-LICENSE)。
- github.com/klauspost/compress v1.20.5，BSD-3-Clause（`zstd` 等包；`gzhttp` 子包为 Apache-2.0）。来源：https://github.com/klauspost/compress/tree/v1.20.5 ，完整许可见 [klauspost-compress-LICENSE](licenses/klauspost-compress-LICENSE)。
- github.com/clipperhouse/uax29/v2 v2.7.0(间接)，MIT。来源：https://github.com/clipperhouse/uax29/tree/v2.7.0 ，完整许可见 [uax29-LICENSE](licenses/uax29-LICENSE)。
- golang.org/x/sys v0.48.0(间接)，BSD-3-Clause(Go 许可)。完整许可见 [x-sys-LICENSE](licenses/x-sys-LICENSE)。
- golang.org/x/term v0.46.0，BSD-3-Clause(Go 许可)。完整许可见 [x-term-LICENSE](licenses/x-term-LICENSE)。
- github.com/klauspost/compress v1.20.5，BSD-3-Clause。来源：https://github.com/klauspost/compress/tree/v1.20.5 ，完整许可见 [klauspost-compress-LICENSE](licenses/klauspost-compress-LICENSE)。

内嵌与适配代码:

- qiangli/coreutils 命令适配来源，MIT。来源：https://github.com/qiangli/coreutils ，保留许可见 [qiangli-coreutils-LICENSE](licenses/qiangli-coreutils-LICENSE)。
- GoAWK 客体适配代码，MIT；保留的版权与许可见 [LICENSE.txt](userland/toolcompat/goawk/LICENSE.txt)。
客体运行时发布物(版本/摘要/支持期见 [构建配置](docs/runtime/toolchain.md);
随发布物分发的完整许可证清单由 Q01/Q15 流水线产出并追加到本文件):

- CPython 3.14.9,Python Software Foundation License 2.0(PSF-2.0)。
  来源:https://www.python.org/ftp/python/3.14.9/Python-3.14.9.tgz 。
- Node.js 24.22.1,MIT(内置依赖各自许可,以源码树 LICENSE 与第三方清单为准)。
  来源:https://nodejs.org/dist/v24.22.1/node-v24.22.1.tar.xz 。
- musl 1.2.6,MIT。来源:https://musl.libc.org/releases/musl-1.2.6.tar.gz 。
- Alpine 3.22.7 minirootfs(riscv64) 构建根,各软件包许可随根文件系统
  /usr/share/licenses 提供。来源:https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/riscv64/ 。

N10 真实 Bash 与基础工具验收资产(Alpine 3.22 riscv64 官方包;
逐文件 sha256 锁定于 internal/guest/n10_test.go):

- Bash 5.2.37-r0,GPL-3.0-or-later。包:https://dl-cdn.alpinelinux.org/alpine/v3.22/main/riscv64/bash-5.2.37-r0.apk (sha256=0e4c048a…d719);二进制 bin/bash sha256=009e1bab…a94。完整许可 [GPL-3.0.txt](licenses/GPL-3.0.txt)。
- BusyBox 1.37.0-r20(static),GPL-2.0。包:https://dl-cdn.alpinelinux.org/alpine/v3.22/main/riscv64/busybox-static-1.37.0-r20.apk (sha256=29072f0a…f31c);二进制 bin/busybox.static sha256=62831fb7…d704。完整许可 [GPL-2.0.txt](licenses/GPL-2.0.txt)。
- GNU Readline 8.2.13-r1,GPL-3.0-or-later。包:https://dl-cdn.alpinelinux.org/alpine/v3.22/main/riscv64/readline-8.2.13-r1.apk (sha256=4b6c7e5f…05fa);库 sha256=e8cce022…cca2。完整许可 [GPL-3.0.txt](licenses/GPL-3.0.txt)。
- NCurses 6.5_p20250503-r0,X11。包:https://dl-cdn.alpinelinux.org/alpine/v3.22/main/riscv64/libncursesw-6.5_p20250503-r0.apk (sha256=7d88384b…9607);库 sha256=28e544f2…c3e4。完整许可 [ncurses-X11.txt](licenses/ncurses-X11.txt)。
- musl 1.2.6 动态加载器由 A04 固定源码构建,附加
  `fpathconf` 描述符校验补丁(testdata/lib/ld-musl-riscv64.so.1
  sha256=23ea7ef0…b0fd;补丁见
  [testdata/lib/fpathconf-ebadf.patch](internal/guest/testdata/lib/fpathconf-ebadf.patch)),MIT。

上述 N10 资产中的 Bash、BusyBox、musl 加载器、GNU Readline 与 NCurses
同时作为 CLI 的 `go:embed` 输入位于 `guest/native/assets`,版本、摘要与
许可以上一段为准。

CLI 的 `--tools=dev` 还内嵌以下固定资产,位于 `guest/devroot/assets`:

- CPython 3.12.16(Alpine 3.22 riscv64),PSF-2.0;来源包
  `python3-3.12.16-r0.apk`，内嵌为 zstd tar `python3-3.12.16-r0.tar.zst`。
- Node.js 22.23.5 与 ICU 数据,MIT;来源归档由
  `internal/guest/node_helpers_test.go` 的摘要锁定；开发根使用
  `node22-runtime.tar.zst`。
- npm 11.6.4,Artistic-2.0;来源归档由 `internal/guest/node_helpers_test.go`
  的摘要锁定。
- TinyCC 0.9.27_git20250106-r0,LGPL-2.1-or-later;来源与许可见上文 N10/Q28
  资产条目及 [LGPL-2.1.txt](licenses/LGPL-2.1.txt)。
- Git 2.56.1-r0(Alpine edge riscv64 官方包),GPL-2.0-only;随附
  libcurl(curl 许可)、libexpat(MIT)、pcre2(BSD-3-Clause)、libidn2
  (GPL-2.0-or-later OR LGPL-3.0-or-later)、libpsl(MIT)、libunistring
  (GPL-2.0-or-later OR LGPL-3.0-or-later)与 ca-certificates-bundle
  (MPL-2.0 AND MIT)。完整 GPL/LGPL/BSD/MIT 文本见 `licenses/`。
- apk-tools 3.0.10-r1 与 libapk(Alpine edge riscv64 官方包),
  GPL-2.0-only。完整许可见 [GPL-2.0.txt](licenses/GPL-2.0.txt)。

Q28 客体原生编译链验收资产(Alpine 3.22 riscv64 官方包;归档摘要由
internal/guest/q28_toolchain_test.go 锁定):

- TinyCC 0.9.27_git20250106-r0,LGPL-2.1-or-later。来源包:tcc、tcc-libs、
tcc-libs-static、tcc-dev(community/riscv64);执行文件、`libtcc.so`、musl
头文件、crt 与 `libc.a` 归档为
`testdata/tcc-riscv64-toolchain.tar.gz` sha256=dc16d9d4…3ce04。完整许可
[LGPL-2.1.txt](licenses/LGPL-2.1.txt)。
- GCC 14.2.0-r6,GPL-2.0-or-later;分发的 cc1plus/链接支持受 GCC Runtime
Library Exception 约束。来源:main/riscv64/gcc-14.2.0-r6.apk。完整许可
[GPL-2.0.txt](licenses/GPL-2.0.txt) 与
[GCC-Runtime-Library-Exception-3.1.txt](licenses/GCC-Runtime-Library-Exception-3.1.txt)。
- GNU Binutils 2.44-r3,GPL-2.0-or-later AND LGPL-2.1-or-later AND
BSD-3-Clause(`as`、`ld`)。来源:main/riscv64/binutils-2.44-r3.apk。许可
[GPL-2.0.txt](licenses/GPL-2.0.txt)、[LGPL-2.1.txt](licenses/LGPL-2.1.txt)、
[BSD-3-Clause.txt](licenses/BSD-3-Clause.txt)。
- GCC 运行依赖:GMP 6.3.0-r3(LGPL-3.0-or-later OR GPL-2.0-or-later)、
MPFR 4.2.1_p1-r0(LGPL-3.0-or-later)、MPC 1.3.1-r1(LGPL-3.0-or-later)、
isl 0.25-r2(MIT)、Jansson 2.14.1-r0(MIT)、zlib 1.4(Zlib)、
Zstandard 1.5.7-r0(BSD-3-Clause OR GPL-2.0-or-later)。许可
[LGPL-3.0.txt](licenses/LGPL-3.0.txt)、[MIT.txt](licenses/MIT.txt)、
[Zlib.txt](licenses/Zlib.txt)、[BSD-3-Clause.txt](licenses/BSD-3-Clause.txt)。
上述 C++ 子集归档为 `testdata/gcc14-cxx-riscv64-toolchain.tar.gz`
sha256=a9a452dc…199d5。
