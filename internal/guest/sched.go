package guest

import (
	"encoding/binary"
)

// SpinForeverImageSHA256 锁定 SpinForeverProbe() 产物摘要。
const SpinForeverImageSHA256 = "363e3bf84b9bd82b51c0f79f9c0665de6a06f139055914dc533ba5273c27a470"

// SpinExitImageSHA256 锁定 SpinExitProbe(5) 产物摘要。
const SpinExitImageSHA256 = "148405884637a357e7467dec4aa5f011963e386ac7766fcb014754c14e172014"

// BlockedReadImageSHA256 锁定 BlockedReadProbe() 产物摘要。
const BlockedReadImageSHA256 = "e63b87db5fed384cd1d04d5b89e33f950f6ef626edae5877d8e3fcca71909b8a"

// SleepProbeImageSHA256 锁定 SleepProbe() 产物摘要。
const SleepProbeImageSHA256 = "1e11286fd726d98039dab46a9ae6a324b555dbde60ea11c57bd53ac44d59ade1"

// spinForeverCode 是 H02 的纯 CPU 死循环探针:jal x0, 0 无限自旋。
func spinForeverCode() []byte {
	return encodeInstructions(0x0000006F)
}

// spinExitCode 是 H02 的有界 CPU 密集探针:递减计数器约 50 万次
// (每轮 2 条指令,约 100 万条指令,跨多个 64Ki 时间片)后以
// 给定退出码退出。
func spinExitCode(exitCode uint8) []byte {
	// 计数器 500000 = 0x7A120:lui t0,0x7A;addi t0,t0,0x120。
	return encodeInstructions(
		0x07A002B7,                 // lui t0, 0x7A
		0x12028293,                 // addi t0, t0, 0x120 → 500000
		0xFFF28293,                 // addi t0, t0, -1   ← 循环体
		0xFE029E63,                 // bnez t0, -4
		uint32(exitCode)<<20|0x513, // li a0, exitCode
		0x05D00893,                 // li a7, 93 (exit)
		0x00000073,                 // ecall
	)
}

// blockedReadCode 是 H03 的阻塞读探针:pipe2 建管道后阻塞读空管道;
// 读返回后以返回值低 8 位退出(测试中通常永不返回)。
func blockedReadCode() []byte {
	return encodeInstructions(
		0xFF010113, // addi sp, sp, -128
		0x00010513, // mv a0, sp → fds
		0x00000593, // li a1, 0 (flags)
		0x03B00893, // li a7, 59 (pipe2)
		0x00000073, // ecall
		0x00012503, // lw a0, 0(sp) → 读端
		0x04010593, // addi a1, sp, 64 → 缓冲
		0x00800613, // li a2, 8
		0x03F00893, // li a7, 63 (read)
		0x00000073, // ecall(阻塞在空管道上)
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall(退出码 = read 结果低 8 位)
	)
}

func encodeInstructions(code ...uint32) []byte {
	out := make([]byte, 0, len(code)*4)
	for _, instruction := range code {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], instruction)
		out = append(out, raw[:]...)
	}
	return out
}

// SpinForeverProbe 返回纯 CPU 死循环探针 ELF。
func SpinForeverProbe() []byte { return buildProbeELF(spinForeverCode()) }

// SpinExitProbe 返回有界 CPU 密集探针 ELF,退出码为 exitCode。
func SpinExitProbe(exitCode uint8) []byte { return buildProbeELF(spinExitCode(exitCode)) }

// BlockedReadProbe 返回阻塞管道读探针 ELF。
func BlockedReadProbe() []byte { return buildProbeELF(blockedReadCode()) }

// buildProbeELF 以与 Probe 相同的布局封装代码载荷。
func buildProbeELF(payload []byte) []byte {
	fileSize := headerEnd + len(payload)
	image := make([]byte, fileSize)
	copy(image[0:4], "\x7fELF")
	image[4] = 2
	image[5] = 1
	image[6] = 1
	binary.LittleEndian.PutUint16(image[16:], 2)
	binary.LittleEndian.PutUint16(image[18:], riscvMachine)
	binary.LittleEndian.PutUint32(image[20:], 1)
	binary.LittleEndian.PutUint64(image[24:], entry)
	binary.LittleEndian.PutUint64(image[32:], phdrOff)
	binary.LittleEndian.PutUint16(image[52:], ehdrSize)
	binary.LittleEndian.PutUint16(image[54:], phdrSize)
	binary.LittleEndian.PutUint16(image[56:], 1)
	phdr := image[phdrOff:]
	binary.LittleEndian.PutUint32(phdr[0:], 1)
	binary.LittleEndian.PutUint32(phdr[4:], 5)
	binary.LittleEndian.PutUint64(phdr[8:], 0)
	binary.LittleEndian.PutUint64(phdr[16:], codeBase)
	binary.LittleEndian.PutUint64(phdr[24:], codeBase)
	binary.LittleEndian.PutUint32(phdr[32:], uint32(fileSize))
	binary.LittleEndian.PutUint32(phdr[40:], uint32(fileSize))
	binary.LittleEndian.PutUint64(phdr[48:], 0x1000)
	copy(image[headerEnd:], payload)
	return image
}

// sleepCode 是 H03 的睡眠探针:clock_nanosleep 单调钟睡眠 200ms 后
// 以 3 退出。
func sleepCode() []byte {
	// 200000000ns = 0xBEBC200:lui 装高 20 位,addi 补低 12 位;
	// 立即数编码由表达式计算,避免手工位拼接出错。
	const nsec = uint64(200_000_000)
	hi, lo := uint32(nsec>>12), uint32(nsec&0xFFF)
	luiT0 := hi<<12 | 5<<7 | 0x37
	addiT0 := lo<<20 | 5<<15 | 5<<7 | 0x13
	// sd x0/t0:rs2 | rs1=sp | funct3=011 | imm | opcode。
	sdSec := uint32(2<<15 | 3<<12 | 16<<7 | 0x23)
	sdNsec := uint32(5<<20 | 2<<15 | 3<<12 | 24<<7 | 0x23)
	return encodeInstructions(
		0xFF010113, // addi sp, sp, -16
		sdSec,      // sd x0, 16(sp) → tv_sec = 0(64 位)
		luiT0,      // lui t0, hi
		addiT0,     // addi t0, t0, lo → 200000000
		sdNsec,     // sd t0, 24(sp) → tv_nsec(64 位,偏移 +8)
		0x00100513, // li a0, 1 (CLOCK_MONOTONIC)
		0x00000593, // li a1, 0 (flags)
		0x01010613, // addi a2, sp, 16 → request
		0x00000693, // li a3, 0 (remain=NULL)
		0x07300893, // li a7, 115 (clock_nanosleep)
		0x00000073, // ecall
		0x05D00893, // li a7, 93 (exit)
		0xFFF54513, // xori a0, a0, -1 → 成功(0)退出 255,负 errno 取补
		0x00000073, // ecall
	)
}

// SleepProbe 返回睡眠探针 ELF。
func SleepProbe() []byte { return buildProbeELF(sleepCode()) }
