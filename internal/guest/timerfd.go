package guest

// TimerfdProbeImageSHA256 锁定 TimerfdProbe() 产物摘要。
const TimerfdProbeImageSHA256 = "a1d7504c4e17e38e9c32f50a8b42762324e3fe397b14e2a1a4d88ed2566f50ad"

// timerfdCode 是 L10 的 timerfd 差分探针程序:创建非阻塞 timerfd
// (CLOCK_MONOTONIC)后,
//   - 到期前非阻塞读返回 EAGAIN;
//   - settime(interval=0, value=30ms)后 gettime 报 value ∈ (0,30ms]
//     且 interval 全零;
//   - clock_nanosleep(80ms) 等待首次到期后读取 8 字节计数 = 1;
//   - 一次性到期后 gettime 全零、再读 EAGAIN;
//   - 解除武装的 settime(全零)返回 0。
//
// 结果字节经 write(1,…) 输出后 exit(0)。栈布局(帧 256):
// 16..29 结果字节、64 读缓冲、96 旧 itimerspec、160 timespec、
// 192 现值 itimerspec、224 新 itimerspec(settime 入参)。
// itimerspec 布局 = {it_interval, it_value}(区间在前)。
func timerfdCode() []byte {
	const (
		sysTimerfdCreate = 85
		sysTimerfdSet    = 86
		sysTimerfdGet    = 87
		sysSleep         = 115
		sysRead          = 63
		sysWrite         = 64
		sysExit          = 93

		clockMonotonic = 1
		tfdNonBlock    = 0o4000

		sleepNsec = uint64(80_000_000)
		firstNsec = uint64(30_000_000)
	)
	// lui+addi 装载 64 位常量:addi 立即数是 12 位有符号,低 12 位
	// ≥2048 时进位到高位(与补码一致)。
	splitConst := func(value uint64) (hi, lo uint32) {
		hi, lo = uint32(value>>12), uint32(value&0xFFF)
		if lo >= 2048 {
			hi++
			lo = uint32(int32(lo) - 4096)
		}
		return hi, lo
	}
	sleepHi, sleepLo := splitConst(sleepNsec)
	luiSleep := sleepHi<<12 | regT0<<7 | 0x37
	addiSleep := sleepLo<<20 | regT0<<15 | regT0<<7 | 0x13
	firstHi, firstLo := splitConst(firstNsec)
	luiFirst := firstHi<<12 | regT0<<7 | 0x37
	addiFirst := firstLo<<20 | regT0<<15 | regT0<<7 | 0x13
	// TFD_NONBLOCK=2048 超出 addi 12 位有符号范围:li 1 后左移 11。
	liNonBlock := []uint32{
		epAddi(regT0, regZero, 1),
		epSlli(regT0, regT0, 11),
	}

	code := []uint32{
		epAddi(regSP, regSP, -256),
		// timerfd_create(CLOCK_MONOTONIC, TFD_NONBLOCK) → s0。
		epAddi(regA0, regZero, clockMonotonic),
	}
	code = append(code, liNonBlock...)
	code = append(code,
		epAddi(regA1, regT0, 0), // mv a1, t0 = TFD_NONBLOCK
		epAddi(regA7, regZero, sysTimerfdCreate),
		0x00000073,
		epAddi(regS0, regA0, 0),
		epSlti(regT0, regA0),
		epSb(regT0, regSP, 16), // r0:负值标记(期望 0)
		// 非阻塞读:无到期 → EAGAIN。
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regSP, 64),
		epAddi(regA2, regZero, 8),
		epAddi(regA7, regZero, sysRead),
		0x00000073,
		epSb(regA0, regSP, 17), // r1:-EAGAIN
		// settime:new={interval 全零, value 30ms} → old 写 96。
		epSd(regZero, regSP, 224), // interval.sec
		epSd(regZero, regSP, 232), // interval.nsec
		epSd(regZero, regSP, 240), // value.sec
		luiFirst,
		addiFirst,
		epSd(regT0, regSP, 248), // value.nsec = 30ms
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regZero, 0),
		epAddi(regA2, regSP, 224),
		epAddi(regA3, regSP, 96),
		epAddi(regA7, regZero, sysTimerfdSet),
		0x00000073,
		epSb(regA0, regSP, 18), // r2:0
		// gettime → 192:校验 value ∈ (0,30ms] 的秒=0、纳秒>0,
		// interval 全零。
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regSP, 192),
		epAddi(regA7, regZero, sysTimerfdGet),
		0x00000073,
		epSb(regA0, regSP, 19),  // r3:0
		epLd(regT0, regSP, 208), // value.sec
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 20),  // r4:value 秒==0
		epLd(regT0, regSP, 216), // value.nsec
		epSltiuImm(regT0, regT0, 1),
		epXori(regT0, regT0, 1),
		epSb(regT0, regSP, 21),  // r5:value 纳秒>0
		epLd(regT0, regSP, 192), // interval.sec
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 22),  // r6:区间秒==0
		epLd(regT0, regSP, 200), // interval.nsec
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 23), // r7:区间纳秒==0
		// clock_nanosleep(CLOCK_MONOTONIC, 0, {0, 80ms}, NULL)。
		epSd(regZero, regSP, 160),
		luiSleep,
		addiSleep,
		epSd(regT0, regSP, 168),
		epAddi(regA0, regZero, clockMonotonic),
		epAddi(regA1, regZero, 0),
		epAddi(regA2, regSP, 160),
		epAddi(regA3, regZero, 0),
		epAddi(regA7, regZero, sysSleep),
		0x00000073,
		epSb(regA0, regSP, 24), // r8:0
		// 读取过期计数:8 字节小端 = 1(校验低字节)。
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regSP, 64),
		epAddi(regA2, regZero, 8),
		epAddi(regA7, regZero, sysRead),
		0x00000073,
		epLbu(regT0, regSP, 64),
		epAddi(regT1, regZero, 1),
		epXori(regT0, regT0, 1),
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 25), // r9:计数低字节==1
		// 一次性到期后 gettime 全零。
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regSP, 192),
		epAddi(regA7, regZero, sysTimerfdGet),
		0x00000073,
		epSb(regA0, regSP, 26), // r10:0
		epLd(regT0, regSP, 192),
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 27), // r11:区间秒==0
		epLd(regT0, regSP, 200),
		epSltiuImm(regT0, regT0, 1),
		epSb(regT0, regSP, 28), // r12:区间纳秒==0
		// 解除武装的 settime(全零)。
		epSd(regZero, regSP, 224),
		epSd(regZero, regSP, 232),
		epSd(regZero, regSP, 240),
		epSd(regZero, regSP, 248),
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regZero, 0),
		epAddi(regA2, regSP, 224),
		epAddi(regA3, regZero, 0),
		epAddi(regA7, regZero, sysTimerfdSet),
		0x00000073,
		epSb(regA0, regSP, 29), // r13:0
		// write(1, 16sp, 14) 后 exit(0)。
		epAddi(regA0, regZero, 1),
		epAddi(regA1, regSP, 16),
		epAddi(regA2, regZero, 14),
		epAddi(regA7, regZero, sysWrite),
		0x00000073,
		epAddi(regA0, regZero, 0),
		epAddi(regA7, regZero, sysExit),
		0x00000073,
	)
	return encodeInstructions(code...)
}

// TimerfdProbe 返回 timerfd 差分探针 ELF。
func TimerfdProbe() []byte { return buildProbeELF(timerfdCode()) }
