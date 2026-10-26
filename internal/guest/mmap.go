package guest

// MmapProbeImageSHA256 锁定 MmapProbe() 产物摘要。
const MmapProbeImageSHA256 = "3aaec8e694a312f7e8d33c51bf65806a6ed9a4ced5fa817452f3bef1fdad78cb"

// mmapCode 是 E08/E09 的文件映射差分探针程序:openat 打开 /probe
// (即本 ELF 映像)后,
//   - MAP_PRIVATE 只读映射读到 ELF 魔数头(快照);
//   - MAP_PRIVATE 可写映射写入 'X' 后文件内容不变(私有修改不写回);
//   - MAP_SHARED 可写映射写入 'Y' 后 msync 确认,read 经文件读到
//     'Y'(即时持久),第二个共享映射也读到 'Y'(跨映射一致)。
//
// 结果字节经 write(1,…) 输出后 exit(0)。栈布局(帧 256):16..27
// 结果、64 读缓冲、200 存 "/probe" 路径。
func mmapCode() []byte {
	const (
		sysOpenat = 56
		sysMmap   = 222
		sysLseek  = 62
		sysRead   = 63
		sysWrite  = 64
		sysMsync  = 227
		sysExit   = 93

		mapLen      = 8192 // 两页:覆盖越文件尾零读
		protRW      = 3
		flagPrivate = 2
		flagShared  = 1
	)
	code := []uint32{
		epAddi(regSP, regSP, -256),
		// 栈上写 "/probe\0"。
		epAddi(regT0, regZero, '/'),
		epSb(regT0, regSP, 200),
		epAddi(regT0, regZero, 'p'),
		epSb(regT0, regSP, 201),
		epAddi(regT0, regZero, 'r'),
		epSb(regT0, regSP, 202),
		epAddi(regT0, regZero, 'o'),
		epSb(regT0, regSP, 203),
		epAddi(regT0, regZero, 'b'),
		epSb(regT0, regSP, 204),
		epAddi(regT0, regZero, 'e'),
		epSb(regT0, regSP, 205),
		// openat(AT_FDCWD, path, O_RDWR, 0) → s0。
		epAddi(regA0, regZero, -100),
		epAddi(regA1, regSP, 200),
		epAddi(regA2, regZero, 2),
		epAddi(regA3, regZero, 0),
		epAddi(regA7, regZero, sysOpenat),
		0x00000073,
		epAddi(regS0, regA0, 0),
		epSlti(regT0, regA0),
		epSb(regT0, regSP, 16), // r0:负值 errno 标记(期望 0)
	}
	// mmapCall 追加一次 mmap(NULL, 8192, prot, flags, s0, 0),结果
	// 存 target,负值标记写 resultOff。
	mmapCall := func(target, prot, flags, resultOff int) {
		code = append(code,
			epAddi(regA0, regZero, 0),
			epLui(regA1, mapLen>>12),
			epAddi(regA2, regZero, prot),
			epAddi(regA3, regZero, flags),
			epAddi(regA4, regS0, 0),
			epAddi(regA5, regZero, 0),
			epAddi(regA7, regZero, sysMmap),
			0x00000073,
			epAddi(target, regA0, 0),
			epSlti(regT0, regA0),
			epSb(regT0, regSP, resultOff),
		)
	}
	// expectByte 校验 base+off 处字节等于 want,结果写 resultOff。
	expectByte := func(base, off, want, resultOff int) {
		code = append(code,
			epLbu(regT0, base, off),
			epXori(regT0, regT0, want),
			epSltiuImm(regT0, regT0, 1),
			epSb(regT0, regSP, resultOff),
		)
	}
	// fileByte 经 lseek+read 读取文件字节 offset 并校验等于 want。
	fileByte := func(offset, want, resultOff int) {
		code = append(code,
			epAddi(regA0, regS0, 0),
			epAddi(regA1, regZero, offset),
			epAddi(regA2, regZero, 0), // SEEK_SET
			epAddi(regA7, regZero, sysLseek),
			0x00000073,
			epAddi(regA0, regS0, 0),
			epAddi(regA1, regSP, 64),
			epAddi(regA2, regZero, 1),
			epAddi(regA7, regZero, sysRead),
			0x00000073,
		)
		expectByte(regSP, 64, want, resultOff)
	}

	// 私有只读映射:ELF 魔数头可见。
	mmapCall(regS1, 1, flagPrivate, 17) // r1:负值标记
	expectByte(regS1, 0, 0x7F, 18)      // r2:P[0]=='\x7f'
	expectByte(regS1, 1, 'E', 19)       // r3:P[1]=='E'
	// 私有可写映射:改 'X' 后映射内可见、文件不变。
	mmapCall(regS2, protRW, flagPrivate, 20) // r4:负值标记
	code = append(code,
		epAddi(regT0, regZero, 'X'),
		epSb(regT0, regS2, 1),
	)
	expectByte(regS2, 1, 'X', 21) // r5:私有修改可见
	fileByte(1, 'E', 22)          // r6:文件未写回
	// 共享可写映射:写 'Y' → 映射内可见;msync;文件持久;
	// 第二个共享映射一致。
	mmapCall(regS3, protRW, flagShared, 23) // r7:负值标记
	code = append(code,
		epAddi(regT0, regZero, 'Y'),
		epSb(regT0, regS3, 2),
	)
	expectByte(regS3, 2, 'Y', 24) // r8:共享写入可见
	code = append(code,
		epAddi(regA0, regS3, 0),
		epLui(regA1, 1),           // 4096
		epAddi(regA2, regZero, 4), // MS_SYNC
		epAddi(regA7, regZero, sysMsync),
		0x00000073,
		epSb(regA0, regSP, 25), // r9:msync 返回 0
	)
	fileByte(2, 'Y', 26)                    // r10:文件读到 'Y'
	mmapCall(regS4, protRW, flagShared, 27) // r11:第二共享映射负值标记
	expectByte(regS4, 2, 'Y', 28)           // r12:跨映射一致
	code = append(code,
		// write(1, 16sp, 13) 输出结果后 exit(0)。
		epAddi(regA0, regZero, 1),
		epAddi(regA1, regSP, 16),
		epAddi(regA2, regZero, 13),
		epAddi(regA7, regZero, sysWrite),
		0x00000073,
		epAddi(regA0, regZero, 0),
		epAddi(regA7, regZero, sysExit),
		0x00000073,
	)
	return encodeInstructions(code...)
}

// MmapProbe 返回文件映射差分探针 ELF。
func MmapProbe() []byte { return buildProbeELF(mmapCode()) }
