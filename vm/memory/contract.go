package memory

import (
	"context"
	"errors"
)

const PageSize uint64 = 4096

type Prot uint8

const (
	ProtRead Prot = 1 << iota

	ProtWrite

	ProtExec
)

func (p Prot) String() string { return p.describe() }

func (p Prot) CanRead() bool  { return p&ProtRead != 0 }
func (p Prot) CanWrite() bool { return p&ProtWrite != 0 }
func (p Prot) CanExec() bool  { return p&ProtExec != 0 }

func (p Prot) WritableAndExecutable() bool { return p.CanWrite() && p.CanExec() }

func (p Prot) describe() string {
	if p == 0 {
		return "none"
	}
	out := make([]byte, 0, 3)
	for _, flag := range []struct {
		bit  Prot
		name byte
	}{{ProtRead, 'r'}, {ProtWrite, 'w'}, {ProtExec, 'x'}} {
		if p&flag.bit != 0 {
			out = append(out, flag.name)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

type Region struct {
	Start uint64
	End   uint64
	Prot  Prot

	Name string

	// Backend 非空时区间是文件共享映射:读写直通后备,不落本表
	// 页表(E09);为空时区间是匿名或文件私有映射,内容在本表页表
	// 中(私有文件映射的初始内容是映射时的文件快照,E08)。
	Backend Backend

	// FileBase 是区间首地址对应的后备偏移(mmap 的文件偏移)。
	FileBase uint64
}

// Backend 是文件映射区间的页后备:共享映射经此直读直写底层数据。
// 多个地址空间对同一文件区域的映射经由文件本身共享同一份数据,
// 写入即时可见且即时持久;ReadAt 越过数据末端以零填充缓冲
// (mmap 语义:越文件尾读取为零页)。
type Backend interface {
	ReadAt(offset uint64, buffer []byte) error

	WriteAt(offset uint64, buffer []byte) error
}

func (r Region) Size() uint64 { return r.End - r.Start }

func (r Region) Contains(address uint64) bool {
	return address >= r.Start && address < r.End
}

func (r Region) Overlaps(other Region) bool {
	return r.Start < other.End && other.Start < r.End
}

type Fault uint8

const (
	FaultUnmapped Fault = iota + 1

	FaultProtection

	FaultMisaligned

	FaultOverflow
)

func (f Fault) String() string {
	switch f {
	case FaultUnmapped:
		return "unmapped"
	case FaultProtection:
		return "protection"
	case FaultMisaligned:
		return "misaligned"
	case FaultOverflow:
		return "overflow"
	default:
		return "fault(" + utoa(uint64(f)) + ")"
	}
}

func (f Fault) Error() string { return string(f) }

type Space interface {
	Map(context.Context, Region) error

	Unmap(context.Context, uint64, uint64) error

	Protect(context.Context, uint64, uint64, Prot) error

	Check(context.Context, uint64, uint64, Prot) error

	Regions(context.Context) ([]Region, error)

	Account(context.Context) (Account, error)
}

type Account struct {
	Mapped uint64

	Regions int

	Limit uint64
}

func isFault(err error, identity error) bool { return errors.Is(err, identity) }

func utoa(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[index:])
}
