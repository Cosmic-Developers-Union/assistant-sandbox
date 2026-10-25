package guest

import (
	"encoding/binary"
)

// SessionProbeImageSHA256 锁定 SessionProbe() 产物摘要。
const SessionProbeImageSHA256 = "b92296157b2e2d5097562190449842d26343a9e6dbc57c461e562df7e88c550f"

// sessionCode 是 H11 会话与进程组的差分探针程序:依次调用
// getpgid(0)、getsid(0)、setpgid(0,0)、setsid()、getpgid(不存在)、
// getsid(不存在)、setpgid(不存在,0)、setpgid(0,不存在组)、
// getpgid(-1)、getsid(-1),把每次调用的 errno(成功为 0)写成一个
// 字节存入栈上,再经 write(1,…) 输出 10 个字节。
// 进程组号与会话号本身不参与比较:参考 Linux(qemu 用户态)与本 VM 的
// 进程号体系不同,errno 序列才是可比语义。
func sessionCode() []byte {
	code := []uint32{
		0xFE010113,
		0x09B00893,
		0x00000513,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A10023,
		0x09C00893,
		0x00000513,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A100A3,
		0x09A00893,
		0x00000513,
		0x00000593,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A10123,
		0x09D00893,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A101A3,
		0x09B00893,
		0x7FFFF537,
		0xFFF50513,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A10223,
		0x09C00893,
		0x7FFFF537,
		0xFFF50513,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A102A3,
		0x09A00893,
		0x7FFFF537,
		0xFFF50513,
		0x00000593,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A10323,
		0x09A00893,
		0x00000513,
		0x7FFFF5B7,
		0xFFF58593,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A103A3,
		0x09B00893,
		0xFFF00513,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A10423,
		0x09C00893,
		0xFFF00513,
		0x00000073,
		0x00054463,
		0x00000513,
		0x40A00533,
		0x00A104A3,
		0x04000893,
		0x00100513,
		0x00010593,
		0x00A00613,
		0x00000073,
		0x05D00893,
		0x00000513,
		0x00000073,
	}
	out := make([]byte, 0, len(code)*4)
	for _, instruction := range code {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], instruction)
		out = append(out, raw[:]...)
	}
	return out
}

// SessionProbe 返回会话与进程组差分探针 ELF;布局与 Probe 相同。
func SessionProbe() []byte {
	payload := sessionCode()
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
