# POSIX.1-2016 兼容测试环境

被测对象是本项目构建的 `assistant-bash.wasm`，由 SDK 的 wazero 执行。
控制进程可以使用 Git、Perl、Go 或 TET；被测命令全部在 WASI sandbox 内执行，
没有宿主命令回退。此目录不改变产品实现。

## 快速开始

在仓库根目录执行：

```sh
make -C test/posix doctor    # 检查宿主控制工具与官方套件缺口
make -C test/posix sources   # 下载 upstream.json 中固定 commit 的四个项目
make -C test/posix upstream  # 执行上游子集，失败仍完成报告
make -C test/posix check     # 执行 36 个自建 POSIX.1-2016 开发样本
make -C test/posix test      # 验证测试适配器本身
```

上游测试需要 Go、Git、Perl，不需要构建 Rust 产品或 GNU Coreutils。
源码位于 `.local/upstream/`，运行前校验 commit 和工作树；已有目录不会被自动重置。
下载中断后可删除对应的未完成源码目录再执行 sources。更新版本时修改 `upstream.json`，
重新检查驱动契约，不能直接换成 latest。

只运行某一组测试：

```sh
make -C test/posix upstream FILTER=coreutils/basename/
make -C test/posix upstream FILTER=posixutils/word_expansion/
make -C test/posix upstream FILTER=sed/
```

完整报告位于 `.generated/upstream/report.md` / `report.json`；开发样本报告位于
`.generated/current/`。每次运行覆盖对应报告，并记录时间、来源版本、筛选条件、
脚本、实际与期望 stdout/stderr、退出码、运行时错误。非 UTF-8 原始数据以 JSON
`raw_bytes` 的 base64 保存，文本预览不会替代原始证据。

`upstream-gate` 和 `gate` 用于 CI：存在 FAIL 或 BLOCKED 时非零退出。
普通 upstream/check 便于完整收集缺口，不以进程退出 0 表示产品兼容。
`make clean` 只删除 `.generated/`，保留下载源码、TET 和授权套件。
所有下载内容和生成结果均被 gitignore 排除。

## 已接入的上游契约

| 来源 | 实际复用内容 | 尚未接入内容 |
|---|---|---|
| [GNU Coreutils](https://github.com/coreutils/coreutils) | basename/dirname 的 Perl vector；原脚本计算参数和期望输出，适配器只替换 run_tests 调度 | 其余命令、gnulib shell 框架、GNU 专属非零退出诊断 |
| [GoAWK](https://github.com/benhoyt/goawk) | testdata/gawk 的 .awk/.in/.ok golden 文件，复用原驱动的四项排除和四项无序输出处理 | Go 内部单测、解释器专属诊断、完整原始 AWK 驱动 |
| [uutils/sed](https://github.com/uutils/sed) | check_output! 的字面量参数和输入/输出 fixture | 其他 Rust 测试函数、check_output_posix!、GNU sed shell 驱动、无法解析的 Rust 参数表达式 |
| [posixutils-rs](https://github.com/rustcoreutils/posixutils-rs) | sh/tests/sh 的 .sh/.out，核实 integration.rs 中 test_script 成功脚本契约 | PTY、权限、特殊 checker、环境 fixture，以及 Rust 内部/系统接口测试 |

这里接入的是这些项目的**明确子集**，不是执行它们完整的 cargo/go/make 测试。
不支持的驱动契约标为 BLOCKED；已适配但目标执行不满足期望标为 FAIL，
例如缺少 awk/sed 返回 127。GNU 扩展测试不等于 POSIX 必选项。
新命令分支可以复用固定的测试数据和 `loadUpstream` 驱动，再扩展该命令的适配。

2026-10-05 固定版本的首次未筛选基线为 516 条：PASS 86、FAIL 397、BLOCKED 33。
自建开发样本为 PASS 28、FAIL 8：缺少 sh/awk/sed/tr/env，以及 grep BRE、tail +N、
cut 字段范围三处差异。以实际报告为准，不把这些数字当作 POSIX 完成率。

## 官方认证目标与 TET

`target.json` 固定目标 POSIX.1-2016，VSC-PCTS2016 3.1（shell/utilities）与
VSX-PCTS2016 1.15（system interfaces），版本依据
[The Open Group 套件清单](https://posix.opengroup.org/testsuites.html)。
目前没有授权套件，因此两者都标为**未运行**。
开源兼容回归不能替代 VSC/VSX 或正式认证。

可选安装公开的 TET3.8-Lite 控制框架并检查实际目标接线：

```sh
make -C test/posix setup
make -C test/posix tet-smoke
```

安装脚本固定公开源码 SHA-256；上游旧式 C 使用 GNU89 编译控制器，
并检查实际 tcc/tcm.sh 产物。下载来源和许可见
[TET3 下载页](https://tetworks.opengroup.org/tetdownloads/)。
自建 smoke 通过真正的 TET 调用 sandbox CLI，得到两条 TP 结果；
它是接线检查，不是官方套件。不要把宿主 Linux 的测试结果标成 assistant-wasi。

获得授权后，把套件放到 `.local/suites/VSC-PCTS2016` / `.local/suites/VSX-PCTS2016`，
按随包文档确认平台、编译器、运行入口和必选测试计划。当前还没有官方 VSC 目标适配；
VSX 更缺少面向 assistant-wasi 的 POSIX C ABI/系统接口执行器，不能直接用宿主
gcc 或 cargo 测试来填充 VSX 通过数。公开控制器也不保证替代套件要求的指定 TETware。
取得有效 journal 后可单独导入：

```sh
make -C test/posix journal JOURNAL=/path/to/journal \
  SUITE=VSC-PCTS2016 SUITE_VERSION=3.1 TARGET=真实被测目标标识
```

导入要求明确来源元数据，保留原始结果码、标签、测试路径、activity/TP 与全部状态，
检查 TCC 起止记录、重复 TP、结果码标签矛盾和截断日志。
导入者填写的元数据不能自动证明真实目标、完整测试计划或认证资格。
FAIL/UNRESOLVED/UNTESTED/UNINITIATED/NORESULT 等都不会折算为 PASS；
完整官方认证需按[认证指南](https://posix.opengroup.org/certification_guide.html)审查。

`utility-index.json` 来自 POSIX.1-2016 utilities 索引，用于显示入口缺口。
其中包含可选/XSI 项，入口存在也不代表参数与语义完整，不能作为必选项的通过率分母。
