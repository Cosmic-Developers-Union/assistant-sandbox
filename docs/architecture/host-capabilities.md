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
| 宿主进程 | `internal/build`、`internal/diff`、`test/posix` | 仅构建、差分参考执行与 POSIX 测试；客体执行路径不可达 |
| 扩展模块 | `vm/bridge.Bridge`、`capability.Set.Bridge` | 扩展调用必须经能力端口，不能取得宿主引用 |
| WASI 部署 | `wasm.go`、`platform/modules` | 旧部署路径；不构成新 VM 契约（R06 收敛） |

## 已审计的显式例外

- `userland/toolcompat/goawk/interp` 保留上游 `os/exec` 代码，但客体
  `awk` 入口固定 `NoExec: true`，`system()`/管道分支不可达。
- `fs_unix.go` 与 `platform/filesystem/*` 的 `syscall` 只用于文件
  属性/时间戳适配，不启动宿主进程。
- `guest/cmd` 与 `cmd/*` 是进程入口，不是客体代码。

`internal/metadata/hostcap_test.go` 对生产源码的导入与进程启动调用
做白名单校验：出现新的宿主能力入口会失败，必须先在本文登记。
