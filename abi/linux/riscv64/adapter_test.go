package riscv64

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	isa "github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu/riscv64"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

type stubRegisters struct {
	values    map[int]uint64
	fail      error
	writeFail error
}

func newStubRegisters() *stubRegisters { return &stubRegisters{values: map[int]uint64{}} }

func (r *stubRegisters) ReadRegister(index int) (uint64, error) {
	if r.fail != nil {
		return 0, r.fail
	}
	return r.values[index], nil
}

func (r *stubRegisters) WriteRegister(index int, value uint64) error {
	if r.writeFail != nil {
		return r.writeFail
	}
	r.values[index] = value
	return nil
}

type argumentFailRegisters struct{ err error }

func (r argumentFailRegisters) ReadRegister(index int) (uint64, error) {
	if index == regNumber {
		return 0, nil
	}
	return 0, r.err
}

func (argumentFailRegisters) WriteRegister(int, uint64) error { return nil }

type handlerFunc func(context.Context, common.Call) common.Reply

func (f handlerFunc) Handle(ctx context.Context, call common.Call) common.Reply { return f(ctx, call) }

func ecall() cpu.Trap {
	return cpu.Trap{Kind: cpu.TrapEcall, Address: 0x1000, Instruction: ecallInstruction}
}

func TestDispatchExitDecodesSyscall(t *testing.T) {
	registers := newStubRegisters()
	registers.values[regNumber] = numberExit
	registers.values[regArgument0] = 7
	adapter := &Adapter{Handler: common.ExitOnly{}, Registers: registers}
	exit, err := adapter.Dispatch(context.Background(), ecall())
	if err != nil {
		t.Fatal(err)
	}
	if !exit.Requested || exit.Code != 7 {
		t.Fatalf("exit=%+v want 7", exit)
	}
	if registers.values[regReturn] != 7 {
		t.Fatalf("退出不应改写返回值寄存器: %#x", registers.values[regReturn])
	}
}

func TestDispatchExitGroup(t *testing.T) {
	registers := newStubRegisters()
	registers.values[regNumber] = numberExitGroup
	registers.values[regArgument0] = 3
	adapter := &Adapter{Handler: common.ExitOnly{}, Registers: registers}
	exit, err := adapter.Dispatch(context.Background(), ecall())
	if err != nil || !exit.Requested || exit.Code != 3 {
		t.Fatalf("exit=%+v err=%v", exit, err)
	}
}

func TestDispatchUnknownSyscallWritesENOSYS(t *testing.T) {
	registers := newStubRegisters()
	registers.values[regNumber] = 999
	adapter := &Adapter{Handler: common.ExitOnly{}, Registers: registers}
	exit, err := adapter.Dispatch(context.Background(), ecall())
	if err != nil {
		t.Fatal(err)
	}
	if exit.Requested {
		t.Fatal("未知调用不应终止进程")
	}
	number, _ := errno.Linux(errno.NotImplemented)
	if got := registers.values[regReturn]; got != uint64(-int64(number)) {
		t.Fatalf("a0=%#x want -ENOSYS", got)
	}
}

func TestDispatchWritesHandlerValue(t *testing.T) {
	registers := newStubRegisters()
	registers.values[regNumber] = 12345
	handler := handlerFunc(func(_ context.Context, call common.Call) common.Reply {
		if call.Number != 12345 {
			t.Errorf("原始调用号=%d", call.Number)
		}
		return common.Reply{Value: 42}
	})
	adapter := &Adapter{Handler: handler, Registers: registers}
	if _, err := adapter.Dispatch(context.Background(), ecall()); err != nil {
		t.Fatal(err)
	}
	if registers.values[regReturn] != 42 {
		t.Fatalf("a0=%d want 42", registers.values[regReturn])
	}
}

func TestDispatchDecodesArguments(t *testing.T) {
	registers := newStubRegisters()
	registers.values[regNumber] = numberExit
	for index := 0; index < argumentCount; index++ {
		registers.values[regArgument0+index] = uint64(100 + index)
	}
	handler := handlerFunc(func(_ context.Context, call common.Call) common.Reply {
		if call.Syscall != common.SysExit {
			t.Errorf("语义调用=%v", call.Syscall)
		}
		for index := 0; index < argumentCount; index++ {
			if call.Args[index] != uint64(100+index) {
				t.Errorf("参数[%d]=%d", index, call.Args[index])
			}
		}
		return common.Reply{Exit: true, Value: call.Args[0]}
	})
	adapter := &Adapter{Handler: handler, Registers: registers}
	exit, err := adapter.Dispatch(context.Background(), ecall())
	if err != nil || !exit.Requested || exit.Code != 100 {
		t.Fatalf("exit=%+v err=%v", exit, err)
	}
}

