package resources

import "context"

type Kind uint8

const (
	KindMemory Kind = iota

	KindWork

	KindDescriptors

	KindOutput

	KindFiles
)

func (k Kind) String() string {
	switch k {
	case KindMemory:
		return "memory"
	case KindWork:
		return "work"
	case KindDescriptors:
		return "descriptors"
	case KindOutput:
		return "output"
	case KindFiles:
		return "files"
	default:
		return "kind(" + itoa(uint64(k)) + ")"
	}
}

type View struct {
	MemoryLimit, MemoryUsed         uint64
	WorkLimit, WorkUsed             uint64
	DescriptorLimit, DescriptorUsed uint64
	OutputLimit, OutputUsed         uint64
	FileLimit, FileUsed             uint64
}

type Meter interface {
	View(context.Context, uint64) (View, error)
	Charge(context.Context, uint64, uint64) error

	ChargeKind(context.Context, uint64, Kind, uint64) error

	ReleaseKind(context.Context, uint64, Kind, uint64) error
}

func itoa(value uint64) string {
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
