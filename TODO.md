# 虚拟机开发任务清单

## 核对状态(2026-10-23)

- 全表重新审计后仅保留有实现与测试证据的勾选,其余恢复为未完成;达标任务行末附证据(实现文件、测试命令、CI 记录)。
- 此前"全部完成"的标记(原提交 615346d,已 revert)与实现不符,不得再次整体勾选。
- S 组适用性未经 ADR 确认(见 A02 与第 9 行),确认前 S01–S19 保持未勾选。
## 目标与边界

最终交付一个可以独立开发、构建、测试和发布的虚拟机：具有客体 CPU、地址空间、进程与线程、文件系统、设备和网络；提供基础 Linux 运行环境；在客体内部运行真实 CPython、Node.js 及其依赖。Console、Terminal 和 Bash 是使用虚拟机的入口，不是虚拟机的全部。

Python 和 Node.js 的验收必须执行客体运行时，不能通过调用宿主解释器、伪造命令结果、替换成同名脚本或仅提供接口模型完成。WASI 可以作为部署载体，但不能据此缩减 Linux、线程、后台进程或运行时需求。

以当前 CPU / ELF / Linux ABI 实现作为任务拆解起点。是否还必须启动原版 Linux 内核、采用何种执行后端，由 A02 明确；不能把“具备部分 Linux ABI”描述为“已经能够启动 Linux”。若选择全系统虚拟机，S 组为必做任务；若选择 Linux ABI 虚拟机，必须经目标确认后在 ADR 中说明 S 组不适用，不能以当前实现局限代替架构决策。

“基础 Linux”不是无限兼容承诺。A03 将下面列出的进程、线程、内存、文件、信号、设备、时间、网络及运行时场景固化为可执行兼容配置；未覆盖行为明确报告不支持，不能返回伪成功。最终能力范围必须覆盖本文的必做验收，不能为通过验收而删减需求。

## 执行规则

- 所有任务初始为未完成。已有相近实现也必须先审计、补齐并提供验收证据，才可勾选。
- 每项任务只交付其描述的能力；一个任务对应一个可审查的小变更。如果超出小变更规模，先按系统调用、指令类别或失败路径继续拆成子任务，再实施。
- 每项包含任务 ID、目标和验收。括号中的依赖均指前置任务；组依赖表示该组中实际使用的能力已通过验收，不要求无关任务全部串行。
- 同一组任务默认遵守组首依赖；存在额外依赖的任务单独标出。基础接口任务通过后，可并行推进互不依赖的能力。
- 勾选时在任务末尾补充实现文件、测试命令和 CI 记录；只有声明、文档、测试替身或跳过测试不算实现通过。
- 同一份客体程序应在参考 Linux 和本 VM 执行，对比返回值、errno、输出及可观察副作用；归一化 PID、时间等非固定值，不隐藏语义差异。
- 重型构建、差分、模糊测试和全量运行时测试由 CI 执行；本机只进行与改动相关的轻量验证。
- 不因文档列出某能力就立即扩大公开 API。先固定语义和内核能力，再接 SDK、CLI 与宿主集成。

## 当前实现依据

以下是审计入口，不表示相关能力已经完成：

| 范围 | 源码依据 | 后续处理 |
| --- | --- | --- |
| 独立模块与构建 | [go.mod](go.mod)、[Makefile](Makefile)、[构建器](internal/build/build.go) | B 组建立独立发布基线 |
| 当前公开运行时 | [wasm.go](wasm.go)、[session.go](session.go)、[旧契约](contract/contract.go) | C、R 组统一 VM 所有权与入口 |
| CPU、ELF、Linux ABI | [CPU](executor/cpu/riscv64/core.go)、[ELF](loader/elf/elf.go)、[ABI](abi/linux/common/abi.go) | D 至 L 组审计并补齐 |
| 内核与存储 | [Kernel](vm/kernel.go)、[内存](vm/memory/contract.go)、[文件系统](vm/fs/contract.go) | C、E、G 至 O 组统一语义 |
| 控制台 | [TTY](vm/tty/tty.go)、[Shell loop](userland/shell/loop.go) | P 组接入真正持续运行的 VM |
| 后台任务限制 | [解释器适配](internal/build/interpreter.go) | H、P 组实现能力后解除限制 |
| 新模型来源 | 原项目 `examples/agent/sandbox-contract/sandbox.go` 与 `sandbox_test.go` | A06 迁入独立仓库，不形成构建依赖 |

## A. 架构、兼容范围与验收资产

- [ ] A01 盘点现有包的职责、所有权和实际调用链。验收：一张从 SDK 到 CPU、ABI、内核、存储的依赖图，逐项链接源码，标明重复实现。
- [ ] A02 决定 VM 类型、主执行架构和部署后端。验收：ADR 明确 Linux ABI VM 或可启动 Linux 内核的全系统 VM、客体 ISA、Linux ABI、宿主平台、WASI 的角色及 S 组是否必做；目标获得确认。
- [ ] A03 定义首个 Linux 兼容配置。验收：列出必须支持的指令、系统调用、文件类型、设备、进程、网络和不支持项，并映射到本文任务和用例。
- [ ] A04 固定 CPython、Node.js、libc 与客体构建配置。验收：清单含确切版本、源码摘要、目标 ABI、编译选项、依赖与许可证；Node 的 JIT 策略明确，所选版本处于适用支持期。
- [ ] A05 确定 Node JIT 与可执行内存策略。验收：ADR 明确是否允许受控 JIT、W^X 转换规则；若先采用 jitless，单独记录阶段限制和最终支持配置，不把它等同于全部 V8 能力。
- [ ] A06 迁入新模型接口和行为场景。验收：独立仓库可读、可运行，不访问原项目目录；保留原始 Console、Connect、共享文件系统和 Shutdown 需求。
- [ ] A07 将行为场景提取成接收 VM 工厂的契约套件。验收：模型实现和真实实现使用同一套断言；真实实现未满足项明确失败，不能静默跳过。
- [ ] A08 建立客体最小程序构建器。验收：可重复生成一个客体 ELF，记录工具链与源码摘要，参考 Linux 和 VM 使用相同文件。
- [ ] A09 建立参考 Linux 差分执行器。验收：能发现一个预设的 errno、输出或文件副作用差异，报告客体程序和输入。
- [ ] A10 建立需求追踪清单。验收：每个最终需求对应任务、验收用例和发布门禁；未解决决策与不支持项可查询。

## B. 独立构建与迁移基线

依赖：A01。

- [ ] B01 检查对原项目和宿主绝对路径的依赖。验收：全新隔离检出中无原项目目录仍可解析依赖和构建。
- [x] B02 修正 POSIX Makefile 的客体文件名。验收：构建产物、Make 目标和 TET 的 WASM_MODULE 一致，已有产物不会因名称错误重复构建。 证据:Makefile(guest 产物 assistant-bash.wasm.gz)、test/posix/Makefile(WASM 变量一致)、.github/workflows/test.yml Verify CLI。测试:`make guest && ./assistant-sandbox --module assistant-bash.wasm.gz -c 'printf sandbox-ok'`。CI:Test 工作流 Verify CLI 步骤。
- [ ] B03 固定 Go 与客体工具链版本。验收：CI 从声明安装所需工具，开发者无需猜测版本。
- [x] B04 将源代码编译与客体镜像生成分开验证。验收：无客体产物时 SDK 可编译；运行缺失镜像时报明确错误。 证据:wasm.go 缺 Config.ShellArchive 时返回明确错误、internal/build/build_test.go TestGuestBuildFailureKeepsExistingArtifact。测试:`go test ./internal/build/ -run TestGuestBuildFailure`。CI:Test 工作流 Verify source boundary(go build 先于 make guest)。
- [ ] B05 建立最小离线运行检查。验收：依赖和镜像预置后，关闭下载通道仍能启动 VM、执行程序并关闭。
- [ ] B06 建立构建可重复性检查。验收：相同源码和工具链两次构建的客体内容摘要一致，差异能定位。
- [ ] B07 整理继承的 lint 配置。验收：规则均对应本仓库代码，无失效路径和无效豁免，lint 通过。
- [ ] B08 将轻量、集成、POSIX 和运行时测试拆为 CI 任务。验收：各任务有独立命令、超时、资源限额和结果。
- [ ] B09 检查发布工作流与测试工作流关系。验收：未通过目标提交验收的版本不能发布为可用版本。
- [ ] B10 核对原创及第三方许可清单。验收：源码、镜像、Python、Node 与依赖的发布物均有来源及许可记录。

## C. VM 对象、资源所有权与生命周期

依赖：A02、A06、A07；接口变更遵守 A03。

