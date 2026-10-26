package guest

// FutexProbeImageSHA256 锁定 FutexProbe() 产物摘要。
const FutexProbeImageSHA256 = "1d844f8e9ad999ede2b7188ca0095be868ed2d9ee5f6c016a1b61fcf5cbd12f5"

// futexCode 是 K05 的 futex WAIT/WAKE 差分探针程序:值不匹配的
// WAIT 立即返回 EAGAIN;值匹配且 1ms 超时返回 ETIMEDOUT;WAKE 无
// 等待者返回 0;未对齐地址、未知操作、CLOCK_REALTIME 标志与负
// timespec 返回 EINVAL。结果字节经 write(1,…) 输出后 exit(0)。
//
// 栈布局(帧 128 字节):16..23 结果字节、32 futex 字(4 字节
// 对齐)、64 超时 timespec。
func futexCode() []byte {
	const (
		sysFutex = 98
		sysWrite = 64
		sysExit  = 93
	)
	// 1ms 与 INT_MAX 的 lui+addi 装载(立即数由表达式计算)。
	const millisecond = uint64(1_000_000)
	msHi, msLo := uint32(millisecond>>12), uint32(millisecond&0xFFF)
	luiT0Ms := msHi<<12 | regT0<<7 | 0x37
	addiT0Ms := msLo<<20 | regT0<<15 | regT0<<7 | 0x13
	const intMax = 0x7FFFFFFF
	maxHi, maxLo := uint32(intMax>>12), uint32(intMax&0xFFF)
	luiT0Max := maxHi<<12 | regT0<<7 | 0x37
	addiT0Max := maxLo<<20 | regT0<<15 | regT0<<7 | 0x13

	code := []uint32{
		epAddi(regSP, regSP, -128),
		// futex 字置 1:WAIT 期望 0 → EAGAIN。
		epAddi(regT0, regZero, 1),
		epSw(regT0, regSP, 32),
		epAddi(regA0, regSP, 32),
		epAddi(regA1, regZero, 0), // FUTEX_WAIT
		epAddi(regA2, regZero, 0),
		epAddi(regA3, regZero, 0), // NULL 超时
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 16), // r0:-EAGAIN
		// 字清零:WAIT 期望 0、1ms 超时 → ETIMEDOUT。
		epSw(regZero, regSP, 32),
		epSd(regZero, regSP, 64),
		luiT0Ms,
		addiT0Ms,
		epSd(regT0, regSP, 72),
		epAddi(regA0, regSP, 32),
		epAddi(regA1, regZero, 0),
		epAddi(regA2, regZero, 0),
		epAddi(regA3, regSP, 64),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 17), // r1:-ETIMEDOUT
		// WAKE:无等待者 → 0。
		epAddi(regA0, regSP, 32),
		epAddi(regA1, regZero, 1), // FUTEX_WAKE
		epAddi(regA2, regZero, 1),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 18), // r2:0
		// WAKE 全量(INT_MAX):仍无等待者 → 0。
		luiT0Max,
		addiT0Max,
		epAddi(regA0, regSP, 32),
		epAddi(regA1, regZero, 1),
		epAddi(regA2, regT0, 0),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 19), // r3:0
		// 未对齐地址 → EINVAL。
		epAddi(regA0, regSP, 33),
		epAddi(regA1, regZero, 0),
		epAddi(regA2, regZero, 0),
		epAddi(regA3, regZero, 0),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 20), // r4:-EINVAL
		// 未知操作(LOCK_PI=6,K07 登记前 EINVAL)。
		epAddi(regA0, regSP, 32),
		epAddi(regA1, regZero, 6),
		epAddi(regA2, regZero, 0),
		epAddi(regA3, regZero, 0),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 21), // r5:-EINVAL
		// FUTEX_CLOCK_REALTIME 与 WAIT 不兼容 → EINVAL。
		epAddi(regA0, regSP, 32),
		epAddi(regT0, regZero, 1),
		epSlli(regT0, regT0, 8), // 256
		epAddi(regA1, regT0, 0),
		epAddi(regA2, regZero, 0),
		epAddi(regA3, regZero, 0),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 22), // r6:-EINVAL
		// 负 timespec → EINVAL。
		epAddi(regT0, regZero, -1),
		epSd(regT0, regSP, 64),
		epSd(regZero, regSP, 72),
		epAddi(regA0, regSP, 32),
		epAddi(regA1, regZero, 0),
		epAddi(regA2, regZero, 0),
		epAddi(regA3, regSP, 64),
		epAddi(regA7, regZero, sysFutex),
		0x00000073,
		epSb(regA0, regSP, 23), // r7:-EINVAL
		// 输出八个结果字节后退出。
		epAddi(regA0, regZero, 1),
		epAddi(regA1, regSP, 16),
		epAddi(regA2, regZero, 8),
		epAddi(regA7, regZero, sysWrite),
		0x00000073,
		epAddi(regA0, regZero, 0),
		epAddi(regA7, regZero, sysExit),
		0x00000073,
	}
	return encodeInstructions(code...)
}

// FutexProbe 返回 futex WAIT/WAKE 差分探针 ELF。
func FutexProbe() []byte { return buildProbeELF(futexCode()) }
