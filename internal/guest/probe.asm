# guest-probe — A08 最小客体探针(RV64I,静态 ELF,无 libc)
# 语义:write(1, msg, 9) 后 exit(42)。只依赖 write/exit 两个系统调用,
# 参考 Linux(riscv64)与本 VM 执行同一份字节码(A09 差分对象)。
# 编码器 internal/guest/probe.go 按本清单手工编码;probe_test.go
# 断言源码摘要与产物摘要,任何一方变更都必须显式更新。
	.text
	.globl _start
_start:
	li	a7, 64			# __NR_write(asm-generic riscv64)
	li	a0, 1			# fd = stdout
	la	a1, msg			# auipc+addi,msg 在第 9 条指令后
	li	a2, 9			# count = len("guest-ok\n")
	ecall
	li	a7, 93			# __NR_exit
	li	a0, 42			# status
	ecall
msg:
	.ascii	"guest-ok\n"
