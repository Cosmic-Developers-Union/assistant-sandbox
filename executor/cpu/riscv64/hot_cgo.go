//go:build cgo && linux

package riscv64

/*
#cgo CXXFLAGS: -O3 -std=c++17 -fno-exceptions -fno-rtti
#include "hot_cpp.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

const hotBatchSupported = true

func runHotBatch(
	registers []uint64,
	pc, pageBase uint64,
	limit uint64,
	regions []uint64,
	pageNumbers []uint64,
	pageAddresses []uintptr,
	pageSize uint64,
	codeBases []uint64,
	codeWords []uintptr,
	codeLengths []uintptr,
	instructionPageSize uint64,
) (uint64, uint64) {
	if len(registers) < numRegs || limit == 0 {
		return pc, 0
	}
	var regionsPointer *C.uint64_t
	if len(regions) > 0 {
		regionsPointer = (*C.uint64_t)(unsafe.Pointer(&regions[0]))
	}
	var pageNumbersPointer *C.uint64_t
	if len(pageNumbers) > 0 {
		pageNumbersPointer = (*C.uint64_t)(unsafe.Pointer(&pageNumbers[0]))
	}
	var pageAddressesPointer *C.uint64_t
	if len(pageAddresses) > 0 {
		pageAddressesPointer = (*C.uint64_t)(unsafe.Pointer(&pageAddresses[0]))
	}
	var codeBasesPointer *C.uint64_t
	if len(codeBases) > 0 {
		codeBasesPointer = (*C.uint64_t)(unsafe.Pointer(&codeBases[0]))
	}
	var codeWordsPointer *C.uint64_t
	if len(codeWords) > 0 {
		codeWordsPointer = (*C.uint64_t)(unsafe.Pointer(&codeWords[0]))
	}
	var codeLengthsPointer *C.uint64_t
	if len(codeLengths) > 0 {
		codeLengthsPointer = (*C.uint64_t)(unsafe.Pointer(&codeLengths[0]))
	}
	nativePC := C.uint64_t(pc)
	steps := C.rv64_hot_run(
		(*C.uint64_t)(unsafe.Pointer(&registers[0])),
		&nativePC,
		C.uint64_t(pageBase),
		C.uint32_t(limit),
		regionsPointer,
		C.uint32_t(len(regions)/3),
		pageNumbersPointer,
		pageAddressesPointer,
		C.uint32_t(len(pageNumbers)),
		C.uint64_t(pageSize),
		codeBasesPointer,
		codeWordsPointer,
		codeLengthsPointer,
		C.uint32_t(len(codeBases)),
		C.uint64_t(instructionPageSize),
	)
	runtime.KeepAlive(registers)
	runtime.KeepAlive(regions)
	runtime.KeepAlive(pageNumbers)
	runtime.KeepAlive(pageAddresses)
	runtime.KeepAlive(codeBases)
	runtime.KeepAlive(codeWords)
	runtime.KeepAlive(codeLengths)
	return uint64(nativePC), uint64(steps)
}
