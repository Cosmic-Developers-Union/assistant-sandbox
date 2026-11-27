# 宿主能力审计（O07）

本仓库把宿主能力集中在明确的边界适配器中。客体内核（`vm/`）与客体
用户程序（`userland/`）不得直接访问宿主文件系统、网络、随机源、时钟
或启动宿主进程。新增宿主能力需要：

1. 在 `vm/capability` 中增加端口/能力，并由部署层注入；
2. 更新本文的授权入口表；
3. 更新 `internal/metadata/hostcap_test.go` 的白名单与测试。

## 授权入口

| 能力 | 唯一入口 | 说明 |
| --- | --- | --- |
| 宿主文件系统 | `platform/filesystem`（`os.Root`）、`filesystem/persistent` | 只通过授权根；客体路径由 `vm/fs` 与 allowlist 解析，不直接使用宿主绝对路径 |
| 宿主时间 | `vm/clock`（`clock.Real`） | 客体可见时间只经时钟端口；`time` 包仅用于测量/展示 |
| 宿主随机 | `platform/entropy`（`crypto/rand`） | 生产熵源；确定性模式用 `vm/entropy.Seeded`，无环境旁路 |
| 宿主网络 | `vm/net.Network`、`platform/network`，默认 `Denied` | M09 出口策略在 vm/net 逐地址/端口/协议裁决；未配置时非回环目标 ENETUNREACH；guest socket 回环仍在 VM 命名空间内 |
| 宿主进程 | `internal/diff` | 仅用于差分参考执行；客体执行路径不可达 |
| 原生后端控制面 | `instance/native_probe_linux.go` | 仅在 linux/amd64 探测 user/mount/PID/net/IPC/UTS/cgroup namespace；子进程只执行 `/bin/true`，不读取宿主文件或启动客体程序 |
| 原生客体根文件系统 | `guest/hostroot` | 只在 linux/amd64 嵌入固定归档；解包目标由部署层提供，路径经根边界校验，不访问宿主 PATH 或解释器 |
| CLI 终端 | `cmd/assistant-sandbox/pty.go`、`signals_*.go` | 仅把宿主 stdin/stdout 桥接到 VM PTY；使用 `term` 切换 raw 模式，并捕获终止信号恢复终端，不读取宿主文件 |
| 扩展模块 | `vm/bridge.Bridge`、`capability.Set.Bridge` | 扩展调用必须经能力端口，不能取得宿主引用 |
| 原生批量执行（受控 unsafe） | `executor/cpu/riscv64/batch.go`、`hot_cgo.go`、`vm/memory/table.go` | 仅把持锁期间的页数据/译码指令切片地址传给同进程批量解释器；调用期间锁覆盖生命周期，不读取宿主文件、网络、时钟或进程能力 |

## 已审计的显式例外

- `userland/toolcompat/goawk/interp` 保留上游 `os/exec` 代码，但客体
  `awk` 入口固定 `NoExec: true`，`system()`/管道分支不可达。
- `platform/filesystem/*` 的 `syscall` 只用于文件属性/时间戳适配，
  不启动宿主进程。
- `cmd/*` 是进程入口，不是客体代码。

`internal/metadata/hostcap_test.go` 对生产源码的导入与进程启动调用
做白名单校验：出现新的宿主能力入口会失败，必须先在本文登记。