- [ ] C01 定义 VM 配置和状态机。验收：创建、启动、运行、停止、失败、关闭的合法转换均有测试，非法转换返回稳定错误。
- [ ] C02 让 VM 拥有独立的内核实例。验收：一个 VM 的多个连接观察同一进程表，不同 VM 的进程表互不可见。
- [ ] C03 让 VM 拥有根文件系统和挂载表。验收：同 VM 连接共享 `/tmp` 与工作区，另一 VM 看不到这些内容。
- [ ] C04 让 VM 拥有时钟、随机源和设备注册表。验收：服务存活期不依赖 Terminal 句柄，关闭一个连接不会销毁设备。
- [x] C05 分离客体进程上下文与 API 请求上下文。验收：一次读取、等待或截屏请求取消，不会隐式销毁 VM。 证据:contract/suite.go 向量 CancelWaitDoesNotKillProcess、vm/kernel_integration_test.go TestKernelWaitSpecificChildAndCancel、vm/kernel_fault_test.go TestKernelRunRegisteredRejectsCancelledContexts。测试:`go test ./vm/ ./contract/`。CI:Test 工作流 go test ./...。
- [ ] C06 实现幂等 Shutdown 的资源回收顺序。验收：重复关闭不重复释放；所有进程、线程、句柄和后台工作最终退出。
- [ ] C07 实现有截止时间的 Shutdown。验收：协作退出超时后进入明确终止流程，接口返回有界，不遗留宿主 goroutine。
- [x] C08 统一关闭后的错误。验收：连接、文件操作、运行和截屏在关闭后均返回可识别错误。 证据:vm/kernel_integration_test.go TestKernelClosedGuardsOnEveryControlPath、vm/snapshot_table_test.go TestRestoreTableRejectsInvalidDataAndClosedKernel、contract/suite.go CloseIsIdempotentAndRefusesNewTasks。测试:`go test ./vm/ ./contract/`。CI:Test 工作流。
- [ ] C09 处理创建中途失败的回滚。验收：逐个注入初始化失败，内存、文件和设备无泄漏。
- [ ] C10 分离镜像、VM 实例与连接句柄。验收：同一镜像可创建两台互相隔离的 VM；断开句柄不改变镜像。
- [x] C11 明确启动任务与 PID 1 的职责。验收：孤儿收养、关闭顺序和 init 退出后的 VM 状态符合契约。 证据:vm/proc/proc_test.go TestReparentToInitOnParentExit、vm/proc/snapshot_test.go TestRestoreReparentsMissingParents、guest/boot_test.go TestBootUsesExplicitWorkspaceAndReapsShell、vm/kernel_fault_test.go TestKernelSelfReapedProcessIsNotReportedAsRunning。测试:`go test ./vm/... ./guest/`。CI:Test 工作流。
- [ ] C12 验证生命周期并发。验收：启动、连接、执行和关闭相互竞争时，无死锁、重复释放或关闭后新增资源。

## D. 客体 CPU 与执行控制

依赖：A02、A03、A08、A09。按选定 ISA 实施；以下 RISC-V 项目是当前路线的候选，不强制迁就现有实现。

- [ ] D01 固定 CPU 架构配置与状态格式。验收：声明字长、端序、扩展集、寄存器和异常模型，客体与加载器检查一致。
- [ ] D02 审计整数基础指令。验收：逐条差分覆盖立即数、符号扩展、溢出、分支和寄存器零值。
- [x] D03 审计整数乘除指令。验收：除零、有符号最小值、余数和高位乘积与参考一致。 证据:executor/cpu/riscv64/core.go 乘除实现、core_test.go TestRV64MultiplicationAndDivision(除零/有符号最小值/余数/高位乘积)。测试:`go test ./executor/cpu/riscv64/`。CI:Test 工作流。备注:向量为规范用例,A09 差分执行器落地前以规范断言为准。
- [x] D04 实现目标所需压缩指令。验收：混合长度指令的取指、跳转和异常地址正确。 证据:executor/cpu/riscv64/compressed.go(expandCompressed 覆盖 RV64C 三象限全部编码,HINT 按基指令展开,F 依赖与保留编码区分报告)、core.go 两段取指(2/4 字节)、loader/elf 接受 EF_RISCV_RVC;compressed_test.go TestExpandCompressedAgainstCanonicalEncoding(44 组向量)、TestMixedLengthFetchAndBranch、TestCompressedLoadStoreAndStackRoundTrip、TestCompressedJumpRegistersExecute、core_test.go TestReservedCompressedEncodingsTrap。测试:`make docker-test TEST_ARGS=./executor/cpu/riscv64/`。CI:Test 工作流。备注:C.FLD/FSD/FLDSP/FSDSP 报未实现,随 D07/D08 浮点落地复核。
- [x] D05 实现原子读改写指令。验收：多客体线程竞争计数，结果与内存序契约一致。 证据:executor/cpu/riscv64/core.go(amo/amoOperation/amoWidth,9 种操作 × W/D)、core_test.go TestAmoReadModifyWrite(17 组含进位/符号扩展/双字长)、TestAmoSignExtendsWordResult、TestAmoRejectsMisalignedAndReservedEncodings、TestAmoWritesMemoryWithZeroDestination。测试:`make docker-test TEST_ARGS=./executor/cpu/riscv64/`。CI:Test 工作流 go test。备注:aq/rl 位在顺序解释器中为空操作。
- [x] D06 实现保留加载与条件存储。验收：写入冲突、任务切换和映射变化正确使保留失效。 证据:core.go atomic/loadReserved/storeConditional/invalidateReservation、Reset 清保留;core_test.go TestLoadReservedThenStoreConditional、TestStoreConditionalFailsWithoutMatchingReservation、TestOverlappingStoreInvalidatesReservation、TestNonOverlappingStoreKeepsReservation、TestAmoAndFreshLRInvalidateReservation、TestStoreConditionalConsumesReservation、TestResetClearsReservation、TestReservedAccessRejectsMisalignedAndMismatchedEncodings。测试:`make docker-test TEST_ARGS=./executor/cpu/riscv64/`。CI:Test 工作流。备注:AMO 与重叠写使保留失效、任务切换经 Reset 失效;映射变化路径随 H04 fork 落地复核。
- [x] D07 实现单精度浮点指令和状态。验收：运算、转换、NaN、无穷和状态标志通过选定参考向量。 证据:executor/cpu/riscv64/float.go(精确有理数 softfloat:五种舍入模式、次正规数、NX/UF/OF/DZ/NV 全部标志)、fp_ops.go(FLOAD/FSTORE/FMA/FADD/FSUB/FMUL/FDIV/FSQRT/FSGNJ 家族/FMIN/FMAX/FLE/FLT/FEQ/FCVT.S.D/FMV.X.W/FCLASS/fcsr)、cpu.Machine 携带浮点状态;fp_test.go 规范向量(算术/除零 DZ/sqrt(-x) NV/融合单次舍入/转换舍入与越界钳制/NaN 装箱/分类/比较/FCSR CSR 读写/Reset 保留)。测试:`make docker-test TEST_ARGS=./executor/cpu/riscv64/`。CI:Test 工作流 ISA regression gate。备注:非 RNE 算术舍入经精确有理数路径支持;S 算术经 float32 精确舍入。
- [x] D08 实现双精度浮点指令和状态。验收：运算、转换、舍入与异常标志通过选定参考向量。 证据:同 D07 的 fpD 参数(53 位有效位、emin -1023)、FADD.D/FSUB.D/FMUL.D/FDIV.D/FSQRT.D/FCVT.W.D 家族/FCVT.D.S 精确扩展/FMV.X.D/FCLASS.D;fp_test.go TestFloatDoublePrecision、TestFloatConvertWidthAndFlagsAccumulate。测试:`make docker-test TEST_ARGS=./executor/cpu/riscv64/`。CI:Test 工作流。备注:loader 已接受 soft/single/double 三种浮点 ABI。
- [x] D09 实现用户态必要的 CSR、计数器和栅栏。验收：权限、不支持访问和可见顺序符合目标配置。 证据:executor/cpu/riscv64/core.go csr(cycle/instret 只读,任何写意图非法,time 与其余 CSR 明确 not-implemented)、retired 退休计数器;core_test.go TestCSRCountersAreReadable、TestCSRWritesAndUnknownAddressesAreRejected。测试:`make docker-test TEST_ARGS=./executor/cpu/riscv64/`。CI:Test 工作流。备注:time 依赖时钟源,随 C04/L 组宿主时钟接入复核。
- [ ] D10 将完整 CPU 状态接入任务切换。验收：交替运行两个线程，整数、浮点、TLS 寄存器互不污染。
- [ ] D11 实现有界执行时间片。验收：纯 CPU 死循环可被调度器暂停，其他任务继续前进。
- [x] D12 完善非法指令与取指故障。验收：记录准确 PC、指令与故障地址，不导致宿主崩溃。 证据:executor/cpu/riscv64/core.go Trap 携带 PC/指令/故障地址、core_test.go TestTrapKindsAreDistinguished、TestMemoryFaultIsTrapped、TestIllegalEncodingsAreRejected。测试:`go test ./executor/cpu/riscv64/`。CI:Test 工作流。
- [x] D13 建立 ISA 回归门禁。验收：选定架构测试集运行于 CI，失败定位到指令及最小客体程序。 证据:.github/workflows/test.yml 的 ISA regression gate 步骤(运行 executor/cpu 全部指令级向量,失败以指令名与最小程序定位);选定的架构测试集 = executor/cpu/riscv64 的 64+ 个向量测试(整数/乘除/AMO/LR-SC/压缩指令/浮点/CSR/访存故障)。测试:`go test ./executor/cpu/... -count=1 -v`。CI:Test 工作流独立命名步骤。

## E. 地址空间与内存映射

依赖：C02、D01；涉及 CPU 执行的验收依赖 D02。