func TestDispatchFaultTrapsAreNotSyscalls(t *testing.T) {
	registers := newStubRegisters()
	adapter := &Adapter{Handler: common.ExitOnly{}, Registers: registers}
	for _, trap := range []cpu.Trap{
		{Kind: cpu.TrapBreakpoint, Address: 0x1000, Instruction: 0x00100073},
		{Kind: cpu.TrapIllegalInstruction, Address: 0x1004},
		{Kind: cpu.TrapMemoryFault, Address: 0x2000, Cause: errno.Range},

		{Kind: cpu.TrapEcall, Address: 0x1008, Instruction: 0x00100073},
	} {
		_, err := adapter.Dispatch(context.Background(), trap)
		var fault common.Fault
		if !errors.As(err, &fault) {
			t.Fatalf("%v: err=%v want Fault", trap.Kind, err)
		}
		if fault.Trap.Kind != trap.Kind {
			t.Fatalf("故障类别=%v want %v", fault.Trap.Kind, trap.Kind)
		}
	}
}

func TestDispatchPropagatesPortErrors(t *testing.T) {
	broken := newStubRegisters()
	broken.fail = errno.Range
	adapter := &Adapter{Handler: common.ExitOnly{}, Registers: broken}
	if _, err := adapter.Dispatch(context.Background(), ecall()); !errors.Is(err, errno.Range) {
		t.Fatalf("读取失败: %v want ERANGE", err)
	}

	adapter = &Adapter{Handler: common.ExitOnly{}, Registers: argumentFailRegisters{err: errno.Range}}
	if _, err := adapter.Dispatch(context.Background(), ecall()); !errors.Is(err, errno.Range) {
		t.Fatalf("参数读取失败: %v want ERANGE", err)
	}
	readOnly := newStubRegisters()
	readOnly.values[regNumber] = 999
	readOnly.writeFail = errno.ReadOnlyFS
	adapter = &Adapter{Handler: common.ExitOnly{}, Registers: readOnly}
	if _, err := adapter.Dispatch(context.Background(), ecall()); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("写入失败: %v want EROFS", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	adapter = &Adapter{Handler: common.ExitOnly{}, Registers: newStubRegisters()}
	if _, err := adapter.Dispatch(cancelled, ecall()); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消: %v", err)
	}
}

func TestDispatchValidation(t *testing.T) {
	for _, adapter := range []*Adapter{{}, {Handler: common.ExitOnly{}}, {Registers: newStubRegisters()}} {
		if _, err := adapter.Dispatch(context.Background(), ecall()); !errors.Is(err, errno.Invalid) {
			t.Fatalf("err=%v want EINVAL", err)
		}
	}
}

func TestSyscallNumberMapping(t *testing.T) {
	for number, want := range map[uint64]common.Syscall{
		numberOpenAt: common.SysOpenAt, numberClose: common.SysClose, numberLseek: common.SysLseek,
		numberRead: common.SysRead, numberWrite: common.SysWrite,
		numberExit: common.SysExit, numberExitGroup: common.SysExitGroup,
		numberClockGettime: common.SysClockGettime, numberBrk: common.SysBrk,
		numberUname:  common.SysUname,
		numberGetPID: common.SysGetPID, numberGetPPID: common.SysGetPPID, numberGetTID: common.SysGetTID,
		numberGetUID: common.SysGetUID, numberGetEUID: common.SysGetEUID,
		numberGetGID: common.SysGetGID, numberGetEGID: common.SysGetEGID,
		numberAcct: common.SysAcct, numberPtrace: common.SysPtrace,
		numberMmap: common.SysMmap, numberMunmap: common.SysMunmap, numberMprotect: common.SysMprotect,
		numberNewfstatat: common.SysNewfstatat, numberFstat: common.SysFstat, numberStatx: common.SysStatx, numberUtimensat: common.SysUtimensat, numberStatfs: common.SysStatfs, numberFstatfs: common.SysFstatfs,
		numberSchedGetAffinity: common.SysSchedGetAffinity, numberSchedSetAffinity: common.SysSchedSetAffinity, numberGetCPU: common.SysGetCPU, numberWaitid: common.SysWaitid, numberDup: common.SysDup, numberRtSigaction: common.SysRtSigaction, numberRtSigprocmask: common.SysRtSigprocmask, numberRtSigreturn: common.SysRtSigreturn, numberSigaltstack: common.SysSigaltstack, numberExecve: common.SysExecve, numberKill: common.SysKill, numberTgkill: common.SysTgkill,
		numberClockNanosleep: common.SysClockNanosleep,
		999:                  common.SysUnknown,
	} {
		if got := syscallOf(number); got != want {
			t.Fatalf("%d=%v want %v", number, got, want)
		}
	}
}

