# Changelog

## [Unreleased]

### 新增

- 内置 S4 开发合同门禁 `make compat-s4`。
- 外部发行版 `/sbin/init` 可作为客体 PID 1 启动，执行 inittab 的
  sysinit/respawn，并通过 `/dev/console`、`/dev/tty0..tty6` 接入标准流。
- `/sys/fs/cgroup/assistant-sandbox` 提供动态 cgroup v2 只读视图；
  `cgroup.procs`、`pids.current`、`memory.current/max` 与内核进程表和
  资源计量一致，namespace 标识每 VM 稳定且跨 VM 不复用。
- S4 APK 仓库门禁改为带签名公钥的真实 `apk add` / `apk del`，校验解包
  文件与 APK 数据库记录。
- 初步嵌套验证 `make sandbox-in-sandbox`：外层 VM 中运行 riscv64 CLI，
  再启动内层 BusyBox sh。
- S4 外部 Alpine 镜像验证 init、`/proc/self/ns`、`/proc/self/cgroup`
  与本地 APK 仓库；内置合同使用固定的 APK 仓库夹具。

### 移除

- 删除历史 WASI 客体 `assistant-bash.wasm.gz`、WASI 构建器与运行时、
  `--module` 兼容参数、POSIX WASI gate 及 wazero 依赖。
- 发布与安装只包含 Linux ABI VM CLI，不再生成或分发 WASM 客体。

### 修复

- 线程从非组长 TID 调用 `exit_group` 时未结束整个线程组的问题。
- 批量 FP 比较把 FLE/FLT/FEQ 的 funct3 映射错位的问题。
- `/dev/null` 等静态设备缺少 `F_GETFL`/`F_SETFL` 状态标志的问题。
- CLI 内存值解析改用整数有理数，避免嵌套运行时误判合法值。
- 实现 `flock(2)`，支持打开文件描述级共享/独占锁，打通 apk 数据库锁。
- O_PATH 目录描述符不能作为 `openat` dirfd、`fchownat` 的
  `AT_SYMLINK_NOFOLLOW`/`AT_EMPTY_PATH` 返回 ENOTSUP，导致 apk 解包
  报 ENOTDIR/owner 警告的问题。
- 打开文件描述符从 3 开始分配，关闭 stdin 后不会复用 0；BusyBox init
  因此把 getty 的 tty 打开误判为失败并高频 respawn。
- init 启动路径未把程序路径放入 argv[0]，导致 BusyBox 把 `-c` 当作
  applet 名；`BootInit` 现与普通 exec 一样拼接 argv。
- `TIOCSCTTY`/`TIOCNOTTY` 未接入终端 ioctl，发行版 init 认领控制终端
  时失败的问题。

### 优化

- 扩展 Node/libm 热路径的 FP 指令覆盖：FSGNJ、FCVT、FMV、FCLASS
  与 fcsr CSR 进入 C++ 批量/JIT 路径，避免每条 FP 指令触发解释器
  或 Go 单步回退。Node 22 jitless 的 1M 次 `Math.sqrt` 从 5.69 s
  降到约 2.0 s，约 2.7–2.8×。
- JIT 增加跨页块链接与 256 项 JALR 目标缓存：跨页 JAL、分支和顺序
  落页命中后直接进入目标页，不再返回 C++ 页调度器。两页热循环从
  70–76 MIPS 提升到 415–481 MIPS；Python 1M 循环从关闭链接的
  5.98–6.00 s 降到 4.46–4.52 s，Node jitless 为 1.72–1.80 s，
  Go 10M 循环为 55–62 ms。
- C++ 批量执行路径启用双精度浮点算术、比较与转换。本机
  `make perf-languages`：Python 7.13 s、Node jitless 10.19 s、
  标准 V8 7.67 s、Go 61 ms。
- JIT 增加 32 项内联直接映射 TLB，复用页内偏移并为写访问合并
  `write_ok` 标志；软件 TLB 慢查从 2.66 亿次降到约 0.62 亿次
  （Python 1M 循环）。
- 内存批量视图按页号增量维护，新增页二分插入，Protect/Unmap 分开
  失效区域表与页表，不再每次 `AcquireMemoryView` 排序全部页。
- 批量译码页缓存从 512 扩到 4096 项，减少 Node/V8 启动时重复译码。
  本机 `make perf-languages`：Python 6.1–6.7 s、Node jitless 1.9–2.0 s、
  标准 V8 3.8–4.6 s、Go 62–71 ms；相对上一版分别约 1.1–1.2×、
  5.2–5.6×、1.7–2.1×、0.9–1.1×。

## [0.1.0] - 2026-11-25

首个面向用户的 RISC-V Linux ABI VM 版本候选。

### 新增

- 真实 riscv64 Bash 5.2 与 BusyBox sh，支持 PTY、作业控制、readline
  左右编辑、历史和 Tab 补全。
- CPython 3.12、Node.js 22、npm、Git、apk-tools 和 TinyCC 开发工具包。
- Python/Node REPL 直接使用各自 readline；光标左右移动和行编辑可用。
- Go `linux/riscv64` ELF runtime 执行支持。
- 1–8 CPU、256 MiB–16 GiB 内存配置，支持任意规范值。
- x86-64 页级 JIT、RISC-V cycle/time/instret 读取和单 hart fence 快路径。
- `--allow-guest-rwx` 下 Node 包装器切换到标准 V8；默认仍为 W^X + jitless。
- `assistant-sandbox --version` 和基于 Git tag 的版本注入。

### 修复

- 交互式 Python/Node 中左右方向键被宿主输入桥吞掉的问题。
- 指令页缓存失效时清空全部翻译缓存的问题，改为只失效实际代码页。
- 页面热点阈值调整为 32，降低 Node/V8 启动时的一次性 JIT 编译开销。

### 已知限制

- Python/Node 仍受 RISC-V 指令数上限约束，明显慢于宿主原生运行时。
- 当前没有客体 zsh 资产；`--shell zsh` 明确拒绝。
- S4 的发行版 init、完整 sysfs/设备枚举和可用包仓库仍未完成。
- 标准 V8 需要 `--allow-guest-rwx`，该开关降低客体代码页的 W^X 隔离。
- Q25–Q27 重型 Node/npm 网络门禁在 main push 和 tag 发布中暂不阻塞；
  仍可在 PR、`workflow_dispatch` 或本地 `make compat-*` 中运行。

### 验证

- `make lint`
- `go test ./cmd/assistant-sandbox ./executor/cpu/riscv64 ./vm/memory ./guest/devroot`
- `make compat-s3`
- `make perf-languages`
- `make build`
