//go:build !cgo || !linux

package riscv64

const hotBatchSupported = false

func runHotBatch(_ []uint64, pc, _ uint64, _ uint64,
	_ []uint64, _ []uint64, _ []uintptr, _ uint64,
	_ []uint64, _ []uintptr, _ []uintptr, _ uint64) (uint64, uint64) {
	return pc, 0
}
