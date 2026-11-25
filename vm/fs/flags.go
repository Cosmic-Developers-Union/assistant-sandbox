package fs

const (
	ReadOnly  uint32 = 0
	WriteOnly uint32 = 1
	ReadWrite uint32 = 2
	Append    uint32 = 1024
	Create    uint32 = 64
	Exclusive uint32 = 128
	Truncate  uint32 = 512
	NonBlock  uint32 = 2048
)
