# 客体 ISA 与 CPU 状态格式声明(D01)

状态:现行。本文件是 [VM 架构决策](../adr/vm-architecture.md)(A02)与
[兼容配置](../compat/linux-profile.md)(A03)的指令层细化;装载器与
CPU 的一致性由下方"一致性检查"小节列出的测试在 CI 锁定。

## 机器模型

| 项 | 声明 | 实现位置 |
| --- | --- | --- |
| 字长 | 64 位(XLEN=64) | [core.go](../../executor/cpu/riscv64/core.go) |
| 端序 | 小端(指令与数据) | 同上;装载器仅接受 ELFDATA2LSB |
| 扩展集 | RV64GC = RV64I + M + A + C + F + D;用户态附加 Zicsr 子集(cycle/instret 只读、fcsr)与 fence/fence.i(空操作) | core.go、compressed.go、float.go |
| 明确不支持 | 特权指令(S 模式 CSR/生态调用)、向量扩展、位操作扩展(B)、加密扩展(K)等——一律非法指令陷入 | TrapIllegalInstruction |
| 压缩指令 | 16 位混编取指(2/4 字节分段校验);C.FLD/FSD/FLDSP/FSDSP 报未实现 | [compressed.go](../../executor/cpu/riscv64/compressed.go) |
| 寻址 | 47 位用户地址空间(DefaultStackTop=1<<47),页 4096 字节 | [vm/memory](../../vm/memory/table.go) |

## 寄存器与状态格式

`cpu.Machine`(完整可保存/恢复的执行状态,任务切换与信号帧共用):

| 字段 | 内容 | 说明 |
| --- | --- | --- |
| PC | 程序计数 | 字节地址,可指向 2 字节压缩指令 |
| Registers[32] | x0–x31 整数寄存器 | x0 恒零(写被忽略);x2=SP、x3=GP 按 ABI 惯例无硬件特殊化 |
| Floats[32] | f0–f31 浮点寄存器 | 按位存储(NaN 装箱由 F/D 指令语义维护) |
| FCSR | 浮点控制状态 | 舍入模式 + NV/DZ/OF/UF/NX 累积标志 |

额外执行器状态:LR/SC 保留(每核一个;AMO 与重叠写使失效,Reset 清空)、
retired 指令计数(instret 上报)。

## 异常模型

同步故障以 `cpu.Trap` 上报,不携带宿主副作用:

| TrapKind | 触发 | 客体可见结果 |
| --- | --- | --- |
| TrapIllegalInstruction | 未实现/保留编码、非法组合 | SIGILL(ILL_ILLOPC),J06 |
| TrapBreakpoint | ebreak | SIGTRAP(TRAP_BRKPT) |
| TrapMemoryFault | 取指/读写未映射、保护、不对齐 | SIGSEGV/SIGBUS(MAPERR/ACCERR/ADRALN),si_addr=故障地址 |
| TrapNotImplemented | 已识别但内核未实现的指令 | fail-closed 宿主错误,不转 SIGILL(不掩盖内核缺口) |

ecall 为系统调用陷入(非异常),经 [abi/linux/riscv64](../../abi/linux/riscv64/adapter.go)
分派;时钟中断等异步事件不存在于用户态模型(时间经 clock 注入)。

## 一致性检查(客体与加载器声明一致)

- ELF 头:EI_CLASS=ELF64、EI_DATA=小端、e_machine=EM_RISCV(243)、
  e_type∈{EXEC,DYN}、浮点 ABI∈{soft,single,double}、e_flags.RVC
  ——由 [image.go](../../loader/elf/image.go) parseHeader 拒绝一切
  不匹配项;`TestLoadRejections` 覆盖。
- 状态格式:32 整数 + 32 浮点寄存器、x0 恒零、FCSR 读写、Reset 完整
  回写——`TestMachineStateFormat`(本任务新增)。
- 异常模型:全部 TrapKind 可区分且携带 PC/指令/故障地址——
  `TestTrapKindsAreDistinguished`(D12 既有)。
- 指令集行为:回归门禁(D13)全部向量测试。