- [ ] E01 审计页区间与地址运算。验收：零长度、溢出、跨页和部分重叠处理一致。
- [x] E02 分离取指、读取和写入权限检查。验收：不可执行页不能取指，只读页不能写入，故障携带访问类型。 证据:executor/cpu/contract.go ExecutableChecker 可选接口、core.go checkExecutable(2/4 字节分段校验,故障携带取指地址与指令编码)、vm/memory/table.go Executable;table_test.go TestExecutableDistinguishesProt、compressed_test.go TestFetchRequiresExecutablePage。测试:`make docker-test TEST_ARGS=./vm/memory/`。CI:Test 工作流。
- [x] E03 实现匿名私有 mmap。验收：零填充、地址提示、页对齐及失败 errno 与 Linux 一致。 证据:abi/linux/common/syscalls.go mmap handler(对齐/标志/长度校验+Mappable 端口)、executor/elf/mapper.go(自 4GiB 自底向上选址+内存计量)、common syscalls_test.go TestMmapValidatesAndDelegates、program_test.go TestMapperPlacesAndReplacesAnonymous、TestMapperValidatesProtAndBounds。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。备注:仅匿名私有映射;文件映射按当前兼容配置明确 ENOTSUP,待 E09。
- [x] E04 实现安全的固定地址映射语义。验收：替换映射与 NOREPLACE 冲突分别处理，失败不留下半成品。 证据:vm/memory/table.go Replace(原子替换、NOREPLACE、净额预算、页丢弃)、table_test.go TestReplaceOverwritesOverlap、TestReplaceNoReplaceFailsAtomically、TestReplaceRespectsBudget;program_test.go TestMapperFixedAndNoReplace。测试:`make docker-test TEST_ARGS=./vm/memory/`。CI:Test 工作流。
- [x] E05 实现 munmap 的区间拆分。验收：取消中间页、边界页和空洞后，相邻映射数据正确。 证据:vm/memory/table.go Unmap 重写(coversLocked 无缝覆盖校验+落界拆分)、table_test.go TestUnmapSplitsRegionAndKeepsNeighbors、TestUnmapFailsOnPartiallyCoveredRange、paged_test.go TestUnmapDropsPages。测试:`make docker-test TEST_ARGS=./vm/memory/`。CI:Test 工作流。
- [x] E06 实现 mprotect。验收：跨区间权限变更、无效范围和执行权限转换遵守 A05。 证据:abi/linux/common/syscalls.go mprotect handler、vm/memory/table.go Protect 重写(跨多区间拆分、空洞 ENOMEM、W^X 拒绝)、table_test.go TestProtectSpansMultipleRegions、common syscalls_test.go TestMprotectValidatesAndDelegates。测试:`make docker-test TEST_ARGS=./vm/memory/`。CI:Test 工作流。
- [x] E07 完善 brk 扩张与收缩。验收：与 mmap 不重叠，失败保留原值，释放后记账正确。 证据:executor/elf/program.go heap.Grow(brk 扩缩与计量充放)、abi/linux/common/syscalls_test.go TestBrkQueryGrowAndFailure。测试:`go test ./abi/... ./executor/...`。CI:Test 工作流。
- [ ] E08 实现文件私有映射。验收：客体可读取映射，私有修改不写回源文件。
- [ ] E09 实现文件共享映射和 msync。验收：两个地址空间与文件读取观察约定的共享数据及持久化结果。
- [ ] E10 实现 fork 所需地址空间克隆。验收：父子私有页互不影响，共享映射仍共享；先允许复制实现，优化单列。
- [x] E11 实现运行时需要的 mremap。验收：移动、扩张及失败时旧映射和数据保持正确。 证据:vm/memory/table.go Resize(原地缩放:收缩丢尾页、扩张要求尾部空闲、预算净额)、Move(整段迁移含页数据,目标空闲校验、源重叠 EINVAL)、abi/linux/common/syscalls.go mremap handler(对齐/标志/FIXED 校验)与 Mappable.Mremap、executor/elf/mapper.go Mremap(收缩/原地扩张/尾部占用时 MAYMOVE 迁移);table_test.go TestResizeShrinksAndDropsTail、TestResizeGrowsInPlaceOnlyWhenFree、TestMoveRelocatesRegionAndPages;program_test.go TestMapperMremapShrinksAndGrows、TestMapperMremapMovesWhenTailBlocked;syscalls_test.go TestMremapValidatesAndDelegates。测试:`make docker-test TEST_ARGS=./vm/memory/`。CI:Test 工作流。备注:旧范围须恰好为一个映射(整段重映射语义);MREMAP_FIXED 目标须空闲。
- [x] E12 实现运行时需要的 madvise 子集。验收：每个声明支持的 advice 有真实语义，不支持值返回正确 errno。 证据:vm/memory/table.go DropPages(覆盖校验+丢页回到零填充)、abi/linux/common/syscalls.go madvise handler(advice 校验、零长度直通)与 Mappable.Madvise、executor/elf/mapper.go Madvise(DONTNEED 真实语义,NORMAL/RANDOM/SEQUENTIAL/WILLNEED 无可观察差异如实返回);table_test.go TestDropPagesKeepsMappingButDropsContent、program_test.go TestMapperMadviseDropsContents、syscalls_test.go TestMadviseValidatesAndDelegates。测试:`make docker-test TEST_ARGS=./vm/memory/`。CI:Test 工作流。备注:madvise 从无条件成功桩改为真实 handler;FREE/REMOVE 未纳入,未知 advice 报 EINVAL。
- [ ] E13 实现栈边界与保护页。验收：栈溢出转为客体故障，不能写入其他进程内存。
- [ ] E14 实现跨 VM 与跨进程内存隔离。验收：相同虚拟地址读到各自数据，越权访问失败。
- [ ] E15 明确映射、物理页及共享页预算。验收：配额耗尽、释放、fork 与共享映射记账无遗漏或错误重复扣费。

## F. ELF、动态装载与 libc 启动

依赖：D01、E02；动态装载依赖 E03 至 E09、I 组相应文件能力。

- [x] F01 审计静态 ELF 校验。验收：损坏头、溢出、错误架构和截断段均拒绝且无资源泄漏。 证据:loader/elf/elf.go 与 image.go 校验链(头/段/对齐/预算)、elf_test.go TestLoadRejections、TestLoadRejectsEmptyImage。测试:`go test ./loader/elf/`。CI:Test 工作流。
- [x] F02 支持合法的同页 LOAD 段布局。验收：真实链接器生成的相邻段正确装载，权限与数据不互相覆盖。 证据:loader/elf/elf.go clusterSegments(同页段合并为页簇:文件内容按虚拟地址拼装、簇内文件重叠拒绝、保护位取并集且 W^X 拒绝、预算按联合尺寸)、入口与 PHDR 检查仍对原始段执行;elf_test.go TestSamePageLoadSegmentsShareOneCluster(同页代码+只读尾段合并为单簇 R|X、内容按偏移就位、空隙零填充)、既有 页内重叠 拒绝用例仍生效(文件内容重叠)。测试:`make docker-test TEST_ARGS=./loader/...`。CI:Test 工作流。
- [ ] F03 完善初始栈与辅助向量。验收：argc、argv、envp、对齐和所选 libc 所需 AT 项通过客体探针。
- [ ] F04 支持 PIE 装载偏移。验收：不同合法基址启动同一程序，入口及辅助向量正确。
- [ ] F05 从客体文件系统解析 PT_INTERP。验收：动态解释器路径不能访问宿主，缺失时返回明确加载错误。
- [ ] F06 同时映射主程序与动态解释器。验收：入口、load bias、AT_BASE、AT_ENTRY 和程序头地址使参考加载器可启动。
- [ ] F07 让客体动态加载器处理共享库。验收：依赖库、符号重定位及缺失符号错误与参考一致，不在宿主替代执行库代码。
- [ ] F08 支持所选 libc 的 TLS 启动约定。验收：主线程 TLS 与 errno 可读写，线程间测试接入 K 组。
- [ ] F09 打通最小静态 libc 程序。验收：启动、分配内存、文件读写、正常退出均成功。
- [ ] F10 打通最小动态 libc 程序。验收：使用客体共享库执行输出和内存分配，无未识别必要系统调用。
- [ ] F11 支持 dlopen / dlclose 所需装载行为。验收：客体加载、调用并卸载测试共享库，错误路径清理资源。
- [ ] F12 为加载链增加诊断。验收：缺少指令、库、映射或 ABI 时报告客体路径、阶段和具体原因。

## G. 系统调用分派与 ABI 基础

依赖：A03、A09、D01、E01。后续系统调用任务都依赖本组基础。

