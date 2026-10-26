package guest

// EpollProbeImageSHA256 锁定 EpollProbe() 产物摘要。
const EpollProbeImageSHA256 = "14be8e67d5bb2e958229017baac6cef7cedcc9177abec871298c2b94c3bbbb3b"

// RV64I 寄存器编号(探针用到的一部分)。
const (
	regZero = 0
	regSP   = 2
	regT0   = 5
	regT1   = 6
	regS0   = 8
	regS1   = 9
	regA0   = 10
	regA1   = 11
	regA2   = 12
	regA3   = 13
	regA4   = 14
	regA5   = 15
	regA7   = 17
	regS2   = 18
	regS3   = 19
	regS4   = 20
)

// epAddi 等是探针用的指令编码器:立即数由表达式计算,避免手工
// 位拼接出错(li/mv 是 addi 的特例)。imm 限于 12 位有符号范围。
func epAddi(rd, rs1, imm int) uint32 {
	return uint32(imm)<<20 | uint32(rs1)<<15 | uint32(rd)<<7 | 0x13
}

func epSlti(rd, rs1 int) uint32 { return 2<<12 | uint32(rs1)<<15 | uint32(rd)<<7 | 0x13 }

// epSltiuImm 编码 sltiu rd, rs1, imm(探针用 imm=1 判零:rs==0 → 1)。
func epSltiuImm(rd, rs1, imm int) uint32 {
	return uint32(imm)<<20 | uint32(rs1)<<15 | 3<<12 | uint32(rd)<<7 | 0x13
}

func epXori(rd, rs1, imm int) uint32 {
	return uint32(imm)<<20 | uint32(rs1)<<15 | 4<<12 | uint32(rd)<<7 | 0x13
}

func epAndi(rd, rs1, imm int) uint32 {
	return uint32(imm)<<20 | uint32(rs1)<<15 | 7<<12 | uint32(rd)<<7 | 0x13
}

func epLui(rd, imm20 uint32) uint32 {
	return imm20<<12 | rd<<7 | 0x37
}

func epLbu(rd, base, off int) uint32 {
	return uint32(off)<<20 | uint32(base)<<15 | 4<<12 | uint32(rd)<<7 | 0x03
}

func epSlli(rd, rs1, shamt uint32) uint32 {
	return shamt<<20 | rs1<<15 | 1<<12 | rd<<7 | 0x13
}

func epLw(rd, base, off int) uint32 {
	return uint32(off)<<20 | uint32(base)<<15 | 2<<12 | uint32(rd)<<7 | 0x03
}

func epLd(rd, base, off int) uint32 {
	return uint32(off)<<20 | uint32(base)<<15 | 3<<12 | uint32(rd)<<7 | 0x03
}

func epStore(rs2, base, off int, funct3 uint32) uint32 {
	imm := uint32(off)
	return (imm>>5)<<25 | uint32(rs2)<<20 | uint32(base)<<15 | funct3<<12 | (imm&0x1F)<<7 | 0x23
}

func epSb(rs2, base, off int) uint32 { return epStore(rs2, base, off, 0) }

func epSw(rs2, base, off int) uint32 { return epStore(rs2, base, off, 2) }

func epSd(rs2, base, off int) uint32 { return epStore(rs2, base, off, 3) }

