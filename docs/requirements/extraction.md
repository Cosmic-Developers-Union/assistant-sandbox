# 独立 sandbox 项目

## 来源

2026-10-16 用户要求将 sandbox 从内部 assistant 项目拆出，目标仓库为
https://github.com/Cosmic-Developers-Union/assistant-sandbox ，本地工作目录为
`../assistant-sandbox`。保留 golangci-lint，将构建和测试迁至 GitHub。

## 验收

独立 Go 模块不引用 assistant 内部包，不携带主仓库历史、部署配置或密钥。
生成的 WASM 不入 Git。GitHub Actions 构建和测试独立项目，标签发布上传
客体、CLI 和校验和。内部项目的消息模型由宿主转换，SDK 接收通用消息快照。

项目原创代码采用 GNU AGPL v3，完整许可见 [LICENSE](../../LICENSE)。主项目依赖已发布版本，不以本机临时路径作为发布依赖。