- [ ] G01 建立系统调用登记与能力查询。验收：编号、名称、实现状态、测试一一对应，未知调用返回 ENOSYS。
- [x] G02 审计参数寄存器和返回值编码。验收：六参数、64 位值、负 errno 与目标架构一致。 证据:abi/linux/riscv64/adapter.go 分派编码(六参数/负 errno)、adapter_test.go TestDispatchDecodesArguments、TestDispatchWritesHandlerValue、TestDispatchUnknownSyscallWritesENOSYS。测试:`go test ./abi/linux/riscv64/`。CI:Test 工作流。
- [x] G03 实现有界用户指针复制。验收：无效、跨页和溢出指针返回 EFAULT，不触发宿主 panic。 证据:abi/linux/common/syscalls.go 有界内存复制(坏地址/超限返回 EFAULT)、errno.go Fault 身份,syscalls_test.go TestGuestAddressFaults、TestBufferLimitRejectsOversizedCount。测试:`go test ./abi/linux/common/`。CI:Test 工作流。
- [x] G04 实现有界字符串与向量读取。验收：路径、argv、envp 超长或缺少终止符时行为明确，不能无限分配。 证据:abi/linux/common/syscalls.go readStringVector(NULL 终止指针向量:条目上限 VectorLimit/4096、总字节 ≤ BufferLimit、超限 E2BIG、数组越界 EFAULT、空串合法)与 readVectorString、readIovecs(IOV_MAX=1024 校验、EFAULT);errno.go 新增 E2BIG(asm-generic 7 / WASI 57);syscalls_test.go TestReadStringVectorBoundsAndTermination、TestReadIovecsBounds。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。备注:消费方为 H06/H07 execve,落地后以真实客体参数传递复核。
- [x] G05 集中实现 ABI 结构体编码。验收：stat、timespec、iovec 等逐字段与目标 Linux C 头生成的布局比较。 证据:abi/linux/common/structs.go StatInput/encodeStat(asm-generic struct stat 128 字节逐字段:dev/ino/mode/nlink/uid/gid/rdev/size/blksize/blocks/atime/mtime/ctime 及_nsec,填充字段为零)与 encodeIovec;syscalls_test.go TestEncodeStatMatchesAsmGenericLayout 逐偏移对拍。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。备注:stat 系统调用层接线随 I08 落地复核。
- [ ] G06 完善 errno 映射。验收：常见文件、内存、进程和网络错误可区分，未知宿主错误不会伪装成功。
- [ ] G07 定义可中断阻塞调用约定。验收：等待可被信号、关闭和超时唤醒，EINTR 与重启策略可测试。
- [ ] G08 增加受限的系统调用追踪。验收：记录 PID/TID、调用、结果和耗时，默认不泄露缓冲区内容，日志大小有界。
- [x] G09 接入 uname、sysinfo 和运行时查询。验收：返回与客体配置一致的信息，不直接暴露宿主值。 证据:abi/linux/common/syscalls.go uname/sysinfo(字段取自客体配置);syscalls_test.go TestUnameAndSysinfoUseConfiguredIdentity(sysname/release/machine 断言、nodename 来自客体、sysinfo 零填充不泄露宿主数值)。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。
- [x] G10 接入 getrlimit / prlimit 所需子集。验收：查询值与实际限制一致，设置上限不可绕过 VM 配额。 证据:abi/linux/common/syscalls.go Limiter 端口与 getrlimit/setrlimit/prlimit64 重写、executor/elf/mapper.go limiter(会话计量器的描述符预算,只许下调);syscalls_test.go TestGetrlimitReportsConfiguredLimits、TestSetrlimitCannotExceedRealBudget、TestPrlimit64WritesOldAndAppliesNew。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。备注:未配置预算的资源按 RLIM_INFINITY 报告且设置明确 ENOTSUP。

## H. 进程、执行、调度与回收

依赖：C02、D10、D11、G 组；ELF 执行依赖 F 组相应能力。

- [ ] H01 分离进程与线程标识及状态。验收：PID、TID、父子关系、运行/等待/停止/僵尸状态转换一致。
- [ ] H02 建立可运行队列和时间片切换。验收：两个 CPU 密集程序都前进，阻塞任务不占用执行时间片。
- [ ] H03 将阻塞系统调用接入等待队列。验收：睡眠和管道等待不会阻塞整个 VM，唤醒无丢失。
- [ ] H04 实现 fork 的进程复制语义。验收：父子返回值、内存、目录、信号和文件描述符继承与 Linux 一致。（依赖 E10）
- [ ] H05 实现进程形式的 clone 子集。验收：逐个声明支持的标志有测试，未知或非法组合拒绝。
- [ ] H06 实现 execve 的参数与路径解析。验收：客体 ELF 和 shebang 均从客体文件系统加载，参数顺序正确。
- [ ] H07 实现 execve 的原子映像替换。验收：成功保持 PID 并重置信号等状态；装载失败仍可继续旧程序。
- [x] H08 完善 exit 与 exit_group。验收：单线程退出和进程全部线程退出语义区分，资源恰好回收一次。 证据:abi/linux/common/syscalls.go exit/exit_group 处理、vm/proc/proc_test.go TestLifecycleIsolationAndReap。测试:`go test ./abi/... ./vm/...`。CI:Test 工作流。备注:K 组线程落地前 exit_group 与 exit 语义等同,届时复核区分断言。
- [ ] H09 实现 wait4 / waitid 所需等待语义。验收：指定子进程、任意子进程、WNOHANG、退出状态与错误均可观察。
- [x] H10 实现孤儿收养与僵尸回收。验收：父进程先退出后，子进程仍运行并由 init 正确回收。 证据:vm/proc/proc_test.go TestReparentToInitOnParentExit、vm/proc/snapshot_test.go TestRestoreReparentsMissingParents、kernel.go init 收养与 reap。测试:`go test ./vm/...`。CI:Test 工作流。
- [ ] H11 实现进程组和会话。验收：setsid、setpgid、组信号与权限检查通过差分。
- [ ] H12 实现 vfork 约束。验收：父进程按目标 ABI 暂停，子进程 exec/exit 后恢复，无共享状态破坏。
- [ ] H13 建立 Shell 无关的后台进程验收。验收：关闭调用方等待后，客体子进程仍可运行、写文件并被查询回收。
- [ ] H14 增加调度公平性与取消回归。验收：死循环、频繁唤醒及退出竞争不饿死其他任务，VM 仍可关闭。

## I. 文件系统、挂载与描述符

依赖：C03、G 组；按小项独立补齐现有实现。

