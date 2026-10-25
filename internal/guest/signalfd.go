package guest

import (
	"encoding/binary"
)

// SignalfdProbeImageSHA256 锁定 SignalfdProbe() 产物摘要。
const SignalfdProbeImageSHA256 = "aae83cb8912e8878141fa608112b1e7feff79d1d0a6985b981d985814f1c0611"

// signalfdCode 是 L11 signalfd 探针程序:屏蔽 SIGUSR1 → signalfd4
// 创建 → kill(getpid(), SIGUSR1) → read(fd) 读取 128 字节 siginfo →
// 以 ssi_signo(首 4 字节)作为退出码。被屏蔽信号由 fd 队列承接,
// 不触发默认终止;掩码外的信号不会进入 fd,因此退出码为 10 即
// 证明完整链路(系统调用→内核路由→fd 队列→读取)工作。
func signalfdCode() []byte {
	code := []uint32{
		0xF0010113, // addi sp, sp, -256(缓冲与 fd 槽都在栈上)
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → set
		0x00000513, // li a0, 0(SIG_BLOCK)
		0x00000613, // li a2, 0(oldset=NULL)
		0x00800693, // li a3, 8(sigsetsize)
		0x08700893, // li a7, 135(rt_sigprocmask)
		0x00000073, // ecall
		0xFFF00513, // li a0, -1(创建新 fd)
		0x00000597, // auipc a1, 0
		0,          // addi a1, a1, → set
		0x00800613, // li a2, 8(sizemask)
		0x00000693, // li a3, 0(flags)
		0x04A00893, // li a7, 74(signalfd4)
		0x00000073, // ecall
		0x0EA12E23, // sw a0, 252(sp) → fd
		0x0AC00893, // li a7, 172(getpid)
		0x00000073, // ecall → a0=pid
		0x00A00593, // li a1, 10(SIGUSR1)
		0x08100893, // li a7, 129(kill)
		0x00000073, // ecall(被屏蔽信号转入 fd 队列)
		0x0FC12503, // lw a0, 252(sp) → fd
		0x00010593, // mv a1, sp(siginfo 缓冲)
		0x08000613, // li a2, 128
		0x03F00893, // li a7, 63(read)
		0x00000073, // ecall → 128
		0x00012503, // lw a0, 0(sp) → ssi_signo
		0x05D00893, // li a7, 93(exit)
		0x00000073, // ecall
	}
	// set 数据跟在代码后,修补两条 addi 的立即数。
	// auipc 的基址是 auipc 指令自身的 PC,立即数须以它为基准。
	setOffset := len(code) * 4
	patch := func(index int, auipcPC int) {
		code[index] = uint32(setOffset-auipcPC)<<20 | 0x00058593
	}
	patch(2, 4)   // 第一处 auipc 的 PC 偏移 4
	patch(10, 36) // 第二处 auipc 的 PC 偏移 36

	out := make([]byte, 0, setOffset+8)
	for _, instruction := range code {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], instruction)
		out = append(out, raw[:]...)
	}
	// set:屏蔽 SIGUSR1(位 9)。
	var set [8]byte
	binary.LittleEndian.PutUint64(set[:], uint64(1)<<9)
	return append(out, set[:]...)
}

// SignalfdProbe 返回 signalfd 差分探针 ELF;布局与 Probe 相同。
func SignalfdProbe() []byte {
	payload := signalfdCode()
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
