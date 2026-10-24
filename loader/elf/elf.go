package elf

import (
	"fmt"
	"sort"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

const (
	magicNumber       = 0x464C457F
	classELF64        = 2
	dataLittleEndian  = 1
	versionCurrent    = 1
	typeExecutable    = 2
	typeShared        = 3
	machineRISCV      = 243
	headerSize        = 64
	programHeaderSize = 56

	programLoad     = 1
	programDynamic  = 2
	programInterp   = 3
	programTLS      = 7
	programGNUStack = 0x6474E551

	flagExec  = 1
	flagWrite = 2
	flagRead  = 4

	riscvFloatABIMask   = 0x6
	riscvFloatABISoft   = 0
	riscvFloatABISingle = 0x2
	riscvFloatABIDouble = 0x4
	riscvCompressed     = 0x1
)

const (
	PageSize = memory.PageSize

	DefaultStackSize = 1 << 20

	MaxStackSize = 64 << 20

	DefaultStackTop = 1 << 47

	DefaultMaxImageBytes = 64 << 20

	maxSegments = 64
)

type Options struct {
	Argv []string
	Env  []string

	ExecPath string

	Random [16]byte

	LoadBias uint64

	StackSize uint64

	StackTop uint64

	MaxImageBytes uint64
}

type Segment struct {
	Region memory.Region

	Data []byte

	Address uint64

	FileOffset uint64

	Filesz uint64
	Memsz  uint64

	Align uint64
}

type Stack struct {
	Region memory.Region

	SP uint64

	ContentBase uint64

	Content []byte
}

type Plan struct {
	Entry uint64

	Bias uint64

	Segments []Segment

	Stack Stack

	ProgramAddress uint64

	ProgramEntrySize uint64
	ProgramCount     uint64
}

func Load(raw []byte, options Options) (*Plan, error) {
	normalized, err := options.normalize()
	if err != nil {
		return nil, err
	}
	image, err := loadImage(raw, normalized)
	if err != nil {
		return nil, err
	}
	stack, err := buildStack(normalized, auxFacts{
		entry:            image.entry,
		programAddress:   image.programAddress,
		programEntrySize: image.programEntrySize,
		programCount:     image.programCount,
	})
	if err != nil {
		return nil, err
	}
	return &Plan{
		Entry:            image.entry,
		Bias:             normalized.LoadBias,
		Segments:         image.segments,
		Stack:            stack,
		ProgramAddress:   image.programAddress,
		ProgramEntrySize: image.programEntrySize,
		ProgramCount:     image.programCount,
	}, nil
}

func (o Options) normalize() (Options, error) {
	if o.StackSize == 0 {
		o.StackSize = DefaultStackSize
	}
	if o.StackSize > MaxStackSize {
		return Options{}, limit(fmt.Sprintf("初始栈 %d 字节超过上限 %d", o.StackSize, MaxStackSize))
	}
	if o.StackTop == 0 {
		o.StackTop = DefaultStackTop
	}
	if o.StackTop%PageSize != 0 {
		return Options{}, invalid(fmt.Sprintf("栈顶 %#x 未按页对齐", o.StackTop))
	}
	if o.StackTop < o.StackSize {
		return Options{}, invalid("栈顶小于栈大小")
	}
	if o.MaxImageBytes == 0 {
		o.MaxImageBytes = DefaultMaxImageBytes
	}
	if o.LoadBias%PageSize != 0 {
		return Options{}, invalid(fmt.Sprintf("重定位基址 %#x 未按页对齐", o.LoadBias))
	}
	if o.ExecPath == "" && len(o.Argv) > 0 {
		o.ExecPath = o.Argv[0]
	}
	return o, nil
}

type loadedImage struct {
	entry            uint64
	segments         []Segment
	programAddress   uint64
	programEntrySize uint64
	programCount     uint64
}

func loadImage(raw []byte, options Options) (*loadedImage, error) {
	r := reader{raw: raw}
	head, err := parseHeader(r)
	if err != nil {
		return nil, err
	}
	bias := uint64(0)
	if head.typeID == typeShared {
		bias = options.LoadBias
	} else if options.LoadBias != 0 {

		return nil, invalid("可执行映像不接受重定位基址")
	}
	entry, ok := addOverflow(head.entry, bias)
	if !ok {
		return nil, overflow("入口地址溢出")
	}
	segments, err := collectSegments(r, head, bias, options.MaxImageBytes)
	if err != nil {
		return nil, err
	}
	if err := checkEntry(entry, segments); err != nil {
		return nil, err
	}
	programAddress, err := locateProgramHeaders(head, segments)
	if err != nil {
		return nil, err
	}
	return &loadedImage{
		entry:            entry,
		segments:         segments,
		programAddress:   programAddress,
		programEntrySize: uint64(head.entrySize),
		programCount:     uint64(head.count),
	}, nil
}

func collectSegments(r reader, head header, bias, budget uint64) ([]Segment, error) {
	if uint64(head.count) > maxSegments {
		return nil, limit(fmt.Sprintf("程序头 %d 个超过上限 %d", head.count, maxSegments))
	}

	if _, err := r.slice(head.programAt, uint64(head.count)*uint64(head.entrySize)); err != nil {
		return nil, err
	}
	segments := make([]Segment, 0, head.count)
	var mapped uint64
	for index := uint64(0); index < uint64(head.count); index++ {
		program := r.programHeaderAt(head.programAt, uint64(head.entrySize), index)
		switch program.typeID {
		case programLoad:
		case programInterp:
			return nil, notSupported("动态链接（PT_INTERP）未实现")
		case programDynamic:
			return nil, notSupported("动态链接（PT_DYNAMIC）未实现")
		case programTLS:
			return nil, notSupported("线程局部存储（PT_TLS）未实现")
		case programGNUStack:
			if program.flags&flagExec != 0 {
				return nil, invalid("映像请求可执行栈")
			}
			continue
		default:

			continue
		}

		if program.memsz == 0 {
			continue
		}
		segment, err := buildSegment(r, program, bias)
		if err != nil {
			return nil, err
		}
		for _, existing := range segments {
			if existing.Region.Overlaps(segment.Region) {
				return nil, invalid(fmt.Sprintf(
					"段 %#x 与段 %#x 在页边界上重叠：同一页被两个段复用需要页复用语义，本装载器不支持",
					segment.Region.Start, existing.Region.Start))
			}
		}

		size := segment.Region.Size()
		if size > budget-mapped {
			return nil, limit(fmt.Sprintf("可装载 %d 字节超过上限 %d", mapped+size, budget))
		}
		mapped += size
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return nil, malformed("没有可装载的 PT_LOAD 段")
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].Region.Start < segments[j].Region.Start })
	return segments, nil
}

