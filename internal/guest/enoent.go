package guest

import (
	"encoding/binary"
)

// ENOENTProbeImageSHA256 锁定 ENOENTProbe() 产物摘要。
const ENOENTProbeImageSHA256 = "05f33297cf1db4b49b44a0e6e246d7c3d354de6343cf397ab86074e47b677c4c"

// enoentCode:openat(AT_FDCWD, "/missing", O_RDONLY, 0) 返回 -ENOENT,
// 以该 errno 低字节作为退出状态(参考 Linux 与本 VM 同为 254)。
func enoentCode() []byte {
	code := []uint32{
		0xF9C00513, // li a0, -100  (AT_FDCWD:12 位补码 0xF9C)
		0x00000597, // auipc a1, 0
		0x02058593, // addi a1, a1, 32  → "/missing"(第 9 条指令后)
		0x00000613, // li a2, 0    (O_RDONLY)
		0x00000693, // li a3, 0    (mode)
		0x03800893, // li a7, 56   (__NR_openat)
		0x00000073, // ecall       → a0 = -ENOENT
		0x05D00893, // li a7, 93   (__NR_exit,a0 保持 -2)
		0x00000073, // ecall
	}
	out := make([]byte, 0, len(code)*4+len("/missing\x00"))
	for _, instruction := range code {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], instruction)
		out = append(out, raw[:]...)
	}
	return append(out, "/missing\x00"...)
}

// ENOENTProbe 返回 openat 缺失路径的差分探针 ELF;布局与 Probe 相同。
func ENOENTProbe() []byte {
	payload := enoentCode()
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
	binary.LittleEndian.PutUint64(phdr[32:], uint64(fileSize))
	binary.LittleEndian.PutUint64(phdr[40:], uint64(fileSize))
	binary.LittleEndian.PutUint64(phdr[48:], 0x1000)
	copy(image[headerEnd:], payload)
	return image
}
