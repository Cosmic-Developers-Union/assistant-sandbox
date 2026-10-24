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

// asm-generic struct statx 的字段偏移;缓冲区按现代内核与 glibc/musl 的
// 256 字节布局,尾部保留字段为零。
const (
	statxMask            = 0x00
	statxBlksize         = 0x04
	statxAttributes      = 0x08
	statxNLink           = 0x10
	statxUID             = 0x14
	statxGID             = 0x18
	statxMode            = 0x1c
	statxIno             = 0x20
	statxSize            = 0x28
	statxBlocks          = 0x30
	statxAttributesMask  = 0x38
	statxAtime           = 0x40
	statxBtime           = 0x50
	statxCtime           = 0x60
	statxMtime           = 0x70
	statxRDevMajor       = 0x80
	statxRDevMinor       = 0x84
	statxDevMajor        = 0x88
	statxDevMinor        = 0x8c
	statxSizeTotal       = 0x100
	statxTimestampLayout = 16 // tv_sec i64 + tv_nsec u32 + reserved i32
)

// asm-generic 的 STATX_* 请求位;本实现填充 BASIC_STATS 全部字段。
const (
	statxMaskType   = 0x001
	statxMaskMode   = 0x002
	statxMaskNLink  = 0x004
	statxMaskUID    = 0x008
	statxMaskGID    = 0x010
	statxMaskAtime  = 0x020
	statxMaskMtime  = 0x040
	statxMaskCtime  = 0x080
	statxMaskIno    = 0x100
	statxMaskSize   = 0x200
	statxMaskBlocks = 0x400

	statxBasicStats = statxMaskType | statxMaskMode | statxMaskNLink | statxMaskUID |
		statxMaskGID | statxMaskAtime | statxMaskMtime | statxMaskCtime |
		statxMaskIno | statxMaskSize | statxMaskBlocks
)

// encodeStatx 把客体文件元数据编码为 struct statx;
// 返回掩码为请求掩码与实际填充字段(BASIC_STATS)的交集。
func encodeStatx(input StatInput, requested uint32) [statxSizeTotal]byte {
	var out [statxSizeTotal]byte
	putU32 := func(offset int, value uint32) {
		binary.LittleEndian.PutUint32(out[offset:], value)
	}
	putU64 := func(offset int, value uint64) {
		binary.LittleEndian.PutUint64(out[offset:], value)
	}
	putTimestamp := func(offset int, seconds, nanoseconds int64) {
		binary.LittleEndian.PutUint64(out[offset:], uint64(seconds))
		binary.LittleEndian.PutUint32(out[offset+8:], uint32(nanoseconds))
	}
	putU32(statxMask, requested&statxBasicStats)
	putU32(statxBlksize, uint32(uint64(input.Blksize)&0xFFFFFFFF))
	putU32(statxNLink, input.NLink)
	putU32(statxUID, input.UID)
	putU32(statxGID, input.GID)
	binary.LittleEndian.PutUint16(out[statxMode:], uint16(input.Mode))
	putU64(statxIno, input.Ino)
	putU64(statxSize, uint64(input.Size))
	putU64(statxBlocks, uint64(input.Blocks))
	putTimestamp(statxAtime, input.AtimeSec, input.AtimeNsec)
	putTimestamp(statxCtime, input.CtimeSec, input.CtimeNsec)
	putTimestamp(statxMtime, input.MtimeSec, input.MtimeNsec)
	return out
}