func buildSegment(r reader, program programHeader, bias uint64) (Segment, error) {
	if program.filesz > program.memsz {
		return Segment{}, invalid(fmt.Sprintf("段文件大小 %#x 大于内存大小 %#x", program.filesz, program.memsz))
	}
	if program.align > 1 {
		if !isPowerOfTwo(program.align) {
			return Segment{}, invalid(fmt.Sprintf("段对齐 %#x 不是 2 的幂", program.align))
		}

		if program.offset%program.align != program.vaddr%program.align {
			return Segment{}, invalid(fmt.Sprintf(
				"段地址 %#x 与文件偏移 %#x 对对齐 %#x 不同余", program.vaddr, program.offset, program.align))
		}
	}
	data, err := r.slice(program.offset, program.filesz)
	if err != nil {
		return Segment{}, err
	}
	address, ok := addOverflow(program.vaddr, bias)
	if !ok {
		return Segment{}, overflow("段地址溢出")
	}
	end, ok := addOverflow(address, program.memsz)
	if !ok {
		return Segment{}, overflow("段结束地址溢出")
	}
	mappedEnd, ok := alignUp(end, PageSize)
	if !ok {
		return Segment{}, overflow("段映射结束地址溢出")
	}
	prot, err := protection(program.flags)
	if err != nil {
		return Segment{}, err
	}
	return Segment{
		Region:     memory.Region{Start: alignDown(address, PageSize), End: mappedEnd, Prot: prot, Name: "image"},
		Data:       append([]byte(nil), data...),
		Address:    address,
		FileOffset: program.offset,
		Filesz:     program.filesz,
		Memsz:      program.memsz,
		Align:      program.align,
	}, nil
}

func protection(flags uint32) (memory.Prot, error) {
	var prot memory.Prot
	if flags&flagRead != 0 {
		prot |= memory.ProtRead
	}
	if flags&flagWrite != 0 {
		prot |= memory.ProtWrite
	}
	if flags&flagExec != 0 {
		prot |= memory.ProtExec
	}
	if prot == 0 {
		return 0, invalid("段没有声明任何权限")
	}
	if prot.WritableAndExecutable() {
		return 0, invalid("段同时可写可执行（W^X）")
	}
	return prot, nil
}

func checkEntry(entry uint64, segments []Segment) error {
	for _, segment := range segments {

		end := segment.Address + segment.Memsz
		if entry < segment.Address || entry >= end {
			continue
		}
		if !segment.Region.Prot.CanExec() {
			return invalid(fmt.Sprintf("入口 %#x 落在不可执行的段里", entry))
		}
		return nil
	}
	return invalid(fmt.Sprintf("入口 %#x 不在任何可装载段内", entry))
}

func locateProgramHeaders(head header, segments []Segment) (uint64, error) {

	length := uint64(head.count) * uint64(head.entrySize)
	tableEnd := head.programAt + length
	for _, segment := range segments {

		fileEnd := segment.FileOffset + segment.Filesz
		if head.programAt >= segment.FileOffset && tableEnd <= fileEnd {
			return segment.Address + (head.programAt - segment.FileOffset), nil
		}
	}
	return 0, invalid("程序头表不在任何可装载段内")
}
