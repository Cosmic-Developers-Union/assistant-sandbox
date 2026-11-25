# Node JIT 与可执行内存策略(A05)

状态:已采纳(2026-10-24)。依据 [VM 架构决策](vm-architecture.md)(A02)
与 [构建配置](../runtime/toolchain.md)(A04)。

## 决策

1. **阶段一(当前):V8 jitless。** Node 按 `--v8-jitless` 配置构建与运行:
   不生成机器码,无可执行数据页,VM 侧不开放任何 RWX 映射。
2. **最终配置:受控 JIT。** V8 默认(启用 TurboFan/Maglev)在客体可用,
   前提是 VM 实现下列 W^X 转换契约;Q24 验收时切换并覆盖。
3. **W^X 转换规则(VM 侧不变式)**:
   - 任何页在任意时刻**不得同时可写且可执行**;mprotect 请求
     PROT_WRITE|PROT_EXEC 一律拒绝(E06 现行语义)。
   - 代码页生命周期:`mmap(PROT_READ|PROT_WRITE, 匿名私有)` 写入 →
     `mprotect(PROT_READ|PROT_EXEC)` 执行 → 需要回写时
     `mprotect(PROT_READ|PROT_WRITE)` 再改回;每次转换经地址空间
     权限检查,可执行页计入映射/物理页预算(E15)。
   - 取指权限独立于数据访问(E02):写模式下的代码页取指即陷入,
     与 Linux 行为一致。
4. **Python 侧**:CPython 3.14 默认解释器(实验性 JIT 保持关闭),
   不产生运行时代码;`mprotect` 契约与 Node 共用。

## 阶段限制(明确记录,不得视作全部 V8 能力)

- jitless 下 Sparkplug/TurboFan/Maglev 均不运行,JS 执行性能显著低于
  默认配置;Q24 的性能指标在切换最终配置后重新测量(T09/T10)。
- **WebAssembly 在 jitless 模式不可用**;WASM 场景属于最终配置的
  验收范围(Q24),阶段一不得宣称支持。
- 内建函数经 C++ 直调路径执行,行为面与默认配置存在已知微小差异,
  差分以最终配置为准。

## 验收挂钩

- Q24:两种模式各跑语言与 WASM 必需用例;jitless 模式单独记录
  "不含 JIT 能力"的能力报告条目(U11)。
- E06/E15:mprotect 的 W↔X 转换与可执行页记账先于 Q24 落地并在
  内存表测试中锁定;文件映射(E08/E09)落地后 JIT 页仍限匿名私有。

## 宿主翻译缓存补充(2026-11-25)

执行器的 x86-64 页级翻译缓存属于**宿主私有实现**,不映射到客体
地址空间,也不改变上文的客体 W^X 契约:

- 翻译器以 `mmap(PROT_READ|PROT_WRITE, MAP_PRIVATE|MAP_ANONYMOUS)`
  分配宿主缓存,写入机器码后 `mprotect(PROT_READ|PROT_EXEC)`;
  缓存从不以 RWX 权限存在。
- 客体页从不因翻译而获得执行权限;客体 `mprotect` 仍拒绝
  `PROT_WRITE|PROT_EXEC`,执行权限检查仍由地址空间表执行。
- 翻译缓存按客体代码页版本失效,不缓存文件共享后备页;翻译失败、
  特殊浮点值、原子/CSR/系统调用等回退到解释器语义基准。
- 该缓存不计入客体物理页预算,但受固定条数与代码体积上限约束。

## 标准 V8 JIT 实验开关

标准 Node 的 V8 JIT 会在启动阶段请求 guest
`mprotect(PROT_READ|PROT_WRITE|PROT_EXEC)`。默认 VM 仍按本 ADR
拒绝该请求。为进行标准 Node 性能对比，CLI 提供显式实验开关：

```sh
assistant-sandbox --allow-guest-rwx -c 'node app.js'
```

该模式只允许 guest 逻辑页同时具备 W/X；宿主 JIT 代码缓存仍严格
保持 RW→RX，不产生宿主 RWX 映射。它会降低 guest 代码页的 W^X
隔离保证，不得作为默认生产配置。

该开关会让 `/usr/bin/node` 包装器切换到标准 V8；不带开关时仍使用
`--jitless`。直接执行 `/usr/bin/node-bin` 始终是标准 V8。
