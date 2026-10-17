package elf

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

const (
	imageCodeBase   = 0x10000
	imageDataBase   = 0x12000
	imageDataOffset = 0x2000
	imageCodeSize   = 0x1000
	imageEntry      = 0x10100
	imagePhdrOffset = 0x40
)

func minimalImage() []byte {
	raw := make([]byte, imageDataOffset+0x10)
	copy(raw[0:4], "\x7fELF")
	raw[offIdentClass] = classELF64
	raw[offIdentData] = dataLittleEndian
	raw[offIdentVersion] = versionCurrent
	binary.LittleEndian.PutUint16(raw[offType:], typeExecutable)
	binary.LittleEndian.PutUint16(raw[offMachine:], machineRISCV)
	binary.LittleEndian.PutUint32(raw[offVersion:], versionCurrent)
	binary.LittleEndian.PutUint64(raw[offEntry:], imageEntry)
	binary.LittleEndian.PutUint64(raw[offProgramOffset:], imagePhdrOffset)
	binary.LittleEndian.PutUint32(raw[offFlags:], riscvFloatABISoft)
	binary.LittleEndian.PutUint16(raw[offHeaderSize:], headerSize)
	binary.LittleEndian.PutUint16(raw[offProgramEntry:], programHeaderSize)
	binary.LittleEndian.PutUint16(raw[offProgramCount:], 2)
	putProgramHeader(raw[imagePhdrOffset:], programHeader{
		typeID: programLoad, flags: flagRead | flagExec,
		offset: 0, vaddr: imageCodeBase, filesz: imageCodeSize, memsz: imageCodeSize, align: PageSize,
	})
	putProgramHeader(raw[imagePhdrOffset+programHeaderSize:], programHeader{
		typeID: programLoad, flags: flagRead | flagWrite,
		offset: imageDataOffset, vaddr: imageDataBase, filesz: 0x10, memsz: 0x100, align: PageSize,
	})

	for index := 0; index < 16; index++ {
		raw[0x200+index] = byte(0x40 + index)
		raw[imageDataOffset+index] = byte(0x80 + index)
	}
	return raw
}

func putProgramHeader(dst []byte, program programHeader) {
	binary.LittleEndian.PutUint32(dst[phOffType:], program.typeID)
	binary.LittleEndian.PutUint32(dst[phOffFlags:], program.flags)
	binary.LittleEndian.PutUint64(dst[phOffOffset:], program.offset)
	binary.LittleEndian.PutUint64(dst[phOffVaddr:], program.vaddr)
	binary.LittleEndian.PutUint64(dst[phOffFilesz:], program.filesz)
	binary.LittleEndian.PutUint64(dst[phOffMemsz:], program.memsz)
	binary.LittleEndian.PutUint64(dst[phOffAlign:], program.align)
}

func patchProgramHeader(t *testing.T, raw []byte, index int, patch func(*programHeader)) {
	t.Helper()
	base := imagePhdrOffset + index*programHeaderSize
	program := programHeader{
		typeID: binary.LittleEndian.Uint32(raw[base+phOffType:]),
		flags:  binary.LittleEndian.Uint32(raw[base+phOffFlags:]),
		offset: binary.LittleEndian.Uint64(raw[base+phOffOffset:]),
		vaddr:  binary.LittleEndian.Uint64(raw[base+phOffVaddr:]),
		filesz: binary.LittleEndian.Uint64(raw[base+phOffFilesz:]),
		memsz:  binary.LittleEndian.Uint64(raw[base+phOffMemsz:]),
		align:  binary.LittleEndian.Uint64(raw[base+phOffAlign:]),
	}
	patch(&program)
	putProgramHeader(raw[base:], program)
}

type decodedStack struct {
	argc   uint64
	argv   []string
	env    []string
	auxv   [][2]uint64
	random []byte
}

