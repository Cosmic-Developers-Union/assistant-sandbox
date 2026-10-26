package guest

// TidAddressProbeImageSHA256 锁定 TidAddressProbe() 产物摘要。
const TidAddressProbeImageSHA256 = "053a11b29dfadb8f95c7567aeb5ad01d061ed81061798cea9892336b236a0046"

// tidAddressCode 是 K03 的 set_tid_address 差分探针程序:set_tid_address
// 返回调用方 TID,与 getpid 相等(单线程模型);重复登记覆盖;地址 0
// 撤销仍返回 TID。结果字节经 write(1,…) 输出后以校验和退出。
func tidAddressCode() []byte {
	const (
		sysSetTidAddress = 96
		sysGetpid        = 172
		sysWrite         = 64
		sysExit          = 93
	)
	// 校验:两次 set_tid_address 的返回都等于 getpid;相等记 1。
	code := []uint32{
		epAddi(regSP, regSP, -128),
		// getpid() → s0。
		epAddi(regA7, regZero, sysGetpid),
		0x00000073,
		epAddi(regS0, regA0, 0),
		// set_tid_address(0x100) → a0;与 getpid 相等校验。
		epLui(regA0, 0),
		epAddi(regA0, regZero, 0x100),
		epAddi(regA7, regZero, sysSetTidAddress),
		0x00000073,
		epXor(regT0, regA0, regS0), // t0 = a0 ^ getpid
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 16), // r0:返回==getpid
		// 再次登记不同地址:返回仍 == getpid(覆盖语义)。
		epAddi(regA0, regZero, 0x200),
		epAddi(regA7, regZero, sysSetTidAddress),
		0x00000073,
		epXor(regT0, regA0, regS0),
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 17), // r1:覆盖后返回==getpid
		// write(1, 16sp, 2) 后 exit(0)。
		epAddi(regA0, regZero, 1),
		epAddi(regA1, regSP, 16),
		epAddi(regA2, regZero, 2),
		epAddi(regA7, regZero, sysWrite),
		0x00000073,
		epAddi(regA0, regZero, 0),
		epAddi(regA7, regZero, sysExit),
		0x00000073,
	}
	return encodeInstructions(code...)
}

// TidAddressProbe 返回 set_tid_address 差分探针 ELF。
func TidAddressProbe() []byte { return buildProbeELF(tidAddressCode()) }
