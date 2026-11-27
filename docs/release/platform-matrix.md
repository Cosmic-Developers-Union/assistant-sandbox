# 发布平台与后端验证矩阵（X13）

“交叉构建”只证明目标二进制可生成；“运行验证”表示该目标在实际 CI
宿主上执行了对应测试。默认 `--backend=auto` 只在具备完整 Linux 隔离
能力的 linux/amd64 上选择原生后端，其余目标回退到 RISC-V 后端。

| Release 目标 | 构建 | 运行验证 | 默认后端 | 显式兼容入口 |
| --- | --- | --- | --- | --- |
| linux/amd64 | 静态 CGO CLI，内嵌 amd64 rootfs | `native-amd64` 特权容器：隔离、文件、网络、cgroup、PTY、语言扩展、3× 性能与下载产物 PTY | 能力完整时 `linux/amd64`，否则 `riscv64` | `--backend=riscv64` |
| linux/arm64 | 纯 Go CLI | `integration (ubuntu-24.04-arm)` 的 RISC-V 回归 | `riscv64` | `--backend=riscv64` |
| darwin/amd64 | 纯 Go CLI | 仅交叉构建，未声明运行验证 | `riscv64` | `--backend=riscv64` |
| darwin/arm64 | 纯 Go CLI | 仅交叉构建，未声明运行验证 | `riscv64` | `--backend=riscv64` |
| windows/amd64 | 纯 Go CLI | 仅交叉构建，未声明运行验证 | `riscv64` | `--backend=riscv64` |

## 原生 CI 能力要求

`native-amd64` 使用 `testdata/release/native-ci.Dockerfile`，并以
`--privileged --cgroupns=host` 挂载可写 cgroup v2。测试设置
`ASSISTANT_REQUIRE_NATIVE=1`：namespace、bind mount、seccomp 或 cgroup
委派缺失时测试直接失败，不允许静默跳过。该任务同时运行显式 RISC-V
CLI 入口，因此同一提交具备两种后端的 CI 证据。

## 性能门禁

- Node 22.23.5 宿主/客体五对交替真实 REPL，逐轮验证结果与变量持久性；
- Python/Node 启动、整数计算、1 MiB 文件读写六个场景各五对；
- 中位数比值超过 3 返回非零，并保存原始 JSON 与 PTY 记录；
- 固定 3.03× 样本必须返回退出码 1，反向证明门禁有效。

## 发布产物复验

Release 先创建 draft，使用 `gh release download` 下载到空目录，然后：

1. 核对五个平台二进制、`SBOM.txt` 与 `SHA256SUMS`；
2. 用 `file` 核对架构，并执行 linux/amd64 `--version`；
3. 在特权容器中以 `PATH=/nonexistent` 运行下载的默认 CLI，通过真实
   PTY 的 X11 验收，不传后端参数、不依赖宿主 Node/Python；
4. 全部通过后才取消 draft。