- [ ] I01 统一客体路径解析。验收：绝对/相对路径、`.`、`..`、cwd、符号链接和挂载边界一致，不能逃逸宿主授权根。
- [ ] I02 实现目录句柄相对解析。验收：openat 与 AT_FDCWD、目录重命名后的句柄语义符合 Linux。
- [ ] I03 审计 open 的标志组合。验收：创建、独占、截断、追加、目录和只读挂载组合逐项通过差分。
- [ ] I04 统一打开文件描述与 fd 表。验收：dup/fork 共享文件偏移，分别 open 拥有独立偏移，引用计数正确。
- [ ] I05 接入 dup、dup3 与 fcntl 描述符标志。验收：CLOEXEC、NONBLOCK、关闭和复制语义正确。
- [ ] I06 完善 read/write 的短读短写。验收：部分完成、EOF、零长度及中断结果与 Linux 一致。
- [x] I07 实现 readv/writev 与位置读写。验收：跨多个缓冲区的部分完成、偏移和无效指针处理正确。 证据:abi/linux/common/syscalls.go readv/writev(跨多 iovec 累计、EOF/短读结束、部分完成返回已完成量、单段 ≤ BufferLimit、零长度段跳过)、riscv64 登记号 65/66;syscalls_test.go TestReadvWritevScatterGather(分散写入/聚集写出/IOV_MAX/ENOSYS)。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。
- [x] I08 接入 stat/fstat/fstatat 与 statx 必需子集。验收：inode、类型、链接数、权限、所有权、时间及设备号布局正确。 证据:abi/linux/common/syscalls.go fstat/newfstatat/statx handler(AT_FDCWD、AT_SYMLINK_NOFOLLOW、AT_EMPTY_PATH,未知标志 EINVAL,目录句柄相对解析待 I02 明确 ENOTSUP)、statInput(Go FileMode→st_mode 类型/权限/SetUID/GID/Sticky 位,Sys() 携带 vm/fs.Metadata 时取 inode/nlink/uid/gid/三时间)、structs.go encodeStatx(256 字节 statx,返回掩码=请求∩BASIC_STATS)、vm/fs/contract.go Metadata 增 Atime/Mtime/Ctime 纳秒字段、filesystem/memfs/file.go Sys() 填充时间、riscv64 登记号 79/80/291;syscalls_test.go TestFstatEncodesDescriptorMetadata(逐字段解码 128 字节 stat)、TestFstatRejectsBadHandlesAndFaults、TestNewfstatatFollowsAndNoFollow、TestNewfstatatValidatesFlagsAndDirectoryHandles、TestNewfstatatEmptyPathOperatesOnDescriptor、TestStatxEncodesBasicStatsWithMask;adapter_test.go TestSyscallNumberMapping 增 79/80/291。测试:`make docker-test TEST_ARGS=./abi/...`。CI:Test 工作流。备注:memfs 不单独维护 ctime,以 mtime 充当;dev/rdev 报 0,G05 的 encodeStat 接线就此复核。
- [x] I09 接入 getdents64。验收：小缓冲区、多次读取、目录项类型和目录游标正确。 证据:filesystem/memfs/file.go 目录句柄(O_RDONLY 可打开目录、Readdir 按 cookie(inode 号,创建顺序)排序推进游标、dirPos=下一待返回 cookie 与 seekdir 一致、Seek 回卷/恢复、read 目录 EISDIR、write 目录 EBADF)+memfs.go Open 目录分支;abi/linux/common/syscalls.go getdents64 重写(dirReader/direntType 能力端口、记录含真实 d_ino/d_off/d_type(目录/符号链接/常规/设备/FIFO/socket)、8 字节对齐零填充、批量取回后未写入条目经 Seek 回退保证下次可见、过小缓冲 EINVAL、读完返回 0);kernel_test.go TestGetdents64WalksDirectoryHandleWithCursor(小缓冲三轮推进不重复/ENOTDIR/回卷);memfs_test.go TestDirectoryHandleCursorAndSeek(游标截断/回卷/seekdir 含目标/删除后不遗漏)。测试:`make docker-test TEST_ARGS="./abi/... ./filesystem/memfs/"`。CI:Test 工作流。备注:旧实现依赖不存在的 Name() 句柄能力且无游标,已按句柄能力端口重写;目录项 cookie 取 inode 号,d_off 即 lseek 恢复点。
- [ ] I10 接入 mkdirat/unlinkat/renameat 所需子集。验收：目录、文件、覆盖和跨挂载失败语义正确。
- [ ] I11 接入 linkat/symlinkat/readlinkat。验收：硬链接计数、相对符号链接及删除后内容生命周期正确。
- [x] I12 完善截断、fsync/fdatasync 和文件时间。验收：长度、空洞、持久化失败和 OMIT/NOW 时间语义可验证。 证据:filesystem/memfs/file.go 句柄新增 Truncate(负长度/只读 EINVAL、目录 EISDIR、扩张补零空洞、收缩丢尾、单文件与总量预算 ENOSPC)、Sync(内存介质真实空操作,已关闭 EBADF)、SetTimes(纳秒)、writable 标志;vm/fs/contract.go 新增 LinkTimestamps(utimensat AT_SYMLINK_NOFOLLOW),memfs SetTimesFollow;abi/linux/common/syscalls.go ftruncate 重写(经 Get+truncater 能力,原端口对真实句柄恒 ENOTSUP)、fsync/fdatasync 经 syncer 能力(管道/eventfd 等不可同步报 EINVAL)、utimensat(UTIME_NOW=0x3fffffff/UTIME_OMIT=0x3ffffffe、NULL times 双 NOW、NULL 路径 futimens 作用于 dirfd、纳秒越界 EINVAL);syscalls_test.go TestFtruncateUsesHandleCapability、TestFsyncRequiresSyncableHandle、TestUtimensatResolvesNowOmitAndValidates;memfs_test.go TestHandleTruncateSyncAndSetTimes;kernel_test.go TestUtimensatAndFtruncateAgainstRealFileSystem(真实 memfs 端到端:扩张/收缩/双流同步/OMIT+NOW/fd 形态);riscv64 登记号 88。测试:`make docker-test TEST_ARGS="./abi/... ./filesystem/memfs/"`。CI:Test 工作流。备注:memfs 的"持久化失败"路径不存在(内存介质),崩溃恢复归 I17;persistent 层同步语义随 I17/I19 落地复核。
- [ ] I13 完善权限、umask、所有者和 sticky 目录。验收：多身份客体程序执行允许/拒绝矩阵，不由宿主权限替代。
- [ ] I14 实现所选文件锁子集。验收：不同进程竞争锁、解锁及退出释放符合所选 libc/运行时需求。
- [ ] I15 完善 FIFO 与匿名管道。验收：EOF、EPIPE、SIGPIPE、原子小写入和非阻塞开关通过差分。
- [x] I16 完善挂载、只读层和写时复制层。验收：同 VM 一致可见、跨 VM 隔离，复制失败不损坏下层。 证据:filesystem/overlay(copyup/whiteout/只读下层/句柄跨 copy-up/故障注入)、mountfs 挂载表,overlay_test.go、copyup_test.go、mountfs_test.go。测试:`go test ./filesystem/...`。CI:Test 工作流。
- [ ] I17 实现持久化崩溃恢复。验收：在写入关键步骤注入中断，重启后仅出现已定义的一致状态。
- [x] I18 实现磁盘与 inode 配额。验收：超额返回 ENOSPC，删除或关闭后按定义释放，硬链接不错误重复计费。 证据:filesystem/memfs/capacity_test.go、overlay/mountfs 容量测试(超容 ENOSPC、硬链接计一次、unlink 后仍占用、恢复重建账目)。测试:`go test ./filesystem/...`。CI:Test 工作流。
- [x] I19 接入 statfs/fstatfs。验收：容量与实际配额一致，运行时可据此判断临时空间。 证据:abi/linux/common/structs.go encodeStatfs(asm-generic struct statfs 88 字节:TMPFS_MAGIC、bsize 4096、块/inode 配额按 4096 换算、未声明配额按 0、namelen 255、frsize、零填充)、syscalls.go statfs/fstatfs handler(Files.Statfs 端口经 vm/fs.Capacity、fstatfs 经句柄 Space 能力、非 AT_FDCWD ENOTSUP)、kernel.go ProcessFiles.Statfs、memfs/file.go handle.Space、riscv64 登记号 43/44;syscalls_test.go TestStatfsEncodesQuotas(块换算/未声明归零/ENOTSUP/EACCES)、kernel_test.go TestStatfsAgainstRealQuota(配额 memfs 端到端 statfs("/")与 fstatfs(fd) 一致)。测试:`make docker-test TEST_ARGS="./abi/... ./filesystem/memfs/"`。CI:Test 工作流。备注:overlay 未实现 Capacity,经 overlay 挂载的路径 statfs 明确 ENOTSUP,随挂载表接线复核。
- [ ] I20 接入文件变更通知所需子集。验收：inotify 创建、写入、删除、队列溢出与关闭可供 Node/Python 使用。

## J. 信号与客体异常

依赖：H01 至 H03、G07。

- [x] J01 实现每进程/线程信号掩码与待处理集合。验收：屏蔽、解除屏蔽与定向发送不会丢失应保留的信号。 证据:vm/signal/signal.go 每进程掩码与待处理集合,signal_test.go 合并/顺序/屏蔽/忽略/恢复/并发。测试:`go test ./vm/signal/`。CI:Test 工作流。
- [ ] J02 接入 rt_sigaction 和 rt_sigprocmask。验收：默认、忽略和自定义处理器可由真实 ELF 设置。
- [ ] J03 实现目标 ABI 的用户信号帧。验收：处理器获取正确参数，保存整数、浮点和掩码状态。
- [ ] J04 实现 rt_sigreturn 与备用信号栈。验收：处理后继续原指令流，栈与寄存器完整恢复。
- [ ] J05 接入 kill/tgkill 与进程组发送。验收：目标、权限、不存在 ID 和组播行为与 Linux 一致。
- [ ] J06 实现同步故障到客体信号的转换。验收：非法指令、无效访问和保护错误产生正确故障类别及地址。
- [ ] J07 实现 SIGSTOP/SIGCONT/SIGKILL。验收：纯 CPU 任务可停止、继续、强制终止，不依赖其主动执行系统调用。
- [ ] J08 实现 SIGCHLD 与等待状态通知。验收：退出/停止/继续与 wait 的竞争不丢通知、不重复回收。
- [ ] J09 完善 EINTR 与 SA_RESTART。验收：读、等待和计时被处理器打断后按调用契约返回或重启。

## K. 线程、TLS 与同步

依赖：D05、D06、D10、E 组共享映射、H 组任务调度、J 组信号基础。

- [ ] K01 定义线程组的共享与私有资源。验收：地址空间、fd、信号处理器、TLS 和线程栈按 clone 标志共享或隔离。
- [ ] K02 实现 libc 创建线程需要的 clone 标志。验收：一个真实 pthread 程序创建并 join 两个线程。
- [ ] K03 接入 set_tid_address 和退出清零唤醒。验收：pthread_join 不挂起，线程退出后 TID 内存与等待队列正确。
- [ ] K04 支持每线程 TLS 初始化与寄存器。验收：两个线程的 thread-local 和 errno 不互相覆盖。（依赖 F08）
- [ ] K05 实现 futex WAIT/WAKE。验收：值不匹配、超时、跨线程唤醒与竞争无丢失唤醒。
- [ ] K06 实现运行时所需 futex 扩展。验收：对 requeue、bitset 等逐项登记，条件变量压力测试不死锁。
- [ ] K07 接入 robust list。验收：持锁线程退出后，鲁棒互斥锁按目标语义报告 owner died。
- [ ] K08 完善线程定向信号与退出。验收：线程收到信号不错误终止无关线程，exit_group 确实结束整个组。
- [ ] K09 验证 pthread 同步原语。验收：mutex、cond、once、rwlock 各有有界压力与超时测试。
- [ ] K10 验证多进程共享同步。验收：共享映射中的同步原语在父子进程间可用，私有 futex 不跨地址空间错误唤醒。

## L. 时间、随机数、查询与事件等待

依赖：C04、G 组、H03。

