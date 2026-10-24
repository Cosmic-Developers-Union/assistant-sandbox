# 需求追踪清单(A10)

状态:现行。本文是最终需求 → 任务 → 验收用例 → 发布门禁的可查询索引;
逐任务证据以 [TODO.md](../../TODO.md) 为权威记录,勾选纪律由
[追踪校验测试](../../internal/metadata/traceability_test.go) 在 CI 强制。

## 追踪规则

- 每条已勾选任务必须同时具备 `证据:`(实现文件/测试/缺陷修复)、`测试:`
  (可执行命令)与 `CI:`(门禁挂靠)三要素;缺失任一即 CI 失败。
- 未勾选任务不得携带证据(防止"半成品伪装完成")。
- 需求范围变更必须同步:本文的阶段门禁表、[兼容配置](../compat/linux-profile.md)
  的三张表、以及 ADR 目录。

## 阶段门禁(与 TODO「建议推进顺序」对应)

| 阶段 | 任务范围 | 发布门禁(CI/命令) | 状态 |
| --- | --- | --- | --- |
| 0:范围与基线 | A、B | `go test ./internal/metadata/`(勾选纪律)+ `make toolchain` + 构建可重复性测试;B08 拆分后逐任务独立门禁 | A 组完成;B05/B07–B10 待做 |
| 1:真实程序执行基础 | C、D、E、F、G、N 基础项 | ISA 回归门禁(`go test ./executor/cpu/...`)+ `go test ./loader/... ./vm/memory/` + 差分探针对拍(B08 后为独立 differential 任务) | 进行中 |
| 2:Linux 执行环境 | H–O;P 控制台基础 | `go test ./vm/... ./abi/... ./filesystem/...` + 差分矩阵扩充 | 进行中 |
| 3:真实语言运行时 | Q + 探针 | Q02 缺口追踪产出物 + 客体运行时测试(A04 固定的版本与摘要) | 未开始 |
| 4:产品入口与可用性 | P、R、T | `model` 契约套件(P15 接真实工厂)+ 基准(T08–T10) | 契约套件已迁入(A06/A07) |
| 5:发布 | U | U01–U14 全部勾选 + SBOM/许可清单(B10) | 未开始 |

## 未解决决策

| 决策 | 状态 |
| --- | --- |
| VM 类型/ISA/后端 | 已决:[vm-architecture.md](../adr/vm-architecture.md)(A02) |
| Node JIT 与 W^X | 已决:[node-jit.md](../adr/node-jit.md)(A05;最终配置切换在 Q24) |
| S 组适用性 | 已决:不适用(A02;推翻须新 ADR) |
| 旧 WASI 路径去留 | 待 R06(以调用方证据决定迁移或删除) |
| CPython/Node 版本 | 已固定:[toolchain.md](../runtime/toolchain.md)(A04;升级须重跑 Q 组验收) |

## 不支持项查询

全部"在册但不支持/未登记"行为统一登记于
[linux-rv64gc-1 兼容配置](../compat/linux-profile.md):
ENOTSUP 表(33 项)、声明语义表(6 项)、ENOSYS 清单(34 项)与
文件/进程/设备/网络边界;`abi/linux/common/profile_test.go` 保证
文档与登记表逐名一致。

## 最终需求 → 任务映射

| 最终需求(TODO「目标与边界」) | 任务 | 验收用例样例 |
| --- | --- | --- |
| 客体 CPU 与执行控制 | D01–D13 | `executor/cpu/riscv64` 全部向量;ISA 回归门禁 |
| 地址空间与内存映射 | E01–E15 | `vm/memory` table/paged 测试 |
| ELF 装载与 libc 启动 | F01–F12 | `loader/elf`;F09/F10 差分探针 |
| 系统调用分派与 ABI | G01–G10 | `abi/...`;registry/profile 一致性测试 |
| 进程、执行与回收 | H01–H14 | `vm/kernel_integration_test`、真实 ELF 测试 |
| 文件系统与描述符 | I01–I20 | `filesystem/...` 语义矩阵 |
| 信号与客体异常 | J01–J09 | 真实 ELF 信号投递测试 |
| 线程与同步 | K01–K10 | pthread 真实程序(待 F08/H02) |
| 时间、随机与事件 | L01–L12 | 虚拟时钟/熵源测试 |
| 网络与 socket | M01–M13 | 差分矩阵(未开始) |
| 根镜像与设备 | N01–N12 | 镜像清单与 devfs 测试 |
| 身份与隔离 | O01–O10 | 双 VM 隔离与故障注入测试 |
| Console/Terminal/Bash | P01–P15 | `model` 契约套件六场景(A07) |
| CPython/Node 客体运行 | Q01–Q31 | A04 固定版本 + Q02 缺口追踪 |
| SDK/CLI/集成 | R01–R09 | 公开 API 示例 + `test/posix` |
| 可观测性与性能 | T01–T11 | 快照/重放测试 + 基准 |
| 最终验收与发布 | U01–U14 | 阶段 5 门禁聚合 |
