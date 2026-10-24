// Package guest 生成可重复的最小客体程序(A08):一个静态 RV64 ELF
// 探针,参考 Linux 与本 VM 使用同一份字节码。构建器为纯 Go(无外部
// 工具链),源码清单与产物摘要由测试锁定,变更必须显式。
package guest

import (
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"fmt"
)

//go:embed probe.asm
var probeSource string

// ProbeSourceSHA256 是 probe.s(语义源码)的 sha256;测试锁定。
const ProbeSourceSHA256 = "9feff7fd10fd4ec35802e21509c0d39df1c395f8b6682337b7140cd7b5ccd548"

// probeImageSHA256 记录 Build() 当前产物的 sha256;编码或布局任何
// 变更都会使该断言失败,必须连同用途说明一起显式更新。
const ProbeImageSHA256 = "9ea76441b535d2820e3c6c06820034802bc70dc06e6ab2ba05e1ffb0515ae360"

// 布局常量:单 PT_LOAD 从文件 offset 0 覆盖全部内容,装入 vaddr
// 0x10000——ELF 头与程序头随段映射(与真实静态 ELF 及本仓库装载器
// 的"程序头表须在段内"约束一致)。_start 紧跟头表(0x78)。
const (
	ehdrSize     = 64
	phdrSize     = 56
	phdrOff      = 64
	headerEnd    = ehdrSize + phdrSize // 0x78
	codeBase     = 0x10000
	entry        = codeBase + 0x78
	riscvMachine = 243
)

// probeCode 是 probe.s 的手工编码(9 条指令 + 消息)。
func probeCode() []byte {
	code := []uint32{
		0x04000893, // li a7, 64   (__NR_write)
		0x00100513, // li a0, 1    (stdout)
		0x00000597, // auipc a1, 0
		0x01C58593, // addi a1, a1, 28  → msg(第 9 条指令后)
		0x00900613, // li a2, 9    (len)
		0x00000073, // ecall
		0x05D00893, // li a7, 93   (__NR_exit)
		0x02A00513, // li a0, 42
		0x00000073, // ecall
	}
	out := make([]byte, 0, len(code)*4+len("guest-ok\n"))
	for _, instruction := range code {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], instruction)
		out = append(out, raw[:]...)
	}
	return append(out, "guest-ok\n"...)
}

// Probe 返回完整 ELF 映像;同一进程与跨进程的输出字节完全一致。
func Probe() []byte {
	payload := probeCode()
	fileSize := headerEnd + len(payload)

	image := make([]byte, fileSize)
	copy(image[0:4], "\x7fELF")
	image[4] = 2                                            // ELFCLASS64
	image[5] = 1                                            // ELFDATA2LSB
	image[6] = 1                                            // EV_CURRENT
	image[7] = 0                                            // ELFOSABI_SYSV
	image[8] = 0                                            // ABI 版本
	binary.LittleEndian.PutUint16(image[16:], 2)            // ET_EXEC
	binary.LittleEndian.PutUint16(image[18:], riscvMachine) // EM_RISCV
	binary.LittleEndian.PutUint32(image[20:], 1)            // EV_CURRENT
	binary.LittleEndian.PutUint64(image[24:], entry)
	binary.LittleEndian.PutUint64(image[32:], phdrOff)
	binary.LittleEndian.PutUint64(image[40:], 0) // e_shoff:无节头
	binary.LittleEndian.PutUint32(image[48:], 0) // e_flags:RV64I 基础
	binary.LittleEndian.PutUint16(image[52:], ehdrSize)
	binary.LittleEndian.PutUint16(image[54:], phdrSize)
	binary.LittleEndian.PutUint16(image[56:], 1) // e_phnum
	binary.LittleEndian.PutUint16(image[58:], 0) // e_shentsize
	binary.LittleEndian.PutUint16(image[60:], 0) // e_shnum
	binary.LittleEndian.PutUint16(image[62:], 0) // e_shstrndx

	phdr := image[phdrOff:]
	binary.LittleEndian.PutUint32(phdr[0:], 1) // PT_LOAD
	binary.LittleEndian.PutUint32(phdr[4:], 5) // PF_R|PF_X
	binary.LittleEndian.PutUint64(phdr[8:], 0)
	binary.LittleEndian.PutUint64(phdr[16:], codeBase)
	binary.LittleEndian.PutUint64(phdr[24:], codeBase)
	binary.LittleEndian.PutUint64(phdr[32:], uint64(fileSize))
	binary.LittleEndian.PutUint64(phdr[40:], uint64(fileSize))
	binary.LittleEndian.PutUint64(phdr[48:], 0x1000)

	copy(image[headerEnd:], payload)
	return image
}

// ImageDigestOf 返回给定映像字节的 sha256 十六进制摘要(供差分
// 执行器与落盘产物核对)。
func ImageDigestOf(image []byte) string {
	sum := sha256.Sum256(image)
	return fmt.Sprintf("%x", sum)
}

// SourceDigest 返回语义源码(probe.asm)的 sha256 十六进制摘要。
func SourceDigest() string {
	sum := sha256.Sum256([]byte(probeSource))
	return fmt.Sprintf("%x", sum)
}

// ImageDigest 返回构建产物的 sha256 十六进制摘要。
func ImageDigest() string { return ImageDigestOf(Probe()) }