func decodeStack(t *testing.T, stack Stack) decodedStack {
	t.Helper()
	read64 := func(address uint64) uint64 {
		t.Helper()
		if address < stack.ContentBase || address+8 > stack.ContentBase+uint64(len(stack.Content)) {
			t.Fatalf("读取 %#x 超出栈内容 [%#x,%#x)", address, stack.ContentBase, stack.ContentBase+uint64(len(stack.Content)))
		}
		return binary.LittleEndian.Uint64(stack.Content[address-stack.ContentBase:])
	}
	readString := func(address uint64) string {
		t.Helper()
		if address < stack.ContentBase {
			t.Fatalf("字符串地址 %#x 低于栈内容起点 %#x", address, stack.ContentBase)
		}
		raw := stack.Content[address-stack.ContentBase:]
		end := 0
		for end < len(raw) && raw[end] != 0 {
			end++
		}
		if end == len(raw) {
			t.Fatalf("字符串 %#x 没有结尾", address)
		}
		return string(raw[:end])
	}
	decoded := decodedStack{argc: read64(stack.SP)}
	cursor := stack.SP + 8
	for index := uint64(0); index < decoded.argc; index++ {
		decoded.argv = append(decoded.argv, readString(read64(cursor)))
		cursor += 8
	}
	cursor += 8
	for read64(cursor) != 0 {
		decoded.env = append(decoded.env, readString(read64(cursor)))
		cursor += 8
	}
	cursor += 8
	for {
		pair := [2]uint64{read64(cursor), read64(cursor + 8)}
		cursor += 16
		if pair[0] == atNull {
			break
		}
		decoded.auxv = append(decoded.auxv, pair)
	}
	for _, pair := range decoded.auxv {
		if pair[0] == atRandom {
			decoded.random = append([]byte(nil), stack.Content[pair[1]-stack.ContentBase:pair[1]-stack.ContentBase+16]...)
		}
	}
	return decoded
}

func findAux(auxv [][2]uint64, kind uint64) (uint64, bool) {
	for _, pair := range auxv {
		if pair[0] == kind {
			return pair[1], true
		}
	}
	return 0, false
}