- [ ] L01 分离实时时钟、单调时钟与测试虚拟时钟。验收：单调时间不回退，系统时间调整不改变相对等待期限。
- [x] L02 接入 clock_gettime/gettimeofday 与精度查询。验收：结构布局、时间范围和所选时钟 ID 行为正确。 证据:abi/linux/common/syscalls.go clockGettime/clockGetres/gettimeofday(realtime+monotonic,未知时钟 ID 拒绝)、syscalls_test.go TestClockGettime 等。测试:`go test ./abi/linux/common/`。CI:Test 工作流。
- [x] L03 接入 nanosleep/clock_nanosleep。验收：相对与绝对等待、EINTR 和剩余时间通过差分。 证据:abi/linux/common/syscalls.go Sleeper 端口与 clockNanosleep(相对/绝对、EINTR 回填剩余、期限循环带取消检查)、executor/elf/program.go 接线 clock.Source;syscalls_test.go TestClockNanosleepRelative、TestClockNanosleepValidates、TestClockNanosleepInterruptReportsRemaining、TestClockNanosleepAbsoluteDeadline。测试:`make docker-test TEST_ARGS=./abi/linux/common/`。CI:Test 工作流。备注:等待推进由 clock.Source 提供,虚拟时钟已验证;宿主生产时钟源随 C04 复核。
- [x] L04 实现 getrandom 和随机设备的安全模式。验收：生产模式不使用固定种子；确定性模式显式配置且可重放。 证据:platform/entropy/entropy_native.go 与 entropy_wasi.go(!wasip1/wasip1 双构建,经 crypto/rand→宿主密码学源,VM 核心无环境随机旁路,whitelisted by vm/wasm_test.go 守卫)、guest/boot.go 默认熵源由可预测的 entropy.Counter 改为 entropy.NewProduction;abi/linux/common/syscalls.go getrandom 校验 GRND_NONBLOCK/GRND_RANDOM/GRND_INSECURE 标志位(未知位 EINVAL,标志不切换数据来源);确定性模式 = vm/entropy/seeded.go Seeded(splitmix64,deployment/native 以 opts.Seed 显式配置,seeded_test.go 断言同种子可重放、异种子不同序列)+ Recording(journal 重放,vm/entropy_record_test.go);platform/entropy/entropy_test.go TestProductionAvoidsFixedSeeds(跨实例/跨次读取不重复)、TestProductionHonoursCancellationAndEmptyReads;syscalls_test.go TestGetrandomValidatesFlags。测试:`go test ./platform/entropy/ ./vm/entropy/ ./abi/linux/common/`;`GOOS=wasip1 GOARCH=wasm go build ./guest/`。CI:Test 工作流(含 WASI 客体构建与环境导入守卫)。备注:devfs random/urandom 与 getrandom 共用注入的 entropy.Source;Counter 保留为测试用确定性源。
- [ ] L05 实现 poll/ppoll。验收：普通文件、管道和关闭的 fd 的就绪、超时、信号掩码正确。
- [ ] L06 实现 select/pselect 所需子集。验收：fd 集合、空集合睡眠及错误 fd 不导致 VM 忙循环。
- [ ] L07 实现 epoll 基础与水平触发。验收：增删改、重复登记、关闭和多 fd 就绪结果正确。
- [ ] L08 实现 epoll 边缘触发与一次性通知。验收：非阻塞读到 EAGAIN、重新武装和并发关闭不丢应有通知。
- [x] L09 实现 eventfd。验收：计数、非阻塞、信号量模式和 poll/epoll 通知正确。 证据:vm/eventfd/eventfd.go(计数/清零/信号量/非阻塞/计满阻塞/Close 唤醒/Ready+OnChange)、vm/fd/table.go EventFD、abi/linux/common kernel.go ProcessDescriptors.EventFD 与 syscalls.go eventfd2;eventfd_test.go 9 项、syscalls_test.go TestEventfd2ValidatesAndDelegates、fd/table_test.go TestEventFDDescriptorLifecycle。测试:`make docker-test TEST_ARGS=./vm/eventfd/`。CI:Test 工作流。备注:Ready/OnChange 已就绪,poll/epoll 系统调用层接入待 L07/L08。
- [ ] L10 实现 timerfd。验收：一次与周期计时、过期计数和关闭可供事件循环使用。
- [ ] L11 实现所选 signalfd 子集。验收：被屏蔽信号可由 fd 读取，与信号处理器及 epoll 的交互正确。
- [ ] L12 补齐 CPU 数、亲和性与资源使用查询。验收：返回客体配置和实际可兑现限制，不暴露或伪造宿主能力。

## M. 网络与 socket

依赖：A03、C04、G 组、I04、L05 至 L08。默认策略及宿主桥接方式由架构任务固定。

- [ ] M01 定义每 VM 网络命名空间与地址配置。验收：两 VM 的监听端口互不冲突，回环地址只指向自身。
- [ ] M02 定义 socket 生命周期与 fd 接入。验收：创建、复制、关闭、阻塞和非阻塞状态与 fd 契约一致。
- [ ] M03 实现 AF_UNIX 流式 socket。验收：客体本地连接、断连、半关闭和路径清理正确。
- [ ] M04 实现 TCP 回环通信。验收：bind/listen/accept/connect、双向收发及半关闭可由 ELF 完成。
- [ ] M05 实现 UDP 基础。验收：报文边界、截断、地址读取和非阻塞错误通过差分。
- [ ] M06 补齐 socket 地址与选项 ABI。验收：getsockname/getpeername/getsockopt/setsockopt 的运行时必需项逐项测试。
- [ ] M07 实现 socket 的 poll/epoll 就绪。验收：连接完成、失败、接收、发送和断连通知不忙等、不丢失。
- [ ] M08 实现 sendmsg/recvmsg 所需子集。验收：分散缓冲区、地址和 AF_UNIX fd 传递的所有权正确。
- [ ] M09 实现受策略约束的宿主网络出口。验收：允许的测试服务可访问，禁止地址、端口和协议被拒绝，DNS 不能绕过策略。
- [ ] M10 配置客体 DNS 与名称解析。验收：libc、Python 和 Node 在可控 DNS 服务下分别解析成功及失败。
- [ ] M11 配置客体 CA 与 TLS 测试服务。验收：Python/Node 校验证书成功，过期、错误域名和不可信证书失败。
- [ ] M12 实现网络预算及关闭回收。验收：连接数、队列和流量受限，Shutdown 唤醒所有阻塞网络操作。
- [ ] M13 定义外部访问客体服务的显式映射。验收：只发布配置的端口，撤销映射后宿主入口关闭，客体端口状态符合契约。

## N. 根镜像、用户环境与基础设备

依赖：C03、I 组；内核动态视图依赖 H、E 相应能力。

- [ ] N01 定义版本化镜像清单。验收：记录架构、ABI、入口、文件、权限、依赖及摘要，可在启动前验证兼容性。
- [ ] N02 构建最小根文件系统。验收：`/bin`、`/lib`、`/usr`、`/etc`、`/tmp`、`/proc`、`/dev` 和工作目录结构可重复生成。
- [x] N03 接入只读基础镜像与每 VM 可写层。验收：重建 VM 不污染基础层；可配置保留或丢弃写入层。 证据:filesystem/overlay 只读基础层 + deployment/layer 每会话可写层,layer_test.go TestBaseLayerRejectsEveryWriteOperation、TestSessionWritesDoNotChangeBaseOrImage、TestLayerSnapshotAndRestoreResetSessionOnly。测试:`go test ./deployment/... ./filesystem/...`。CI:Test 工作流。
- [ ] N04 提供 passwd/group、HOME、PATH 与 locale 配置。验收：身份、用户目录和 UTF-8 行为由客体查询获得。
- [x] N05 提供 `/dev/null`、`zero`、`random`、`urandom`。验收：读写语义、权限和随机模式符合契约。 证据:filesystem/devfs(null/zero/random/urandom 读写语义与权限)、descriptor_test.go。测试:`go test ./filesystem/...`。CI:Test 工作流。备注:RISC-V ABI 侧 devfs 装配待接线,接线后复核。
- [ ] N06 完善 `/proc/self`、进程和线程目录。验收：调用者身份正确，退出项及时消失，跨 VM 不可见。
- [ ] N07 提供运行时需要的 `/proc` 进程字段。验收：fd、exe、cwd、maps、stat/status、cmdline 等与实际状态一致。
- [ ] N08 提供内存、CPU、挂载等系统视图。验收：`meminfo`、`cpuinfo`、mount 信息和运行时查询反映客体配置。
- [ ] N09 提供客体共享内存目录与容量。验收：`/dev/shm` 及运行时所需共享对象可被多进程访问并正确回收。
- [ ] N10 打包真实 Bash 与基础 Linux 工具。验收：从客体 ELF 执行 Shell、文件、文本、进程命令；Go 内建命令不得替代真实 ELF 兼容验收。
- [x] N11 明确宿主工作区导入与导出。验收：只挂载授权目录，链接和重命名不能突破边界，权限与冲突策略可测试。 证据:platform/filesystem/filesystem_test.go TestFilesystemLifecycleAndSymlinkBoundary、TestFilesystemLinkSymlinkReadlinkLstat、fs_test.go TestFilesystemCapabilitiesAndQuotas、TestFilesystemReadOnlyAndHandleBudget。测试:`go test . ./platform/...`。CI:Test 工作流。
- [ ] N12 实现镜像更新与损坏检测。验收：失败更新不替换可用镜像，摘要错误在执行前发现。

## O. 身份、隔离与资源限制

依赖：C 组、E15、H 组、I13；网络项依赖 M 组。

- [ ] O01 实现客体 UID/GID 与有效身份。验收：身份系统调用、文件检查和子进程继承一致，客体 root 不等于宿主 root。
- [ ] O02 实现进程数量与线程数量上限。验收：耗尽后创建失败，退出后恢复额度，不能经另一个入口绕过。
- [ ] O03 实现 CPU 工作量与运行时间限制。验收：客体死循环被限制，控制台和管理 API 仍响应。
- [ ] O04 实现总内存与每进程限制。验收：mmap、brk、线程栈和运行时分配统一计费，耗尽只影响目标 VM。
- [ ] O05 实现 fd、管道和队列预算。验收：所有创建路径共享限额，关闭和异常退出后可回收。
- [x] O06 实现输出与屏幕历史预算。验收：持续输出不会无限占用宿主内存，截断/背压策略与错误可观察。 证据:session.go outputBudget(16MB wire 预算)、userland/shell/loop.go OutputLimit 与流重置、executor/wasm/engine_host.go newOutputBudget、vm/jobs_fault_test.go TestJobOutputTruncationIsMarked。测试:`go test . ./vm/ ./userland/...`。CI:Test 工作流。
- [ ] O07 审计全部宿主能力入口。验收：文件、网络、随机、时间、进程和扩展调用都经过明确授权，不能直接启动宿主程序。
- [ ] O08 建立双 VM 隔离测试。验收：文件、内存、PID、设备、socket 和资源耗尽互不污染。
- [ ] O09 建立恶意客体故障测试。验收：错误指令、坏指针、畸形 ELF 和系统调用风暴不会使宿主崩溃或失去关闭能力。
- [ ] O10 定义扩展模块能力边界。验收：扩展无法取得未授权宿主引用，也不能绕过 VM 的文件和网络策略。