// epollCode 是 L08 的边缘触发/一次性差分探针程序:pipe2 建管道、
// epoll_ctl 以 EPOLLIN|EPOLLET 登记读端后,空管道等待返回 0(无初始
// 边沿);写入 → 报告一次;未排空时重复等待不再报告;再次写入产生
// 新边沿仍报告;排空后写入又报告。随后 MOD 换 EPOLLIN|EPOLLONESHOT:
// 就绪状态下重新武装立即报告一次,新数据不再报告(禁用),再 MOD
// 又立即报告。结果字节序列经 write(1,…) 输出后 exit(0)。
//
// 栈布局(帧 192 字节):0 管道描述符、16..36 结果字节、48 登记事件
// (events u32 + data u64)、64 等待返回数组(2 项)、128 读缓冲、
// 160..163 四个单字节数据源。
func epollCode() []byte {
	const (
		sysPipe2        = 59
		sysEpollCreate1 = 20
		sysEpollCtl     = 21
		sysEpollPwait   = 22
		sysRead         = 63
		sysWrite        = 64
		sysExit         = 93
	)
	code := []uint32{
		epAddi(regSP, regSP, -192),
		// pipe2(sp, 0):读端存 s0,写端存 s1。
		epAddi(regA0, regSP, 0),
		epAddi(regA1, regZero, 0),
		epAddi(regA7, regZero, sysPipe2),
		0x00000073,
		epSlti(regT0, regA0),
		epSb(regT0, regSP, 16), // r0:负值 errno 标记(期望 0)
		epLw(regS0, regSP, 0),
		epLw(regS1, regSP, 4),
		epSlti(regT0, regS0),
		epSb(regT0, regSP, 17), // r1
		epSlti(regT0, regS1),
		epSb(regT0, regSP, 18), // r2
		// epoll_create1(0):实例存 s2。
		epAddi(regA0, regZero, 0),
		epAddi(regA7, regZero, sysEpollCreate1),
		0x00000073,
		epAddi(regS2, regA0, 0),
		epSlti(regT0, regS2),
		epSb(regT0, regSP, 19), // r3
	}
	// 登记事件:events = 1<<31 | EPOLLIN(0x80000001),data = 0x77。
	code = append(code,
		epAddi(regT0, regZero, 1),
		epSlli(regT0, regT0, 31),
		epAddi(regT0, regT0, 1),
		epSw(regT0, regSP, 48),
		epAddi(regT1, regZero, 0x77),
		epSd(regT1, regSP, 56),
		// epoll_ctl(s2, ADD, s0, 48sp)。
		epAddi(regA0, regS2, 0),
		epAddi(regA1, regZero, 1),
		epAddi(regA2, regS0, 0),
		epAddi(regA3, regSP, 48),
		epAddi(regA7, regZero, sysEpollCtl),
		0x00000073,
	)
	pwait0 := func(resultOff int) {
		code = append(code,
			epAddi(regA0, regS2, 0),
			epAddi(regA1, regSP, 64),
			epAddi(regA2, regZero, 2),
			epAddi(regA3, regZero, 0),
			epAddi(regA4, regZero, 0),
			epAddi(regA7, regZero, sysEpollPwait),
			0x00000073,
			epSb(regA0, regSP, resultOff),
		)
	}
	writeByte := func(dataOff, resultOff int, value int) {
		code = append(code,
			epAddi(regT0, regZero, value),
			epSb(regT0, regSP, dataOff),
			epAddi(regA0, regS1, 0),
			epAddi(regA1, regSP, dataOff),
			epAddi(regA2, regZero, 1),
			epAddi(regA7, regZero, sysWrite),
			0x00000073,
			epSb(regA0, regSP, resultOff),
		)
	}
	modCtl := func(resultOff int) {
		code = append(code,
			epAddi(regA0, regS2, 0),
			epAddi(regA1, regZero, 3),
			epAddi(regA2, regS0, 0),
			epAddi(regA3, regSP, 48),
			epAddi(regA7, regZero, sysEpollCtl),
			0x00000073,
			epSb(regA0, regSP, resultOff),
		)
	}

	pwait0(20) // r4:空管道无初始边沿 → 0
	writeByte(160, 21, 'A')
	pwait0(22) // r5:写 1;r6:新边沿报告 → 1
	code = append(code,
		// 返回事件的 events 位与 data 校验。
		epLw(regT0, regSP, 64),
		epAndi(regT0, regT0, 1),
		epSb(regT0, regSP, 23), // r7:EPOLLIN 置位 → 1
		epLd(regT1, regSP, 72),
		epXori(regT1, regT1, 0x77),
		epSltiuImm(regT1, regT1, 1),
		epSb(regT1, regSP, 24), // r8:data==0x77 → 1
	)
	pwait0(25) // r9:边沿消费后不再报告 → 0
	writeByte(161, 26, 'B')
	pwait0(27) // r10:写 1;r11:未排空新活动 = 新边沿 → 1
	code = append(code,
		// read(s0, 128sp, 8):排空全部数据 → 2。
		epAddi(regA0, regS0, 0),
		epAddi(regA1, regSP, 128),
		epAddi(regA2, regZero, 8),
		epAddi(regA7, regZero, sysRead),
		0x00000073,
		epSb(regA0, regSP, 28), // r12
	)
	writeByte(162, 29, 'C')
	pwait0(30) // r13:写 1;r14:排空后再写 → 新边沿 → 1
	// MOD 换一次性:events = 1<<30 | EPOLLIN(0x40000001),data = 0x88。
	code = append(code,
		epAddi(regT0, regZero, 1),
		epSlli(regT0, regT0, 30),
		epAddi(regT0, regT0, 1),
		epSw(regT0, regSP, 48),
		epAddi(regT1, regZero, 0x88),
		epSd(regT1, regSP, 56),
	)
	modCtl(31) // r15:MOD 返回 0
	pwait0(32) // r16:就绪状态重新武装立即报告 → 1
	writeByte(163, 33, 'D')
	pwait0(34) // r17:写 1;r18:一次性禁用,新数据不报 → 0
	modCtl(35) // r19:再 MOD 返回 0
	pwait0(36) // r20:再次重新武装后立即可报 → 1
	code = append(code,
		// write(1, 16sp, 21) 输出结果字节后 exit(0)。
		epAddi(regA0, regZero, 1),
		epAddi(regA1, regSP, 16),
		epAddi(regA2, regZero, 21),
		epAddi(regA7, regZero, sysWrite),
		0x00000073,
		epAddi(regA0, regZero, 0),
		epAddi(regA7, regZero, sysExit),
		0x00000073,
	)
	return encodeInstructions(code...)
}

// EpollProbe 返回边缘触发/一次性差分探针 ELF。
func EpollProbe() []byte { return buildProbeELF(epollCode()) }
