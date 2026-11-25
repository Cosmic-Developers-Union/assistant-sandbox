# 独立源码模块与客体发布

## 决策

SDK 位于模块根目录，Linux ABI VM、用户命令和运行时资产同仓维护。
原主项目的 agent 发布流程集成测试留在主项目。

SDK 的公开执行入口为 `instance` 与 `model`。WASI 客体、`Config.ShellArchive`、
`Config.MessageSource` 和独立 WASM 构建器已移除；发布只包含 CLI 与 SHA-256
清单，不提交生成物。实现依据为 [发布工作流](../../.github/workflows/release.yml)
和 [VM 架构决策](vm-architecture.md)。