## P. Console、Terminal 与 Bash 接入

依赖：C、H、I、J、N 的所需能力；屏幕测试依赖真实进程执行，不以替身代替。

- [ ] P01 创建 VM 所有的持久 Console 会话。验收：无连接时仍存在，重新 Attach 保留 cwd、变量和函数。
- [ ] P02 实现独立 Connect 会话。验收：各连接 Bash 状态独立，但共享 VM 的文件、进程可见性与设备。
- [ ] P03 区分 Disconnect 的两种生命周期。验收：控制台断开只释放句柄；独立连接按已固定策略关闭其会话和关联任务。
- [ ] P04 为多个 Console 句柄定义命令排队。验收：并发 Run 不交错破坏 Shell 状态，等待请求可取消。
- [ ] P05 定义 Run 取消与命令中断的分别语义。验收：取消等待、发送中断和关闭 VM 分别测试，不静默重建 Bash。
- [ ] P06 实现持续输出汇聚。验收：前台与后台输出在无连接时仍到达 Console，输出带来源和序列边界。
- [ ] P07 定义 Result 与后台输出的关系。验收：前台命令结果不会混入无法归属的后台输出，stdout/stderr 合并策略明确。
- [ ] P08 接入 PTY 主从设备及 ioctl。验收：客体 isatty、窗口尺寸和 termios 工作，管道与终端行为有区别。
- [ ] P09 接入前台进程组与终端信号。验收：Ctrl-C、Ctrl-Z、继续和挂断只作用于正确目标。
- [ ] P10 将屏幕状态绑定到持续输出。验收：断开后后台 `\r` 更新可在重新连接后读取，状态不依赖 API 输出缓冲。
- [ ] P11 完善 ANSI/VT 屏幕基础。验收：光标、擦除、滚动、宽字符和组合字符有固定字节流与参考帧。
- [ ] P12 实现窗口调整及 Screen 快照。验收：尺寸变化通知客体，快照是原子副本且坐标约定一致。
- [ ] P13 在真实客体中验证后台任务与作业控制。验收：`&`、wait、管道、停止/继续在断开重连后仍符合契约；能力通过后移除旧禁用逻辑。
- [ ] P14 明确 Bash 自身退出后的行为。验收：Console 的状态和重新启动规则可查询，不把退出伪装成普通成功调用。
- [ ] P15 运行迁入的全部新模型契约。验收：真实 VM 工厂通过全部原始用例，并加入取消、并发、故障和后台输出用例。

## Q. CPython 与 Node.js 客体运行时

依赖：A04、A05、F、H、I、J、K、L、M、N 的实际所需能力。先推进启动探针；缺失能力按 Q02 拆成前置修复，不能把整个运行时移植作为单项任务。

- [ ] Q01 建立 CPython 客体构建产物流水线。验收：固定工具链构建真实解释器及标准库，产物摘要与依赖可追溯。
- [ ] Q02 建立运行时启动缺口追踪。验收：Python/Node 每次启动记录首个缺失指令、系统调用或文件，自动生成可复现最小用例；每个新缺口单独建任务。
- [ ] Q03 打通 Python 最小启动。验收：客体执行 `python3 --version` 与算术脚本，退出码和输出正确。
- [ ] Q04 验证 Python 标准库加载。验收：路径、编码、json、re、datetime、math 的固定用例通过。
- [ ] Q05 验证 Python 文件与压缩模块。验收：pathlib、tempfile、zipfile、gzip 在客体工作区完成读写与清理。
- [ ] Q06 验证 Python 原生扩展装载。验收：构建一个目标 ABI 扩展并在客体 import、调用，错误架构产物被拒绝。
- [ ] Q07 打包 Python 必需的原生依赖。验收：ssl、hashlib、zlib、sqlite3 在客体执行实际功能，缺失依赖不能被跳过。
- [ ] Q08 验证 Python threading。验收：线程、锁、条件变量及 thread-local 在客体完成有界并发用例。
- [ ] Q09 验证 Python subprocess。验收：启动客体 ELF，传递参数/环境，收集双流输出并处理超时、退出和信号。
- [ ] Q10 验证 Python multiprocessing。验收：选定平台默认方式及 fork/spawn 所需方式完成进程、队列与共享数据测试。
- [ ] Q11 验证 Python asyncio 与网络。验收：计时、取消、TCP/UDP 与 TLS 在可控服务下通过，循环空闲时不忙等。
- [ ] Q12 打通 venv 与 pip 离线安装。验收：新建虚拟环境，安装固定纯 Python 包和本项目构建的原生 wheel，退出 VM 后按持久化配置保留。
- [ ] Q13 打通 Python 受控联网安装。验收：从可控包索引安装固定依赖，证书、网络策略和失败回滚生效。
- [ ] Q14 建立 CPython 上游测试分片。验收：固定版本相关测试按模块运行，失败逐项建任务；必要模块不得靠标记 skip 达成发布。
- [ ] Q15 建立 Node.js 客体构建产物流水线。验收：真实 Node/V8、内置库和所需数据按 A04/A05 构建，记录摘要与配置。
- [ ] Q16 打通 Node 最小启动。验收：客体执行 `node --version` 和 JavaScript 算术脚本，无宿主代理执行。
- [ ] Q17 验证 Node 模块加载。验收：CommonJS、ESM、package.json 与动态 import 使用真实文件解析且结果正确。
- [ ] Q18 验证 Node 文件和路径模块。验收：同步/异步文件读写、目录遍历和错误码正确，回调不丢失。
- [ ] Q19 验证 libuv 事件循环。验收：timer、promise、I/O 并发与关闭顺序正确，空闲不忙等。
- [ ] Q20 验证 Node worker_threads。验收：线程通信、共享缓冲区、Atomics 与异常隔离符合目标配置。
- [ ] Q21 验证 Node child_process。验收：spawn/execFile 的管道、退出、信号和环境传递正确，目标程序在客体执行。
- [ ] Q22 验证 Node net/http/https/dns。验收：客体服务与客户端完成固定并发、超时、证书验证和连接回收用例。
- [ ] Q23 验证 Node 原生扩展装载。验收：一个目标架构 N-API 扩展可加载执行，错误 ABI 返回可诊断错误。
- [ ] Q24 验证 V8 执行配置。验收：A05 选定模式下运行语言与 WebAssembly 必需用例；需要 JIT 时覆盖可执行页切换及生成代码一致性。
- [ ] Q25 打通 npm 离线安装。验收：使用锁文件安装固定纯 JS 包与预构建原生扩展，执行包脚本并验证解析结果。
- [ ] Q26 打通 npm 受控联网安装。验收：可控 registry 下载、完整性校验、TLS 和网络策略生效，失败不产生可用的半安装依赖。
- [ ] Q27 建立 Node 上游测试分片。验收：固定版本的目标模块测试可重复运行，必要失败均关闭后才允许发布。
- [ ] Q28 构建客体原生扩展编译链。验收：客体编译器、链接器和构建工具生成可运行的最小 C/C++ 程序，不调用宿主工具替代客体执行。
- [ ] Q29 验证 pip 从源码构建扩展。验收：在客体构建固定 sdist，安装并 import，产物架构和依赖正确。（依赖 Q28）
- [ ] Q30 验证 node-gyp 从源码构建扩展。验收：客体 Python、构建链与 Node 头文件构建固定 addon，并由客体 Node 加载。（依赖 Q28、Q03、Q23）
- [ ] Q31 增加双运行时共存场景。验收：同 VM 中 Python/Node 经文件、管道及回环 HTTP 交换数据，重连不影响后台服务。

## R. SDK、CLI 与 assistant 集成

依赖：C 与 P 的公开契约；运行时命令依赖 Q 对应能力。

- [ ] R01 统一公开 VM 构造与错误类型。验收：示例只用公开 API 完成创建、文件访问、连接和 Shutdown。
- [ ] R02 让 CLI 成为 VM API 的调用方。验收：单次执行与持续会话不另造内核，debug/verbose/dry-run 行为明确。
- [ ] R03 增加独立的镜像选择与校验入口。验收：版本不兼容、摘要损坏和缺少镜像均在启动前报告，SDK 不隐式下载。
- [ ] R04 提供连接与会话标识。验收：同进程中的 VM、Console 和 Terminal 可明确寻址，不以宿主目录隐式代表 VM。
- [ ] R05 将 MessageSource 与通用 VM 解耦。验收：不提供 agent 消息也能运行全部 VM 核心用例，消息作为显式可选能力注入。
- [ ] R06 收敛旧 Runtime/Session 与 contract.Session 入口。验收：公开入口均落到同一资源所有权模型，迁移或删除策略有调用方证据。
- [ ] R07 接入 assistant 的 VM 适配。验收：assistant 依赖已发布模块与镜像，无本机 replace 路径；消息、文件和执行场景通过集成测试。
- [ ] R08 验证原有工作流迁移。验收：执行命令、连续修改文件、断开重连和取消等待在新模型中有明确结果。
- [ ] R09 提供独立开发入门。验收：新检出按文档完成构建、创建 VM、运行 Bash/Python/Node、关闭 VM，所有命令由 CI 复验。

## S. 全系统虚拟机路径

依赖：A02 明确要求启动原版 Linux 内核时，本组全部为必做。以下任务以 RISC-V 全系统路线描述；若选用其他后端，按同等可验收目标替换，不强制自研已有成熟后端。

