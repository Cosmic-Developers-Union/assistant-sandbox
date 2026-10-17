package deployment

type Support uint8

const (
	Unsupported Support = iota

	Partial

	Supported
)

func (s Support) String() string {
	switch s {
	case Supported:
		return "supported"
	case Partial:
		return "partial"
	default:
		return "unsupported"
	}
}

type Matrix struct {
	Name string

	Deploy string

	Executors map[string]Support

	Signals Support

	TTY Support

	Snapshot Support

	Replay Support

	Network Support

	AddressSpace Support

	Notes []string
}
