# 性能基线

状态:2026-11-25,AMD Ryzen 7 5700G(x86_64 宿主)。VM 默认 2 CPU、
4 GiB;多核正确性门禁固定覆盖 1/2/4 三档。

## 当前执行后端

- Go 侧按 2 字节边界预译码指令页,支持 RV64IM、压缩指令、AMO、
  LR/SC,以及 CPython 热路径使用的单精度浮点子集。
- x86-64 宿主使用页级 JIT。生成代码位于宿主私有映射:先以 RW
  写入,再 `mprotect` 为 RX;客体地址空间仍执行 W^X,绝不暴露
  RWX 页。
- JIT 内联最近页缓存与 512 项软件 TLB;跨页、文件后端、特殊浮点
  值、CSR、系统调用和不支持指令回退到 C++ 批量解释器或 Go 单步
  语义基准。
- 页面连续进入 32 次后才编译，避免 Node/Python 启动时把一次性代码
  页展开成数百 KB 机器码。
- JIT/批量路径识别只读 cycle/time/instret 读取和单 hart fence；
  地址空间只失效实际被写保护的指令页，避免 V8 写代码页时清空
  全部翻译缓存。
- 已支持运行 linux/riscv64 Go ELF runtime；`RDTIME` 使用与
  cycle/instret 相同的单调退休指令计数。
- `ASSISTANT_SANDBOX_DISABLE_JIT=1` 可强制使用解释路径做对拍。

## 原始执行核心

`go test ./executor/cpu/riscv64 -run '^$' -bench . -benchtime=2s -count=2`

| 路径 | 吞吐 |
| --- | --- |
| x86-64 页级 JIT(热点整数/分支/访存) | 2.1–2.4 GIPS |
| Go 单步解释 | 约 55 MIPS |

JIT 相对 Go 单步约快 38–43 倍。页级 JIT 的执行上限由宿主代码缓存、
客体指令密度和访存局部性决定;不是单纯扩大批量或缓存能继续
线性提升。

## 端到端

| 场景 | 客体 | 宿主原生 | 说明 |
| --- | ---: | ---: | --- |
| C 100M 次整数循环(TCC `-O0`) | 1.31–1.71 s | 0.06–0.07 s | 含约 0.57 s CLI 启动 |
| Python 3.12 启动 | 1.53–1.61 s | 约 0.009 s | 含 CLI/开发根初始化 |
| Python 1M 次整数循环 | 7.13 s | 0.056 s | 2026-11-25 `make perf-languages`，双精度批量路径 |
| Node.js 22 jitless, 100k 次循环 | 10.19 s | 0.026 s | 双精度批量路径后 |
| Node.js 22 标准 V8 JIT, 100k 次循环 | 7.67 s | 0.025 s | 需 `--allow-guest-rwx` 实验开关 |
| Go 10M 次整数循环 | 0.061 s | 约 0.007 s | 已启动 VM 内的 Go runtime |
| CPython `test_os` | 171.7 s | n/a | 完整上游模块 |
| S3 开发工作流 | 88.9 s | n/a | Bash/Python/Node/TCC/git/apk |

按去掉 CLI 启动后的计算阶段比较:

- C 100M 循环约 12–18×,已接近但不是严格 10×。
- Python 1M 循环约 120–125×,仍远高于 10×。
- Node 十万次循环约 310–400×,主要被 Node/V8 启动成本支配；
  标准 V8 JIT 比 jitless 略快，但 DBT 下仍无法接近宿主。
- Go 10M 循环约 8–10×,当前已达到 10× 目标附近。

## 10× 目标评估

页级 JIT 已消除逐指令分派、寄存器数组往返和大部分 TLB 慢查,
但不能消除客体程序本身的指令工作量。CPython 3.12 的一次整数
循环在 RISC-V 客体中约执行 1,500 条指令;即使每条只花一个宿主
周期,也高于 x86 原生 CPython 一次循环的周期数。Python 要达到
10×,下一步必须做更高层的热点识别与优化(例如 CPython 字节码
级加速、跨基本块超级块/轨迹、常量传播和冗余装入消除),而不是
继续扩大页缓存或批量。

当前结论必须按工作负载陈述:Go 计算热点已达到约 10×;C 接近
10×;Python 单线程解释循环、Node jitless 启动和 VM 进程启动尚未
达到 10×。

3× 目标的指令级可行性、标准 Node 对比与可选后端决策见
[加速路线](acceleration-roadmap.md)。

