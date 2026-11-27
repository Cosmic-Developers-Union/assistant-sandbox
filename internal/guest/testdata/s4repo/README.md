# S4 外部包仓库夹具

该目录来自 Alpine v3.22 riscv64 的软件仓库索引，保留
`git-init-template-2.56.1-r0.apk` 作为无依赖包，用于验证
`apk add` 的索引读取、签名校验、包选择、解包与 `apk del` 清理。
`alpine-devel@lists.alpinelinux.org-60ac2099.rsa.pub` 是验证索引和包
签名所需的公钥；安装脚本由测试显式关闭，文件只写入 VM 根文件系统。