func TestLoadMinimalImage(t *testing.T) {
	raw := minimalImage()
	plan, err := Load(raw, Options{Argv: []string{"prog", "-x"}, Env: []string{"A=1"}, ExecPath: "prog"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Entry != imageEntry || plan.Bias != 0 {
		t.Fatalf("入口=%#x 基址=%#x", plan.Entry, plan.Bias)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("段数=%d", len(plan.Segments))
	}
	code, data := plan.Segments[0], plan.Segments[1]
	if code.Region != (memory.Region{Start: imageCodeBase, End: imageCodeBase + imageCodeSize, Prot: memory.ProtRead | memory.ProtExec, Name: "image"}) {
		t.Fatalf("代码段=%+v", code.Region)
	}
	if data.Region != (memory.Region{Start: imageDataBase, End: imageDataBase + PageSize, Prot: memory.ProtRead | memory.ProtWrite, Name: "image"}) {
		t.Fatalf("数据段=%+v", data.Region)
	}

	if data.Memsz != 0x100 || data.Filesz != 0x10 || data.Address != imageDataBase {
		t.Fatalf("数据段=%+v", data)
	}
	if got := code.Data[0x200:0x210]; got[0] != 0x40 {
		t.Fatalf("代码段内容未按文件偏移取: %x", got[:4])
	}

	raw[imageDataOffset] = 0xFF
	if data.Data[0] != 0x80 {
		t.Fatalf("段内容必须与输入缓冲区解耦: %#x", data.Data[0])
	}
	if plan.ProgramAddress != imageCodeBase+imagePhdrOffset {
		t.Fatalf("程序头地址=%#x", plan.ProgramAddress)
	}
	if plan.ProgramEntrySize != programHeaderSize || plan.ProgramCount != 2 {
		t.Fatalf("AT_PHENT=%d AT_PHNUM=%d", plan.ProgramEntrySize, plan.ProgramCount)
	}
	if plan.Stack.Region.Prot != memory.ProtRead|memory.ProtWrite || plan.Stack.Region.Name != "stack" {
		t.Fatalf("栈区间=%+v", plan.Stack.Region)
	}
	if plan.Stack.Region.End != DefaultStackTop || plan.Stack.Region.Size() != DefaultStackSize {
		t.Fatalf("栈区间=%+v", plan.Stack.Region)
	}
	if plan.Stack.SP%stackAlignment != 0 {
		t.Fatalf("SP=%#x 未 16 字节对齐", plan.Stack.SP)
	}
	if plan.Stack.ContentBase != alignDown(plan.Stack.SP, PageSize) {
		t.Fatalf("ContentBase=%#x SP=%#x", plan.Stack.ContentBase, plan.Stack.SP)
	}
	if plan.Stack.SP-plan.Stack.ContentBase >= PageSize {
		t.Fatalf("内容区 %#x 超过一页", plan.Stack.SP-plan.Stack.ContentBase)
	}
	if uint64(len(plan.Stack.Content)) != plan.Stack.Region.End-plan.Stack.ContentBase {
		t.Fatalf("内容长度=%d", len(plan.Stack.Content))
	}
}

func TestStackLayout(t *testing.T) {
	random := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	plan, err := Load(minimalImage(), Options{
		Argv:     []string{"prog", "arg"},
		Env:      []string{"A=1", "B=2"},
		ExecPath: "/bin/prog",
		Random:   random,
	})
	if err != nil {
		t.Fatal(err)
	}
	stack := decodeStack(t, plan.Stack)
	if stack.argc != 2 || !reflect.DeepEqual(stack.argv, []string{"prog", "arg"}) {
		t.Fatalf("argv=%v argc=%d", stack.argv, stack.argc)
	}
	if !reflect.DeepEqual(stack.env, []string{"A=1", "B=2"}) {
		t.Fatalf("envp=%v", stack.env)
	}
	if !reflect.DeepEqual(stack.random, random[:]) {
		t.Fatalf("AT_RANDOM 内容=%x", stack.random)
	}
	for kind, want := range map[uint64]uint64{
		atPhdr:   plan.ProgramAddress,
		atPhent:  plan.ProgramEntrySize,
		atPhnum:  plan.ProgramCount,
		atPagesz: PageSize,
		atEntry:  plan.Entry,
		atBase:   0,
		atSecure: 0,
		atClkTCK: clockTicksPerSecond,
		atUID:    0,
		atGID:    0,
	} {
		got, ok := findAux(stack.auxv, kind)
		if !ok {
			t.Fatalf("auxv 缺少类型 %d", kind)
		}
		if got != want {
			t.Errorf("auxv[%d]=%#x want %#x", kind, got, want)
		}
	}
	execfn, ok := findAux(stack.auxv, atExecFN)
	if !ok {
		t.Fatal("auxv 缺少 AT_EXECFN")
	}

	if address := execfn; address < plan.Stack.SP || address >= plan.Stack.Region.End {
		t.Fatalf("AT_EXECFN 地址 %#x 不在栈内", address)
	}
	if got := string(plan.Stack.Content[execfn-plan.Stack.ContentBase:][:len("/bin/prog")]); got != "/bin/prog" {
		t.Fatalf("AT_EXECFN 指向 %q", got)
	}
}

func TestStackAlignmentAcrossCounts(t *testing.T) {
	for _, tc := range []struct {
		argv, env int
	}{
		{0, 0}, {1, 0}, {2, 0}, {0, 1}, {1, 1}, {2, 1}, {3, 2}, {5, 4},
	} {
		options := Options{StackSize: 1 << 16}
		for index := 0; index < tc.argv; index++ {
			options.Argv = append(options.Argv, strings.Repeat("a", index+1))
		}
		for index := 0; index < tc.env; index++ {
			options.Env = append(options.Env, strings.Repeat("e", index+1)+"=1")
		}
		plan, err := Load(minimalImage(), options)
		if err != nil {
			t.Fatalf("argv=%d env=%d: %v", tc.argv, tc.env, err)
		}
		if plan.Stack.SP%stackAlignment != 0 {
			t.Fatalf("argv=%d env=%d: SP=%#x 未对齐", tc.argv, tc.env, plan.Stack.SP)
		}
		if plan.Stack.Region.Size() != options.StackSize {
			t.Fatalf("argv=%d env=%d: 栈大小=%d", tc.argv, tc.env, plan.Stack.Region.Size())
		}

		decoded := decodeStack(t, plan.Stack)
		if int(decoded.argc) != tc.argv || len(decoded.env) != tc.env {
			t.Fatalf("argv=%d env=%d: 解析得到 argc=%d env=%d", tc.argv, tc.env, decoded.argc, len(decoded.env))
		}
	}
}

func TestLoadDeterministic(t *testing.T) {
	options := Options{Argv: []string{"prog"}, Env: []string{"A=1"}, StackSize: 1 << 16}
	first, err := Load(minimalImage(), options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(minimalImage(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("同一输入必须得到相同计划")
	}
}

func TestLoadSharedObject(t *testing.T) {
	const bias = 0x400000
	raw := minimalImage()
	binary.LittleEndian.PutUint16(raw[offType:], typeShared)
	plan, err := Load(raw, Options{LoadBias: bias, Argv: []string{"prog"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Entry != imageEntry+bias || plan.Bias != bias {
		t.Fatalf("入口=%#x 基址=%#x", plan.Entry, plan.Bias)
	}
	if plan.Segments[0].Region.Start != imageCodeBase+bias {
		t.Fatalf("段起始=%#x", plan.Segments[0].Region.Start)
	}
	if plan.ProgramAddress != imageCodeBase+imagePhdrOffset+bias {
		t.Fatalf("程序头地址=%#x", plan.ProgramAddress)
	}

	if _, err := Load(minimalImage(), Options{LoadBias: bias, Argv: []string{"prog"}}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("可执行映像带基址应报 EINVAL: %v", err)
	}
}

func TestEmptySegmentIsSkipped(t *testing.T) {
	raw := minimalImage()
	patchProgramHeader(t, raw, 1, func(program *programHeader) {
		program.filesz, program.memsz = 0, 0
	})
	plan, err := Load(raw, Options{Argv: []string{"prog"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 1 {
		t.Fatalf("段数=%d", len(plan.Segments))
	}
}

func TestLoadRejections(t *testing.T) {
	type testCase struct {
		label    string
		want     error
		detail   string
		truncate int
		mutate   func(t *testing.T, raw []byte)
		options  func(*Options)
	}
	for _, tc := range []testCase{
		{
			label: "魔数不对", want: errno.NoExec, detail: "魔数",
			mutate: func(t *testing.T, raw []byte) { raw[0] = 0 },
		},
		{
			label: "ELF32", want: errno.NotSupported, detail: "ELF64",
			mutate: func(t *testing.T, raw []byte) { raw[offIdentClass] = 1 },
		},
		{
			label: "大端", want: errno.NotSupported, detail: "小端",
			mutate: func(t *testing.T, raw []byte) { raw[offIdentData] = 2 },
		},
		{
			label: "e_ident 版本不对", want: errno.NoExec, detail: "e_ident",
			mutate: func(t *testing.T, raw []byte) { raw[offIdentVersion] = 2 },
		},
		{
			label: "e_version 不对", want: errno.NoExec, detail: "e_version",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint32(raw[offVersion:], 2) },
		},
		{
			label: "重定位目标文件", want: errno.NotSupported, detail: "e_type",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint16(raw[offType:], 1) },
		},
		{
			label: "非 RISC-V", want: errno.NotSupported, detail: "RISC-V",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint16(raw[offMachine:], 62) },
		},
		{
			label: "要求压缩指令", want: errno.NotSupported, detail: "压缩",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint32(raw[offFlags:], 1) },
		},
		{
			label: "浮点 ABI", want: errno.NotSupported, detail: "浮点",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint32(raw[offFlags:], 4) },
		},
		{
			label: "程序头项大小不对", want: errno.NoExec, detail: "e_phentsize",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint16(raw[offProgramEntry:], 55) },
		},
		{
			label: "没有程序头", want: errno.NoExec, detail: "程序头",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint16(raw[offProgramCount:], 0) },
		},
		{
			label: "映像截断", want: errno.NoExec, detail: "超出映像",
			truncate: 40,
		},
		{
			label: "程序头表越界", want: errno.NoExec, detail: "超出映像",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint64(raw[offProgramOffset:], uint64(len(raw)))
			},
		},
		{
			label: "程序头表偏移溢出", want: errno.NoExec, detail: "超出映像",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint64(raw[offProgramOffset:], ^uint64(0)) },
		},
		{
			label: "程序头数超上限", want: errno.Limit, detail: "超过上限",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint16(raw[offProgramCount:], 100) },
		},
		{
			label: "文件大小大于内存大小", want: errno.Invalid, detail: "大于内存大小",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) { program.filesz = program.memsz + 1 })
			},
		},
		{
			label: "段对齐不是 2 的幂", want: errno.Invalid, detail: "2 的幂",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) { program.align = 3 })
			},
		},
		{
			label: "段地址与偏移不同余", want: errno.Invalid, detail: "不同余",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) { program.vaddr = imageCodeBase + 0x10 })
			},
		},
		{
			label: "段文件区间越界", want: errno.NoExec, detail: "超出映像",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 1, func(program *programHeader) {
					program.filesz, program.memsz = 0x100, 0x100
				})
			},
		},
		{
			label: "段地址溢出", want: errno.Overflow, detail: "段地址溢出",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint16(raw[offType:], typeShared)
				binary.LittleEndian.PutUint64(raw[offEntry:], 0)
			},
			options: func(options *Options) { options.LoadBias = ^uint64(0) - (PageSize - 1) },
		},
		{
			label: "段映射结束溢出", want: errno.Overflow, detail: "溢出",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) {
					program.offset, program.vaddr, program.filesz, program.memsz = 0, ^uint64(0)-0xFFF, 0, 0xFF0
				})
			},
		},
		{
			label: "段在页内重叠", want: errno.Invalid, detail: "重叠",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 1, func(program *programHeader) {
					program.offset, program.vaddr = 0xF00, imageCodeBase+0xF00
				})
			},
		},
		{
			label: "段同时可写可执行", want: errno.Invalid, detail: "W^X",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) {
					program.flags = flagRead | flagWrite | flagExec
				})
			},
		},
		{
			label: "段没有权限", want: errno.Invalid, detail: "权限",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) { program.flags = 0 })
			},
		},
		{
			label: "入口落在不可执行段", want: errno.Invalid, detail: "不可执行",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint64(raw[offEntry:], imageDataBase)
			},
		},
		{
			label: "入口不在任何段内", want: errno.Invalid, detail: "不在任何可装载段",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint64(raw[offEntry:], 0x90000)
			},
		},
		{
			label: "可执行栈", want: errno.Invalid, detail: "可执行栈",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 1, func(program *programHeader) {
					program.typeID, program.flags = programGNUStack, flagRead|flagExec
				})
			},
		},
		{
			label: "动态链接解释器", want: errno.NotSupported, detail: "PT_INTERP",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 1, func(program *programHeader) { program.typeID = programInterp })
			},
		},
		{
			label: "动态段", want: errno.NotSupported, detail: "PT_DYNAMIC",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 1, func(program *programHeader) { program.typeID = programDynamic })
			},
		},
		{
			label: "TLS 段", want: errno.NotSupported, detail: "PT_TLS",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 1, func(program *programHeader) { program.typeID = programTLS })
			},
		},
		{
			label: "没有可装载段", want: errno.NoExec, detail: "没有可装载",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) { program.typeID = 4 })
				patchProgramHeader(t, raw, 1, func(program *programHeader) { program.typeID = 4 })
			},
		},
		{
			label: "程序头表不在可装载段内", want: errno.Invalid, detail: "程序头表",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) {
					program.offset, program.vaddr = 0x1000, 0x11000
				})
				binary.LittleEndian.PutUint64(raw[offEntry:], 0x11100)
			},
		},
		{
			label: "映像超过字节预算", want: errno.Limit, detail: "超过上限",
			mutate:  func(t *testing.T, raw []byte) {},
			options: func(options *Options) { options.MaxImageBytes = 0x1000 },
		},
		{
			label: "栈顶未页对齐", want: errno.Invalid, detail: "栈顶",
			mutate:  func(t *testing.T, raw []byte) {},
			options: func(options *Options) { options.StackTop = DefaultStackTop + 1 },
		},
		{
			label: "栈顶小于栈大小", want: errno.Invalid, detail: "栈顶小于",
			mutate:  func(t *testing.T, raw []byte) {},
			options: func(options *Options) { options.StackTop = PageSize; options.StackSize = PageSize * 4 },
		},
		{
			label: "栈超过上限", want: errno.Limit, detail: "超过上限",
			mutate:  func(t *testing.T, raw []byte) {},
			options: func(options *Options) { options.StackSize = MaxStackSize + PageSize },
		},
		{
			label: "栈装不下参数", want: errno.Limit, detail: "超过栈大小",
			mutate: func(t *testing.T, raw []byte) {},
			options: func(options *Options) {
				options.StackSize = PageSize
				options.Argv = []string{strings.Repeat("x", 4096)}
			},
		},
		{
			label: "基址未页对齐", want: errno.Invalid, detail: "重定位基址",
			mutate:  func(t *testing.T, raw []byte) {},
			options: func(options *Options) { options.LoadBias = PageSize + 1 },
		},
		{
			label: "e_ehsize 不是 64", want: errno.NoExec, detail: "e_ehsize",
			mutate: func(t *testing.T, raw []byte) { binary.LittleEndian.PutUint16(raw[offHeaderSize:], 63) },
		},
		{
			label: "头部只读到程序头项大小", want: errno.NoExec, detail: "超出映像",
			truncate: 53,
		},
		{
			label: "头部短于版本字段", want: errno.NoExec, detail: "超出映像",
			truncate: 8,
		},
		{
			label: "程序头被截断在字段中间", want: errno.NoExec, detail: "超出映像",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint64(raw[offProgramOffset:], uint64(len(raw))-44)
			},
		},
		{
			label: "程序头只剩开头", want: errno.NoExec, detail: "超出映像",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint64(raw[offProgramOffset:], uint64(len(raw))-2)
			},
		},
		{
			label: "第二段超过字节预算", want: errno.Limit, detail: "超过上限",
			options: func(options *Options) { options.MaxImageBytes = imageCodeSize + PageSize/2 },
		},
		{
			label: "段结束地址溢出", want: errno.Overflow, detail: "段结束地址溢出",
			mutate: func(t *testing.T, raw []byte) {
				patchProgramHeader(t, raw, 0, func(program *programHeader) {
					program.offset, program.vaddr, program.filesz, program.memsz = 0, ^uint64(0)-PageSize+1, 0, 2*PageSize
				})
			},
		},
		{
			label: "入口地址溢出", want: errno.Overflow, detail: "入口地址溢出",
			mutate: func(t *testing.T, raw []byte) {
				binary.LittleEndian.PutUint16(raw[offType:], typeShared)
				binary.LittleEndian.PutUint64(raw[offEntry:], PageSize)
			},
			options: func(options *Options) { options.LoadBias = ^uint64(0) - (PageSize - 1) },
		},
		{
			label: "栈顶被字符串占满", want: errno.Limit, detail: "装不下",
			options: func(options *Options) {
				options.StackTop, options.StackSize = PageSize, PageSize
				options.ExecPath = "-"
				options.Argv = []string{strings.Repeat("x", int(PageSize)-96)}
			},
		},
		{
			label: "栈顶下方装不下参数", want: errno.Limit, detail: "装不下",
			options: func(options *Options) {
				options.StackTop, options.StackSize = 2*PageSize, PageSize
				options.Argv = []string{strings.Repeat("x", 3*int(PageSize))}
			},
		},
	} {
		t.Run(tc.label, func(t *testing.T) {
			raw := minimalImage()
			if tc.mutate != nil {
				tc.mutate(t, raw)
			}
			if tc.truncate > 0 {
				raw = raw[:tc.truncate]
			}
			options := Options{Argv: []string{"prog"}}
			if tc.options != nil {
				tc.options(&options)
			}
			_, err := Load(raw, options)
			if !errors.Is(err, tc.want) {
				t.Fatalf("错误=%v 应为 %v", err, tc.want)
			}
			if !strings.Contains(err.Error(), tc.detail) {
				t.Fatalf("错误 %q 应包含 %q", err, tc.detail)
			}
		})
	}
}

