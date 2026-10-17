package wasm

import "github.com/Cosmic-Developers-Union/assistant-sandbox/deployment"

func Matrix() deployment.Matrix {
	return deployment.Matrix{
		Name:   "wasm",
		Deploy: "wasip1 guest (wazero)",
		Executors: map[string]deployment.Support{

			"builtin-go": deployment.Supported,

			"wasi": deployment.Partial,

			"wasm-in-guest": deployment.Unsupported,
			"shebang":       deployment.Unsupported,

			"elf": deployment.Partial,
		},
		Signals:  deployment.Unsupported,
		TTY:      deployment.Unsupported,
		Snapshot: deployment.Unsupported,
		Replay:   deployment.Unsupported,
		Network:  deployment.Unsupported,

		AddressSpace: deployment.Unsupported,
		Notes: []string{
			"扩展模块由宿主登记并校验导入面，客体经显式桥调用，无宿主回退",
			"executor/wasm 已能校验并解析第三方模块的导入面，但尚不能实例化执行",
			"客体没有信号、TTY、快照与回放装配；相应内核设施仅存在于 Native 部署",
			"网络默认 ENETUNREACH；定时任务、包管理、sudo、多用户返回 ENOTSUP",
			"持续 Bash 会话状态保存在客体内，宿主重建会话即为全新初态",
			"客体标准输出不是终端（WASI fd 1 是管道/预开目录流），`nohup` 因此不会在客体里产生 nohup.out——它的重定向那半只在 native 部署与交互终端下生效；「忽略 SIGHUP」这半在客体里照常成立（内核处置）",
			"客体**没有 chmod**：WASI preview1 只有 path_filestat_set_times、没有 path_chmod，宿主适配层因此在 wasip1 上不实现 vm/fs.Modes，命令侧拿到的是 ENOTSUP（能力缺失），不做近似；模式位能力只在原生部署可用",
			"ELF：静态映像在客体里由 RV64IM 解释执行、ECALL 交给 Linux ABI 适配层，程序能 openat 自己的文件（描述符安装穿过作用域视图）；不支持动态链接、TLS 与线程，没有 mmap（brk 可用，按页增长 program break），地址空间按进程登记到内核（客体在宿主线性内存里跑，页表语义不适用）；客体也装配了分层计量，ELF 程序的指令与映射都记账",
		},
	}
}
