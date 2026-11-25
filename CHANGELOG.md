# Changelog

## [Unreleased]

### 新增

- 内置 S4 开发合同门禁 `make compat-s4`。
- 初步嵌套验证 `make sandbox-in-sandbox`：外层 VM 中运行 riscv64 CLI，
  再启动内层 BusyBox sh。

### 移除

- 删除历史 WASI 客体 `assistant-bash.wasm.gz`、WASI 构建器与运行时、
  `--module` 兼容参数、POSIX WASI gate 及 wazero 依赖。
- 发布与安装只包含 Linux ABI VM CLI，不再生成或分发 WASM 客体。

### 修复

- 线程从非组长 TID 调用 `exit_group` 时未结束整个线程组的问题。
- 批量 FP 比较把 FLE/FLT/FEQ 的 funct3 映射错位的问题。
- `/dev/null` 等静态设备缺少 `F_GETFL`/`F_SETFL` 状态标志的问题。
- CLI 内存值解析改用整数有理数，避免嵌套运行时误判合法值。

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
