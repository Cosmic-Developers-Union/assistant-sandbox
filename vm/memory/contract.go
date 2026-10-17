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
