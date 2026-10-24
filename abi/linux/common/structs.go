package common

import "encoding/binary"

const timespecSize = 16

func encodeTimespec(seconds, nanoseconds int64) [timespecSize]byte {
	var encoded [timespecSize]byte
	binary.LittleEndian.PutUint64(encoded[0:], uint64(seconds))
	binary.LittleEndian.PutUint64(encoded[8:], uint64(nanoseconds))
	return encoded
}

// StatInput 是编码 struct stat 所需的客体文件元数据;
// 由文件系统层从其自有 FileInfo 构造。
type StatInput struct {
	Dev       uint64
	Ino       uint64
	Mode      uint32 // 完整模式位(类型 + 权限)
	NLink     uint32
	UID       uint32
	GID       uint32
	RDev      uint64
	Size      int64
	Blksize   int64
	Blocks    int64
	AtimeSec  int64
	AtimeNsec int64
	MtimeSec  int64
	MtimeNsec int64
	CtimeSec  int64
	CtimeNsec int64
}

// statField 是 asm-generic struct stat 的字段偏移(共 128 字节),
// 与 riscv64 的 linux asm-generic 头文件布局一致。
const (
	statDev       = 0
	statIno       = 8
	statMode      = 16
	statNLink     = 20
	statUID       = 24
	statGID       = 28
	statRDev      = 32
	statSize      = 48
	statBlksize   = 56
	statBlocks    = 64
	statAtime     = 72
	statAtimeNsec = 80
	statMtime     = 88
	statMtimeNsec = 96
	statCtime     = 104
	statCtimeNsec = 112
	statSizeTotal = 128
)

// encodeStat 把客体文件元数据编码为 asm-generic struct stat。
func encodeStat(input StatInput) [statSizeTotal]byte {
	var out [statSizeTotal]byte
	putU64 := func(offset int, value uint64) {
		binary.LittleEndian.PutUint64(out[offset:], value)
	}
	putU32 := func(offset int, value uint32) {
		binary.LittleEndian.PutUint32(out[offset:], value)
	}
	putI64 := func(offset int, value int64) {
		binary.LittleEndian.PutUint64(out[offset:], uint64(value))
	}
	putU64(statDev, input.Dev)
	putU64(statIno, input.Ino)
	putU32(statMode, input.Mode)
	putU32(statNLink, input.NLink)
	putU32(statUID, input.UID)
	putU32(statGID, input.GID)
	putU64(statRDev, input.RDev)
	putI64(statSize, input.Size)
	binary.LittleEndian.PutUint32(out[statBlksize:], uint32(uint64(input.Blksize)&0xFFFFFFFF))
	putI64(statBlocks, input.Blocks)
	putI64(statAtime, input.AtimeSec)
	putI64(statAtimeNsec, input.AtimeNsec)
	putI64(statMtime, input.MtimeSec)
	putI64(statMtimeNsec, input.MtimeNsec)
	putI64(statCtime, input.CtimeSec)
	putI64(statCtimeNsec, input.CtimeNsec)
	return out
}

// encodeIovec 把缓冲区描述符编码为 struct iovec{base, len}(16 字节)。
func encodeIovec(base, length uint64) [16]byte {
	var out [16]byte
	binary.LittleEndian.PutUint64(out[0:], base)
	binary.LittleEndian.PutUint64(out[8:], length)
	return out
}
