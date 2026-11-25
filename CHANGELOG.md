# Changelog

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

### 验证

- `make lint`
- `go test ./cmd/assistant-sandbox ./executor/cpu/riscv64 ./vm/memory ./guest/devroot`
- `make compat-s3`
- `make perf-languages`
- `make build`