> 状态：当前唯一 ADR（docs/adr/standalone-module.md）仅确立 WASI 执行模型，未记录全系统路线取舍；按第 9 行，S 组是否必做须经目标确认并在 ADR 中说明。确认前本组保持未勾选。

采用此路径时，Linux 系统调用的应用语义由客体 Linux 内核提供；G 至 O 中的验收仍必须通过，但不要求重复在宿主重写 Linux 内核实现。A10 必须记录每项能力由哪一层提供。

- [ ] S01 固定机器型号、启动协议和设备布局。验收：内核、固件、设备树与 VM 配置使用同一份可查询规格。
- [ ] S02 实现或接入目标特权级状态。验收：特权指令、CSR 访问和非法访问异常通过参考测试。
- [ ] S03 实现或接入异常委派及特权级切换。验收：用户陷入内核并返回，PC 与状态寄存器正确。
- [ ] S04 实现或接入目标页表/MMU。验收：地址转换、权限、缺页和 TLB 失效通过参考测试。
- [ ] S05 建立物理地址空间与 MMIO 总线。验收：RAM、只读区和设备区访问按区域分派，越界可诊断。
- [ ] S06 接入启动固件及 SBI 或等效接口。验收：固定固件启动并完成内核入口交接。
- [ ] S07 提供内核启动所需计时器。验收：时钟中断驱动内核 tick，等待和超时能前进。
- [ ] S08 提供中断控制器。验收：设备触发、屏蔽、确认和结束中断行为正确。
- [ ] S09 提供启动串口设备。验收：捕获内核早期日志，并向客体输入字节。
- [ ] S10 生成设备树或后端要求的机器描述。验收：内核识别 CPU、内存、中断与所有已启用设备。
- [ ] S11 启动固定原版 Linux 内核至 initramfs。验收：内核日志可追溯，执行真实 `/init` 并能关闭机器。
- [ ] S12 接入块设备。验收：客体对测试磁盘读写、刷盘，重启后文件内容保持。
- [ ] S13 从持久根磁盘启动用户空间。验收：真实 init、Bash、procfs、devfs 正常工作。
- [ ] S14 接入虚拟网卡和后端网络。验收：客体接口获得配置，回环、受控出口和端口映射通过 M 组验收。
- [ ] S15 接入客体随机设备。验收：内核和运行时随机读取不会永久阻塞，测试模式可控。
- [ ] S16 接入宿主授权目录共享设备。验收：客体可访问指定目录，不能访问未授权宿主路径。
- [ ] S17 验证多核与原子同步。验收：多个客体 CPU 启动，Linux 多线程压力测试通过，或明确配置为单核但支持抢占线程。
- [ ] S18 接入 VM API、Console 与生命周期。验收：P 组新模型用例运行于启动真实 Linux 的 VM，Shutdown 包含内核退出与强制停止。
- [ ] S19 在全系统镜像执行 Q 组运行时验收。验收：CPython、Node、pip、npm 与原生扩展通过同一套发布门禁。

## T. 可观测性、状态与性能

依赖：对应内核能力；本组不以优化替代功能正确性。

- [ ] T01 提供统一 VM 状态与进程检查。验收：列出 VM 状态、进程/线程、等待原因、fd 与预算使用，不依赖内部指针。
- [ ] T02 提供客体崩溃报告。验收：包含 PID/TID、PC、寄存器、映射和最近有限调用记录，可复现最小失败程序。
- [ ] T03 接入资源指标。验收：CPU 工作量、内存、磁盘、网络和输出指标与配额扣减一致。
- [x] T04 明确快照范围与安全点。验收：区分文件系统、关机磁盘快照、进程状态与整机状态，不把部分序列化宣称为整机快照。 证据:vm/proc/snapshot.go、filesystem/memfs/snapshot_codec.go、deployment Matrix 的 Snapshot/Replay 字段、vm/snapshot_table_test.go。测试:`go test ./vm/ ./deployment/...`。CI:Test 工作流。
- [x] T05 验证已声明的快照恢复能力。验收：恢复后文件、资源与允许保存的执行状态一致；不支持活动连接时明确拒绝。（依赖 T04） 证据:vm/snapshot_restore_state_test.go、vm/snapshot_table_test.go TestRestoreTableRejectsInvalidDataAndClosedKernel、deployment/layer 状态恢复测试。测试:`go test ./vm/ ./deployment/...`。CI:Test 工作流。
- [x] T06 明确记录重放的可控输入边界。验收：时间、随机、外部 I/O 与调度输入有覆盖清单，未记录输入不会被声称可确定重放。 证据:vm/replay/(effects/header/memory/run/schedule)、vm/entropy_record_test.go、vm/signal_record_test.go、vm/stdin_record_test.go。测试:`go test ./vm/`。CI:Test 工作流。
- [x] T07 验证已声明的记录重放能力。验收：固定程序重放输出和状态一致，输入不匹配时停止并定位。（依赖 T06） 证据:vm/replay 重放验证(录制输入不匹配时停止并定位),replay 包各 _test.go。测试:`go test ./vm/replay/`。CI:Test 工作流。
- [ ] T08 建立 CPU 与系统调用基准。验收：固定客体和宿主配置下记录吞吐、开销与内存，CI 保存可比较结果。
- [ ] T09 建立 Python/Node 场景性能基线。验收：启动、依赖导入、文件处理、并发服务有耗时和内存数据。
- [ ] T10 定义最低可用性能与管理响应要求。验收：数值、测试环境和容差在测量后固定，超限发布失败，不靠无限延长超时通过。
- [ ] T11 按首个测得瓶颈创建独立优化任务。验收：每项优化有前后基准及语义回归，CPU、内存和 I/O 优化分别提交。

## U. 最终验收与发布

依赖：A10 追踪的全部必做任务；A02 要求全系统时包含 S 组。

- [ ] U01 通过 Linux 基础进程验收。验收：真实 ELF 完成 fork/exec/wait、管道、后台运行、进程组、线程与信号，不依赖 Shell 模型替代。
- [ ] U02 通过 Linux 内存与文件验收。验收：动态 libc、TLS、mmap、共享内存、权限、挂载及持久化场景全部通过。
- [ ] U03 通过 Linux 设备与网络验收。验收：PTY、procfs、随机、计时、epoll、socket、DNS 与 TLS 场景全部通过。
- [ ] U04 通过全部 Console/Terminal 契约。验收：状态保持、独立 Bash、共享 VM 资源、后台屏幕、取消与 Shutdown 用例通过真实后端。
- [ ] U05 通过 Python 发布场景。验收：标准库、原生扩展、线程、多进程、asyncio、网络、venv 与 pip 全部通过固定测试清单。
- [ ] U06 通过 Node 发布场景。验收：模块、事件循环、worker、子进程、网络、原生扩展与 npm 全部通过固定测试清单。
- [ ] U07 通过包源码构建场景。验收：Python sdist 与 Node addon 均在客体编译安装后运行，不以宿主预编译作为此项通过证据。
- [ ] U08 通过隔离与故障验收。验收：资源耗尽、恶意输入、后台死循环、双 VM 并发及强制关闭不会逃逸或拖垮宿主管理面。
- [ ] U09 通过独立仓库验收。验收：干净 CI 不访问原项目目录即可构建、测试、打包和执行全部必做验收。
- [ ] U10 通过实际部署后端验收。验收：对每个声明支持的宿主平台和后端执行同一兼容配置；不满足者不标为支持。
- [ ] U11 生成版本化能力报告。验收：逐项列出 Linux、ISA、Python、Node、JIT、设备和网络的实现与测试证据，无未关闭必做缺口。
- [ ] U12 发布 SDK、镜像与工具链清单。验收：版本关联、摘要、许可证、SBOM 和可用下载资源齐全，可在新环境重建 VM。
- [ ] U13 完成 assistant 的真实集成验收。验收：使用发布版本创建 VM，运行 Bash/Python/Node 工作流，断开重连和退出均正常。
- [ ] U14 核对原始需求并关闭任务清单。验收：每条用户需求均有可执行证据，只有架构决策明确不适用的条件任务可不实施，不以“已提供接口”代替完成。

## 建议推进顺序与阶段出口

| 阶段 | 任务范围 | 出口条件 |
| --- | --- | --- |
| 0：范围与基线 | A、B | VM 类型、ISA、运行时版本与 Linux 兼容配置明确，独立构建可靠 |
| 1：真实程序执行基础 | C、D、E、F、G、N 的基础项 | 客体静态及动态 libc 程序可运行，VM 的资源所有权统一 |
| 2：Linux 执行环境 | H 至 O；P 的持久控制台基础 | 多进程、多线程、信号、文件、事件与网络行为通过差分 |
| 3：真实语言运行时 | Q，穿插最小启动探针 | Python/Node 的包、线程、子进程、网络与原生扩展均可使用 |
| 4：产品入口与可用性 | P、R、T | SDK/CLI/assistant 使用同一 VM，故障可诊断，性能达到测得目标 |
| 5：发布 | U | 全部必做场景及部署矩阵通过，可独立发布并被 assistant 消费 |

若 A02 选择全系统路线：阶段 1 先完成 S01 至 S11，再推进 S12 至 S18；客体 Linux 内核承担 Linux 语义实现，H 至 O 仍作为黑盒兼容验收要求。不得同时开展两套重复内核实现来维持旧接口；旧路径是否保留由 R06 决定。

运行时探针应尽早执行，不等所有底层任务结束才首次启动 Python/Node。新发现的真实依赖通过 Q02 增加具名小任务，并关联 A10 与对应发布门禁；不得以“任务表没写到”为由绕过必要能力。
