# 第三方来源与许可

- mvdan.cc/sh/v3 v3.14.0，BSD-3-Clause。来源：https://github.com/mvdan/sh/tree/v3.14.0 ，完整许可见 [mvdan-sh-LICENSE](licenses/mvdan-sh-LICENSE)。
- wazero v1.12.0，Apache-2.0。来源：https://github.com/tetratelabs/wazero/tree/v1.12.0 ，完整许可见 [wazero-LICENSE](licenses/wazero-LICENSE)。
- GoAWK 客体适配代码，MIT；保留的版权与许可见 [LICENSE.txt](userland/toolcompat/goawk/LICENSE.txt)。

`guest/overlay/stdin_os.go.txt` 来自 mvdan 的 `interp/stdin_js.go`，调整为 WASI
内存管道。`build/interpreter.go` 在临时依赖副本中适配解释器；模块缓存不被修改。
其他模块依赖及固定版本见 [go.mod](go.mod)和 [go.sum](go.sum)。