func TestLoadRejectsEmptyImage(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, {0x7F}, {0x7F, 'E', 'L', 'F'}} {
		if _, err := Load(raw, Options{Argv: []string{"prog"}}); !errors.Is(err, errno.NoExec) {
			t.Fatalf("%d 字节映像应报 ENOEXEC: %v", len(raw), err)
		}
	}
}

func TestLoadKeepsAddressSpaceBoundsSeparate(t *testing.T) {

	const base uint64 = ^uint64(0) - 2*PageSize + 1
	raw := minimalImage()
	patchProgramHeader(t, raw, 0, func(program *programHeader) {
		program.offset, program.vaddr = 0, base
	})
	binary.LittleEndian.PutUint64(raw[offEntry:], base+0x100)
	plan, err := Load(raw, Options{Argv: []string{"prog"}})
	if err != nil {
		t.Fatal(err)
	}

	high := plan.Segments[len(plan.Segments)-1]
	if high.Region.Start != base {
		t.Fatalf("段起始=%#x want %#x", high.Region.Start, base)
	}
	if high.Region.End != base+PageSize {
		t.Fatalf("段结束=%#x", high.Region.End)
	}
	if !high.Region.Contains(plan.Entry) {
		t.Fatalf("入口 %#x 不在段 %+v 内", plan.Entry, high.Region)
	}
	if plan.Entry != base+0x100 {
		t.Fatalf("入口=%#x", plan.Entry)
	}
}

func TestProgramTableInLaterSegment(t *testing.T) {
	raw := minimalImage()

	patchProgramHeader(t, raw, 0, func(program *programHeader) {
		program.offset, program.vaddr = 0x1000, 0x11000
	})
	patchProgramHeader(t, raw, 1, func(program *programHeader) {
		program.offset, program.vaddr, program.filesz, program.memsz = 0, 0x12000, 0x1000, 0x1000
	})
	binary.LittleEndian.PutUint64(raw[offEntry:], 0x11100)
	plan, err := Load(raw, Options{Argv: []string{"prog"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProgramAddress != 0x12000+imagePhdrOffset {
		t.Fatalf("程序头地址=%#x", plan.ProgramAddress)
	}
}

func TestNonExecutableStackIsAccepted(t *testing.T) {
	raw := minimalImage()
	patchProgramHeader(t, raw, 1, func(program *programHeader) {
		program.typeID, program.flags = programGNUStack, flagRead|flagWrite
	})
	plan, err := Load(raw, Options{Argv: []string{"prog"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 1 {
		t.Fatalf("段数=%d", len(plan.Segments))
	}
}
