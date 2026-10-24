# 第三方来源与许可

模块依赖(与 [go.mod](go.mod)/[go.sum](go.sum) 固定版本一一对应,
由 [许可核对测试](internal/metadata/licenses_test.go) 在 CI 强制):

- mvdan.cc/sh/v3 v3.14.0，BSD-3-Clause。来源：https://github.com/mvdan/sh/tree/v3.14.0 ，完整许可见 [mvdan-sh-LICENSE](licenses/mvdan-sh-LICENSE)。
- wazero v1.12.0，Apache-2.0。来源：https://github.com/tetratelabs/wazero/tree/v1.12.0 ，完整许可见 [wazero-LICENSE](licenses/wazero-LICENSE)。
- github.com/mattn/go-runewidth v0.0.23，MIT。来源：https://github.com/mattn/go-runewidth/tree/v0.0.23 ，完整许可见 [go-runewidth-LICENSE](licenses/go-runewidth-LICENSE)。
- github.com/spf13/pflag v1.0.10，BSD-3-Clause。来源：https://github.com/spf13/pflag/tree/v1.0.10 ，完整许可见 [spf13-pflag-LICENSE](licenses/spf13-pflag-LICENSE)。
- golang.org/x/crypto v0.56.0，BSD-3-Clause(Go 许可)。来源：https://cs.opensource.google/go/x/crypto/+/v0.56.0 ，完整许可见 [x-crypto-LICENSE](licenses/x-crypto-LICENSE)。
- github.com/clipperhouse/uax29/v2 v2.7.0(间接)，MIT。来源：https://github.com/clipperhouse/uax29/tree/v2.7.0 ，完整许可见 [uax29-LICENSE](licenses/uax29-LICENSE)。
- golang.org/x/sys v0.48.0(间接)，BSD-3-Clause(Go 许可)。完整许可见 [x-sys-LICENSE](licenses/x-sys-LICENSE)。
- golang.org/x/term v0.46.0(间接)，BSD-3-Clause(Go 许可)。完整许可见 [x-term-LICENSE](licenses/x-term-LICENSE)。

内嵌与适配代码:

- qiangli/coreutils 命令适配来源，MIT。来源：https://github.com/qiangli/coreutils ，保留许可见 [qiangli-coreutils-LICENSE](licenses/qiangli-coreutils-LICENSE)。
- GoAWK 客体适配代码，MIT；保留的版权与许可见 [LICENSE.txt](userland/toolcompat/goawk/LICENSE.txt)。
- `guest/overlay/stdin_os.go.txt` 来自 mvdan 的 `interp/stdin_js.go`，调整为 WASI
  内存管道。`build/interpreter.go` 在临时依赖副本中适配解释器；模块缓存不被修改。

客体运行时发布物(版本/摘要/支持期见 [构建配置](docs/runtime/toolchain.md);
随发布物分发的完整许可证清单由 Q01/Q15 流水线产出并追加到本文件):

- CPython 3.14.9,Python Software Foundation License 2.0(PSF-2.0)。
  来源:https://www.python.org/ftp/python/3.14.9/Python-3.14.9.tgz 。
- Node.js 24.22.1,MIT(内置依赖各自许可,以源码树 LICENSE 与第三方清单为准)。
  来源:https://nodejs.org/dist/v24.22.1/node-v24.22.1.tar.xz 。
- musl 1.2.6,MIT。来源:https://musl.libc.org/releases/musl-1.2.6.tar.gz 。
- Alpine 3.22.7 minirootfs(riscv64) 构建根,各软件包许可随根文件系统
  /usr/share/licenses 提供。来源:https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/riscv64/ 。
