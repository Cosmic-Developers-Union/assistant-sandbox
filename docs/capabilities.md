# 版本化能力报告(U11)

报告版本:2026-11-22,对应 TODO 当前 HEAD。所有“通过”项均在真实
RISC-V Linux 客体执行器中运行,不以宿主 Python/Node/gcc 代替。

| 能力域 | 状态 | 实现与验收证据 |
| --- | --- | --- |
| Linux ABI 与设备 | 通过 | asm-generic RISC-V 系统调用、PTY/termios、procfs、随机源、计时、epoll、socket、DNS、TLS;证据见 U01–U04、T01–T08 与 `go test ./abi/... ./vm/... ./instance/...` |
| ISA | 通过 | RV64IMAFDC/Zicsr/Zifencei 执行器与差分工件;证据见 D 组、`go test ./executor/cpu/... ./internal/diff/...` |
| Python | 通过 | CPython 3.12.16、标准库/线程/多进程/asyncio/venv/pip、受控 HTTPS 索引、CPython 上游分片、客体 sdist 原生扩展;证据见 Q04–Q14、Q29、U05、U07 |
| Node.js | 通过 | Node 22.23.5 jitless、模块/事件循环/worker/子进程/HTTP/TLS、N-API、npm 离线与受控 registry、上游分片、node-gyp;证据见 Q15–Q30、U06–U07 |
| JIT / 可执行页 | 阶段一通过 | A05 当前选择 jitless;已验证语言执行、Wasm 不可用和映射无 RWX。受控 JIT 不声明为已启用,按 A05 的后续阶段门禁保留。 |
| 网络 | 通过 | 受策略限制的 TCP/UDP、DNS、TLS;受控 pip/npm 下载与错误证书拒绝;证据见 M09–M11、Q13、Q22、Q26、Q31 |
| 发布与集成 | 通过 | U12 生成本地 CLI/客体摘要与 SBOM;U13 使用发布产物执行 Bash/Python/Node、断开重连与退出;R07 assistant 已发布模块和缓存镜像集成测试通过。 |

## 可复现命令

```sh
go test ./internal/guest/ -run 'TestPython312|TestNode22|TestGuestNativeToolchain' -count=1
go test ./abi/... ./vm/... ./instance/... -count=1
go test ./internal/metadata/ -count=1
```

能力报告不把 JIT 阶段一伪装成完整 V8 JIT;版本、摘要和许可信息的发布
清单见 `docs/release/manifest.md`。
