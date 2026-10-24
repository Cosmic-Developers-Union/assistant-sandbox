package elf

import (
	"encoding/binary"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func malformed(detail string) error    { return errno.Wrap(errno.NoExec, detail) }
func invalid(detail string) error      { return errno.Wrap(errno.Invalid, detail) }
func overflow(detail string) error     { return errno.Wrap(errno.Overflow, detail) }
func limit(detail string) error        { return errno.Wrap(errno.Limit, detail) }
func notSupported(detail string) error { return errno.Wrap(errno.NotSupported, detail) }

type reader struct{ raw []byte }

func (r reader) slice(offset, length uint64) ([]byte, error) {
	end, ok := addOverflow(offset, length)
	if !ok || end > uint64(len(r.raw)) {
		return nil, malformed(fmt.Sprintf("读取 [%#x,%#x) 超出映像 %d 字节", offset, end, len(r.raw)))
	}
	return r.raw[offset:end], nil
}

const (
	offIdentClass    = 4
	offIdentData     = 5
	offIdentVersion  = 6
	offType          = 16
	offMachine       = 18
	offVersion       = 20
	offEntry         = 24
	offFlags         = 48
	offHeaderSize    = 52
	offProgramOffset = 32
	offProgramEntry  = 54
	offProgramCount  = 56

	phOffType   = 0
	phOffFlags  = 4
	phOffOffset = 8
	phOffVaddr  = 16
	phOffFilesz = 32
	phOffMemsz  = 40
	phOffAlign  = 48
)

type header struct {
	entry     uint64
	typeID    uint16
	machine   uint16
	flags     uint32
	programAt uint64
	entrySize uint16
	count     uint16
}

func parseHeader(r reader) (header, error) {
	raw, err := r.slice(0, headerSize)
	if err != nil {
		return header{}, err
	}
	if binary.LittleEndian.Uint32(raw) != magicNumber {
		return header{}, malformed("魔数不是 ELF")
	}
	if class := raw[offIdentClass]; class != classELF64 {
		return header{}, notSupported(fmt.Sprintf("只支持 ELF64，映像是 class %d", class))
	}
	if data := raw[offIdentData]; data != dataLittleEndian {
		return header{}, notSupported(fmt.Sprintf("只支持小端，映像是 data %d", data))
	}
	if identVersion := raw[offIdentVersion]; identVersion != versionCurrent {
		return header{}, malformed(fmt.Sprintf("e_ident 版本 %d 不是 %d", identVersion, versionCurrent))
	}
	if version := binary.LittleEndian.Uint32(raw[offVersion:]); version != versionCurrent {
		return header{}, malformed(fmt.Sprintf("e_version %d 不是 %d", version, versionCurrent))
	}
	if size := binary.LittleEndian.Uint16(raw[offHeaderSize:]); size != headerSize {
		return header{}, malformed(fmt.Sprintf("e_ehsize %d 不是 %d", size, headerSize))
	}
	head := header{
		entry:     binary.LittleEndian.Uint64(raw[offEntry:]),
		typeID:    binary.LittleEndian.Uint16(raw[offType:]),
		machine:   binary.LittleEndian.Uint16(raw[offMachine:]),
		flags:     binary.LittleEndian.Uint32(raw[offFlags:]),
		programAt: binary.LittleEndian.Uint64(raw[offProgramOffset:]),
		entrySize: binary.LittleEndian.Uint16(raw[offProgramEntry:]),
		count:     binary.LittleEndian.Uint16(raw[offProgramCount:]),
	}
	if head.typeID != typeExecutable && head.typeID != typeShared {
		return header{}, notSupported(fmt.Sprintf("e_type %d 既不是可执行也不是共享对象", head.typeID))
	}
	if head.machine != machineRISCV {
		return header{}, notSupported(fmt.Sprintf("e_machine %d 不是 RISC-V(%d)", head.machine, machineRISCV))
	}
	switch abi := head.flags & riscvFloatABIMask; abi {
	case riscvFloatABISoft, riscvFloatABISingle, riscvFloatABIDouble:
	default:
		return header{}, notSupported(fmt.Sprintf("映像要求浮点 ABI %d，仅支持 soft/single/double", abi))
	}
	if head.entrySize != programHeaderSize {
		return header{}, malformed(fmt.Sprintf("e_phentsize %d 不是 %d", head.entrySize, programHeaderSize))
	}
	if head.count == 0 {
		return header{}, malformed("没有程序头")
	}
	return head, nil
}

type programHeader struct {
	typeID uint32
	flags  uint32
	offset uint64
	vaddr  uint64
	filesz uint64
	memsz  uint64
	align  uint64
}

func (r reader) programHeaderAt(base, entrySize, index uint64) programHeader {
	raw := r.raw[base+index*entrySize:][:programHeaderSize]
	return programHeader{
		typeID: binary.LittleEndian.Uint32(raw[phOffType:]),
		flags:  binary.LittleEndian.Uint32(raw[phOffFlags:]),
		offset: binary.LittleEndian.Uint64(raw[phOffOffset:]),
		vaddr:  binary.LittleEndian.Uint64(raw[phOffVaddr:]),
		filesz: binary.LittleEndian.Uint64(raw[phOffFilesz:]),
		memsz:  binary.LittleEndian.Uint64(raw[phOffMemsz:]),
		align:  binary.LittleEndian.Uint64(raw[phOffAlign:]),
	}
}

func addOverflow(left, right uint64) (uint64, bool) {
	sum := left + right
	return sum, sum >= left
}

func subOverflow(left, right uint64) (uint64, bool) {
	if right > left {
		return 0, false
	}
	return left - right, true
}

func alignDown(address, alignment uint64) uint64 { return address &^ (alignment - 1) }

func alignUp(address, alignment uint64) (uint64, bool) {
	aligned := alignDown(address, alignment)
	if aligned == address {
		return address, true
	}
	return addOverflow(aligned, alignment)
}

func isPowerOfTwo(value uint64) bool { return value != 0 && value&(value-1) == 0 }
