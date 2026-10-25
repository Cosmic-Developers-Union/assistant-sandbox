package guest

import (
	"encoding/binary"
)

// PipeProbeImageSHA256 锁定 PipeProbe() 产物摘要。
const PipeProbeImageSHA256 = "48402768a2275d73d69ab6e9751246de176feb5a546dc31332b3142ae3d1f28c"

// pipeCode 是 I15 管道差分探针程序:pipe2(O_NONBLOCK) 创建后
// 依次空读(EAGAIN)、写 2 字节、读回、再空读(EAGAIN);
// rt_sigaction 把 SIGPIPE 设为 SIG_IGN 后关闭读端,断管写返回 EPIPE
// 且进程不终止;输出七个结果字节后恢复 SIG_DFL 再断管写,进程以
// SIGPIPE 默认处置终止(退出码 128+13)。
func pipeCode() []byte {
	code := []uint32{
		0xF0010113, // addi sp, sp, -256
		0x00010513, // mv a0, sp → fds
		0x00100393, // li t2, 1
		0x00B39593, // slli a1, t2, 11 → 0o4000 (O_NONBLOCK)
		0x03B00893, // li a7, 59 (pipe2)
		0x00000073, // ecall
		0x00A10823, // sb a0, 16(sp)
		0x00012283, // lw t0, 0(sp) → 读端
		0x00410303, // lw t1, 4(sp) → 写端
		0x00028513, // mv a0, t0
		0x02010593, // addi a1, sp, 32 → 缓冲
		0x00800613, // li a2, 8
		0x03F00893, // li a7, 63 (read)
		0x00000073, // ecall → -EAGAIN
		0x00A108A3, // sb a0, 17(sp)
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → "hi"
		0x00200613, // li a2, 2
		0x00030513, // mv a0, t1
		0x04000893, // li a7, 64 (write)
		0x00000073, // ecall → 2
		0x00A10923, // sb a0, 18(sp)
		0x00028513, // mv a0, t0
		0x02010593, // addi a1, sp, 32
		0x00800613, // li a2, 8
		0x03F00893, // li a7, 63
		0x00000073, // ecall → 2
		0x00A109A3, // sb a0, 19(sp)
		0x00028513, // mv a0, t0
		0x02010593, // addi a1, sp, 32
		0x00800613, // li a2, 8
		0x03F00893, // li a7, 63
		0x00000073, // ecall → -EAGAIN
		0x00A10A23, // sb a0, 20(sp)
		0x00D00513, // li a0, 13 (SIGPIPE)
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → sigaction(IGN)
		0x00000613, // li a2, 0 (oldact=NULL)
		0x00800693, // li a3, 8
		0x08600893, // li a7, 134 (rt_sigaction)
		0x00000073, // ecall → 0
		0x00A10AA3, // sb a0, 21(sp)
		0x00028513, // mv a0, t0
		0x03900893, // li a7, 57 (close)
		0x00000073, // ecall(关闭读端)
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → "hi"
		0x00100613, // li a2, 1
		0x00030513, // mv a0, t1
		0x04000893, // li a7, 64
		0x00000073, // ecall → -EPIPE(SIGPIPE 已忽略,进程存活)
		0x00A10B23, // sb a0, 22(sp)
		0x00100513, // li a0, 1
		0x01010593, // addi a1, sp, 16
		0x00700613, // li a2, 7
		0x04000893, // li a7, 64
		0x00000073, // ecall(输出七个结果字节)
		0x00D00513, // li a0, 13
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → sigaction(DFL)
		0x00000613, // li a2, 0
		0x00800693, // li a3, 8
		0x08600893, // li a7, 134
		0x00000073, // ecall(恢复默认处置)
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → "hi"
		0x00100613, // li a2, 1
		0x00030513, // mv a0, t1
		0x04000893, // li a7, 64
		0x00000073, // ecall → -EPIPE + SIGPIPE 默认处置:进程终止
		0x06300513, // li a0, 99(不应到达)
		0x05D00893, // li a7, 93
		0x00000073, // ecall
	}
	// 数据区:"hi" 后按 8 字节对齐放两份 sigaction(handler=1 即 SIG_IGN,
	// handler=0 即 SIG_DFL;flags/restorer/mask 全零)。
	messageOffset := len(code) * 4
	ignOffset := (messageOffset + 2 + 7) &^ 7
	dflOffset := ignOffset + 32
	patch := func(addiIndex, auipcIndex int, target int) {
		code[addiIndex] = uint32(target-auipcIndex*4)<<20 | 0x00058593
	}
	patch(16, 15, messageOffset)
	patch(36, 35, ignOffset)
	patch(46, 45, messageOffset)
	patch(59, 58, dflOffset)
	patch(65, 64, messageOffset)

	out := make([]byte, 0, dflOffset+32)
	for _, instruction := range code {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], instruction)
		out = append(out, raw[:]...)
	}
	out = append(out, "hi"...)
	if pad := ignOffset - len(out); pad > 0 {
		out = append(out, make([]byte, pad)...)
	}
	var action [32]byte
	binary.LittleEndian.PutUint64(action[0:], 1) // sa_handler = SIG_IGN
	out = append(out, action[:]...)
	binary.LittleEndian.PutUint64(action[0:], 0) // sa_handler = SIG_DFL
	out = append(out, action[:]...)
	return out
}

// PipeProbe 返回管道差分探针 ELF;布局与 Probe 相同。
func PipeProbe() []byte {
	payload := pipeCode()
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