func TestRunsExitProgramOnRealCore(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)

	if err := space.Map(ctx, memory.Region{
		Start: 0x1000, End: 0x2000, Prot: memory.ProtRead | memory.ProtWrite, Name: "code",
	}); err != nil {
		t.Fatal(err)
	}
	var code []byte
	for _, instruction := range []uint32{
		0x05D00893,
		0x00700513,
		0x00000073,
	} {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		code = append(code, encoded[:]...)
	}
	if err := space.Write(ctx, 0x1000, code); err != nil {
		t.Fatal(err)
	}
	if err := space.Protect(ctx, 0x1000, 0x2000, memory.ProtRead|memory.ProtExec); err != nil {
		t.Fatal(err)
	}
	core, err := isa.New(cpu.Config{Memory: space, Entry: 0x1000})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := common.NewRunner(common.Config{
		Core:       core,
		Dispatcher: &Adapter{Handler: common.ExitOnly{}, Registers: core},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status != 7 {
		t.Fatalf("退出状态=%d want 7", status)
	}
}

func TestRunsWriteAndExitProgram(t *testing.T) {
	ctx := context.Background()
	const base = 0x1000
	space := memory.New(0)
	if err := space.Map(ctx, memory.Region{
		Start: base, End: base + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite, Name: "image",
	}); err != nil {
		t.Fatal(err)
	}
	var code []byte
	for _, instruction := range []uint32{
		0x00100513,
		0x000015B7,
		0x08058593,
		0x00500613,
		0x04000893,
		0x00000073,
		0x05D00893,
		0x00000513,
		0x00000073,
	} {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		code = append(code, encoded[:]...)
	}
	if err := space.Write(ctx, base, code); err != nil {
		t.Fatal(err)
	}
	if err := space.Write(ctx, base+0x80, []byte("hello")); err != nil {
		t.Fatal(err)
	}

	if err := space.Protect(ctx, base, base+memory.PageSize, memory.ProtRead|memory.ProtExec); err != nil {
		t.Fatal(err)
	}
	output := &bufferStream{}
	const pid = 11
	table := fd.New()
	table.Attach(pid, fd.Streams{Output: output})
	core, err := isa.New(cpu.Config{Memory: space, Entry: base})
	if err != nil {
		t.Fatal(err)
	}
	handler := common.Syscalls{
		Memory:      space,
		Descriptors: common.ProcessDescriptors{Table: table, PID: pid},
	}
	runner, err := common.NewRunner(common.Config{
		Core:       core,
		Dispatcher: &Adapter{Handler: handler, Registers: core},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("退出状态=%d want 0", status)
	}
	if string(output.output) != "hello" {
		t.Fatalf("标准输出=%q want hello", output.output)
	}

	registers, err := core.ReadRegister(regReturn)
	if err != nil {
		t.Fatal(err)
	}
	if registers != 0 {
		t.Fatalf("退出时 a0=%d want 0（exit 状态）", registers)
	}
}

type bufferStream struct{ output []byte }

func (s *bufferStream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *bufferStream) Write(buffer []byte) (int, error) {
	s.output = append(s.output, buffer...)
	return len(buffer), nil
}

func (s *bufferStream) Close() error { return nil }

func TestOpenFlagTranslation(t *testing.T) {
	for _, tc := range []struct {
		label string
		raw   uint64
		want  *common.OpenFlags
	}{
		{"只读", 0, &common.OpenFlags{Mode: common.OpenRead}},
		{"只写加创建加截断", 1 | openCreate | openTruncate, &common.OpenFlags{Mode: common.OpenWrite, Create: true, Truncate: true}},
		{"读写加追加加 cloexec", 2 | openAppend | openCloseOnExec, &common.OpenFlags{Mode: common.OpenReadWrite, Append: true, CloseOnExec: true}},
		{"独占加创建", openCreate | openExclusive, &common.OpenFlags{Mode: common.OpenRead, Create: true, Exclusive: true}},
		{"O_DIRECTORY 未实现", 0o200000, nil},
		{"O_NOFOLLOW 未实现", 0o400000, nil},
	} {
		got := openFlags(tc.raw)
		switch {
		case (got == nil) != (tc.want == nil):
			t.Fatalf("%s: got=%+v want=%+v", tc.label, got, tc.want)
		case got != nil && *got != *tc.want:
			t.Fatalf("%s: got=%+v want=%+v", tc.label, got, tc.want)
		}
	}
}

func TestRunsOpenReadWriteProgram(t *testing.T) {
	ctx := context.Background()
	const base = 0x1000
	const dataPage = 0x2000
	space := memory.New(0)
	for _, region := range []memory.Region{
		{Start: base, End: base + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite, Name: "image"},
		{Start: dataPage, End: dataPage + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite, Name: "data"},
	} {
		if err := space.Map(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	var code []byte
	for _, instruction := range []uint32{
		0xF9C00513,
		0x000015B7,
		0x08058593,
		0x00000613,
		0x00000693,
		0x03800893,
		0x00000073,
		0x000025B7,
		0x00800613,
		0x03F00893,
		0x00000073,
		0x00100513,
		0x04000893,
		0x00000073,
		0x00000513,
		0x05D00893,
		0x00000073,
	} {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		code = append(code, encoded[:]...)
	}
	if err := space.Write(ctx, base, code); err != nil {
		t.Fatal(err)
	}
	if err := space.Write(ctx, base+0x80, append([]byte("/data.txt"), 0)); err != nil {
		t.Fatal(err)
	}
	if err := space.Protect(ctx, base, base+memory.PageSize, memory.ProtRead|memory.ProtExec); err != nil {
		t.Fatal(err)
	}
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(ctx, "/data.txt", vmfs.Create|1, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const pid = 21
	table := fd.New()
	output := &bufferStream{}
	table.Attach(pid, fd.Streams{Output: output})
	core, err := isa.New(cpu.Config{Memory: space, Entry: base})
	if err != nil {
		t.Fatal(err)
	}
	handler := common.Syscalls{
		Memory:      space,
		Descriptors: common.ProcessDescriptors{Table: table, PID: pid},
		Files:       common.ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"},
	}
	runner, err := common.NewRunner(common.Config{
		Core:       core,
		Dispatcher: &Adapter{Handler: handler, Registers: core},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0 {
		t.Fatalf("退出状态=%d want 0", status)
	}
	if string(output.output) != "12345678" {
		t.Fatalf("标准输出=%q want 12345678", output.output)
	}

	opened := table.Opened(pid)
	if len(opened) != 4 || opened[3] != 3 {
		t.Fatalf("退出后描述符=%v want [0 1 2 3]", opened)
	}
}

func TestRunsIdentityAndClockProgram(t *testing.T) {
	ctx := context.Background()
	const base = 0x1000
	const buffer = 0x2000
	const pid = 33
	space := memory.New(0)
	for _, region := range []memory.Region{
		{Start: base, End: base + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite, Name: "image"},
		{Start: buffer, End: buffer + memory.PageSize, Prot: memory.ProtRead | memory.ProtWrite, Name: "data"},
	} {
		if err := space.Map(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	var code []byte
	for _, instruction := range []uint32{
		0x00100513,
		0x000025B7,
		0x07100893,
		0x00000073,
		0x0AC00893,
		0x00000073,
		0x05D00893,
		0x00000073,
	} {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], instruction)
		code = append(code, encoded[:]...)
	}
	if err := space.Write(ctx, base, code); err != nil {
		t.Fatal(err)
	}
	if err := space.Protect(ctx, base, base+memory.PageSize, memory.ProtRead|memory.ProtExec); err != nil {
		t.Fatal(err)
	}
	core, err := isa.New(cpu.Config{Memory: space, Entry: base})
	if err != nil {
		t.Fatal(err)
	}
	source := &clock.Virtual{}
	handler := common.Syscalls{
		Memory:      space,
		Descriptors: common.ProcessDescriptors{Table: fd.New(), PID: pid},
		Clock:       source,
		Identity:    &common.Identity{PID: pid, PPID: 1},
	}
	runner, err := common.NewRunner(common.Config{
		Core:       core,
		Dispatcher: &Adapter{Handler: handler, Registers: core},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var encoded [16]byte
	if err := space.Read(ctx, buffer, encoded[:]); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(encoded[0:]); got != 0 {
		t.Fatalf("tv_sec=%d want 0", got)
	}
	if got := binary.LittleEndian.Uint64(encoded[8:]); got != 0 {
		t.Fatalf("tv_nsec=%d want 0", got)
	}
	if status != pid {
		t.Fatalf("退出状态=%d want pid=%d", status, pid)
	}
}

// TestRegistryMatchesAdapterMapping 验证 G01:编号、名称、分派与
// 适配器映射一一对应——登记项都有确定编号与稳定名称;适配器认识
// 但未实现的编号固定登记为 ENOSYS 清单;未知调用统一 ENOSYS。
func TestRegistryMatchesAdapterMapping(t *testing.T) {
	registered := common.Registered()
	if len(registered) == 0 {
		t.Fatal("登记表为空")
	}
	inverse := map[common.Syscall]uint64{}
	for number := uint64(0); number < 500; number++ {
		call := syscallOf(number)
		if call == common.SysUnknown {
			continue
		}
		if prev, ok := inverse[call]; ok {
			t.Fatalf("系统调用 %v 由编号 %d 与 %d 同时映射", call, prev, number)
		}
		inverse[call] = number
	}
	for _, call := range registered {
		if _, ok := inverse[call]; !ok {
			t.Fatalf("登记的 %v 没有适配器编号映射", call)
		}
		if name := call.String(); name == "" {
			t.Fatalf("登记的 %v 缺少稳定名称", call)
		}
	}
	// 适配器认识但未实现的编号:明确登记为 ENOSYS 清单(新增映射
	// 必须同时实现或显式进入本清单,防止伪成功)。
	unimplemented := map[string]bool{
		"accept4": true, "acct": true, "chroot": true, "close_range": true,
		"clone3":     true,
		"faccessat2": true, "fadvise64": true, "fallocate": true, "inotify_add_watch": true,
		"inotify_init1": true, "inotify_rm_watch": true, "kexec_load": true, "memfd_create": true,
		"mount": true, "openat2": true, "perf_event_open": true, "pidfd_getfd": true,
		"pidfd_open": true, "pivot_root": true,
		"ptrace": true, "reboot": true, "sync": true,
		"syslog":  true,
		"umount2": true, "userfaultfd": true,
	}
	for call := range inverse {
		name := call.String()
		if !common.IsRegistered(call) && !unimplemented[name] {
			t.Fatalf("适配器映射 %v 既未实现也未登记为 ENOSYS 清单", call)
		}
		if common.IsRegistered(call) && unimplemented[name] {
			t.Fatalf("%v 已实现,应从 ENOSYS 清单移除", call)
		}
	}
	// 未登记调用在 ABI 层统一 ENOSYS(包括已分配但未实现的编号)。
	unregistered := common.Syscall(0)
	for candidate := common.Syscall(1); candidate <= common.Syscall(500); candidate++ {
		if common.IsRegistered(candidate) {
			continue
		}
		unregistered = candidate
		break
	}
	handler := common.Syscalls{}
	if reply := handler.Handle(context.Background(), common.Call{Syscall: unregistered}); reply.Errno != errno.NotImplemented {
		t.Fatalf("未登记调用: %+v want ENOSYS", reply)
	}
	// 登记项全部可安全查询(零端口下不 panic,返回守卫错误)。
	for _, call := range registered {
		_ = handler.Handle(context.Background(), common.Call{Syscall: call})
	}
}
