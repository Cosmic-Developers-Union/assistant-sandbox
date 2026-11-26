# S4 外部包仓库夹具

该目录来自 Alpine v3.22 riscv64 的软件仓库索引，保留
`git-init-template-2.56.1-r0.apk` 作为无依赖包，用于验证
`apk add --simulate` 的索引读取、包选择和依赖求解。夹具不执行安装
脚本，也不写入客体外文件。
