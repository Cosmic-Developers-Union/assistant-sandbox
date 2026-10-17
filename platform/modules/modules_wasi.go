//go:build wasip1

package modules

import "unsafe"

//go:wasmimport assistant exec
func moduleCall(request unsafe.Pointer, size uint32, response unsafe.Pointer, capacity uint32) uint32

func exchangeModule(request, response []byte) uint32 {
	return moduleCall(unsafe.Pointer(unsafe.SliceData(request)), uint32(len(request)), unsafe.Pointer(unsafe.SliceData(response)), uint32(len(response)))
}
