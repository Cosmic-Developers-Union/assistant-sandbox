package guest

// MemoryPressureProbeImageSHA256 锁定压力探针产物摘要。
const MemoryPressureProbeImageSHA256 = "ee335fe57f2209e54d99d428b753ce28f7c220008246a6ea065315f692631180"

// MemoryPressureProbe 尝试匿名映射 64MiB:成功 exit 0,失败(预算/
// ENOMEM)exit 2。用于 O04 的每进程内存预算验收。
func MemoryPressureProbe() []byte {
	const mapBytes = 64 << 20
	code := []uint32{
		epAddi(regSP, regSP, -16),
		epAddi(regA0, regZero, 0),
		epLui(regA1, mapBytes>>12),
		epAddi(regA2, regZero, 3),    // PROT_READ|PROT_WRITE
		epAddi(regA3, regZero, 0x22), // MAP_PRIVATE|MAP_ANONYMOUS
		epAddi(regA4, regZero, -1),
		epAddi(regA5, regZero, 0),
		epAddi(regA7, regZero, 222), // mmap
		0x00000073,
		epSlti(regT0, regA0),
		epSlli(regT0, regT0, 1),
		epAddi(regA0, regT0, 0),
		epAddi(regA7, regZero, 93), // exit
		0x00000073,
	}
	payload := make([]byte, 0, len(code)*4)
	for _, instruction := range code {
		payload = append(payload, byte(instruction), byte(instruction>>8), byte(instruction>>16), byte(instruction>>24))
	}
	return buildProbeELF(payload)
}
