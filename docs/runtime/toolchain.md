# 客体运行时构建配置(A04)

状态:现行(2026-10-24 固定)。版本更换须同步本文件的摘要与支持期证据,
并走 TODO Q 组的缺口追踪。

## 目标 ABI(固定)

| 项 | 值 |
| --- | --- |
| 客体 ISA | riscv64gc(RV64IMAFDC)+ Zicsr/Zifencei,小端 |
| 数据 ABI | lp64d(双精度浮点调用约定;VM 装载器亦接受 soft/single,但运行时按 lp64d 构建) |
| 内核 ABI | Linux asm-generic riscv64 系统调用(兼容配置 [linux-rv64gc-1](../compat/linux-profile.md)) |
| libc | musl(经 Alpine 构建根,见下) |
| 二进制形态 | 静态优先(F09);动态 musl 程序(F10)以 `/lib/ld-musl-riscv64-sf1.so` 家族为目标 |

## 版本与源码摘要(固定)

| 组件 | 版本 | 源码摘要 sha256 | 支持期 | 许可证 |
| --- | --- | --- | --- | --- |
| CPython | 3.14.9 | `8b47e85ee67e93945e93620a051a8f44bfc1e9735ed67075d166f4a74b7a060b`(Python-3.14.9.tgz,python.org/ftp/python/3.14.9) | 修复期至 2027-10-01,EOL 2030-10-31(endoflife.date/api/python.json,2026-10-24 查询) | PSF-2.0 |
| Node.js | 24.22.1 | `4f5379796280cdaeb0c2f358fd04b6a16cc4fb7dfa83b5a8c91a9d963b435231`(node-v24.22.1.tar.xz;与 nodejs.org/dist/v24.22.1/SHASUMS256.txt 官方条目一致) | LTS(2025-10-28 起),维护期至 2028-04-30(endoflife.date/api/nodejs.json) | MIT(捆绑依赖各自许可,B10 汇总) |
| musl | 1.2.6(ABI 参考源码) | `d585fd3b613c66151fc3249e8ed44f77020cb5e6c1e635a616d3f9f82460512a`(musl.libc.org/releases) | 上游滚动 | MIT |
| Alpine 构建根 | 3.22.7 minirootfs(riscv64) | `50043f35362c3b7507a94814f2373948485e6d577a39f61833f2c37c0760458a`(dl-cdn.alpinelinux.org/alpine/v3.22/releases/riscv64/latest-releases.yaml,2026-10-21 发布) | Alpine 3.22 支持期内 | 各包 Alpine 许可证清单(B10) |

构建根内 musl 版本以 minirootfs 实际包含为准(摘要已随根文件系统锁定);
musl 1.2.6 独立源码作为 ABI 行为参考与差分对拍基准。

仓库内 guest 装载器 `ld-musl-riscv64.so.1` 使用该 1.2.6 源码构建,
仅附加 `fpathconf(3)` 描述符校验补丁(关闭描述符返回 `EBADF`),
产物 sha256 为
`23ea7ef0c0fb58d350b0a847c75de7529a68c240af87c4fdd4a327444801b0fd`。
补丁与可复现构建命令见
[`internal/guest/testdata/lib/README.md`](../../internal/guest/testdata/lib/README.md)。
该补丁属于 guest libc 兼容层,不改变内核 ABI;上游 musl 对已知
`pathconf` 名称不校验描述符。

## 构建策略

- **构建环境**:CI 在 Docker 中以 `qemu-riscv64-static` + Alpine riscv64
  minirootfs 原生(模拟)构建,消除交叉工具链漂移;构建容器摘要随根
  文系统摘要锁定。本机不执行重型构建(B 组纪律)。
- **CPython**:`./configure --prefix=/usr --disable-shared` 先静态;
  依赖(zlib、openssl、libffi、bzip2、xz、readline、sqlite3)取自
  Alpine 3.22 仓库固定版本,由 Q01 流水线记录逐项版本与摘要;
  `--with-ensurepip` 保留(Q12 venv/pip 需要);free-threaded 与
  实验性 JIT 均保持关闭。
- **Node.js**:按 [Node JIT 与 W^X 决策](../adr/node-jit.md)(A05),
  阶段一以 `--v8-jitless` 配置;`--without-npm` 先剥离(npm 场景
  Q25/Q26 独立装配);libuv/openssl/cares 等 bundled 依赖按官方
  tarball 内嵌版本,不拉取外部替换。
- **优化级别**:全部 `-O2`,不开 LTO/PGO(保证可重复与差分稳定;
  性能优化按 T11 单列)。

## 依赖与许可证清单职责

- 本文件固定"源头三件"(CPython/Node/构建根)的版本、摘要与支持期。
- 运行时内部依赖的逐项版本、来源与许可由 Q01/Q15 流水线产出的
  SBOM 追加,汇总进 [THIRD_PARTY.md](../../THIRD_PARTY.md)(B10)。
- 版本升级规则:仅当新版本处于适用支持期、重新计算摘要、并重跑
  受影响 Q 组验收后才可替换;不支持期版本不得进入发布。
