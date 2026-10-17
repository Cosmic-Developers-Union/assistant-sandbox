# 独立源码模块与客体发布

## 决策

SDK 位于模块根目录，客体、VM、用户命令、构建器及 POSIX 开发测试同仓维护。
原主项目的 agent 发布流程集成测试留在主项目；独立模块通过
`Config.MessageSource` 接收消息，不导入内部 agent 模型。

`Config.ShellArchive` 显式接收客体。生产代码不使用 `go:embed` 引用生成的 WASM，
使 Go 模块在未运行生成器时仍可编译；生成器只为测试和发布提供客体。
版本选择和加载由宿主负责。发布资源放 GitHub Release，不提交至 Git。

保持 WASI 执行模型；本次拆分不增加 bwrap 后端或动态编译行为。
实现依据为 [Config 与构造器](../../wasm.go)、[消息接口](../../messages.go)、
[构建器](../../internal/build/build.go)和[发布工作流](../../.github/workflows/release.yml)。
