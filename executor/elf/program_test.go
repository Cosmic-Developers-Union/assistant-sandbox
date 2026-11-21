package elf

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	loaderelf "github.com/Cosmic-Developers-Union/assistant-sandbox/loader/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

const (
	imageCodeBase    = 0x10000
	imageDataBase    = 0x12000
	imageDataOffset  = 0x2000
	imageCodeSize    = 0x1000
	imageEntry       = 0x10100
	imageMessageAt   = 0x200
	imageProgramAt   = 0x100
	imagePathAt      = 0x300
	imagePhdrOffset  = 0x40
	imageHeaderSize  = 64
	imagePhdrSize    = 56
	imageProgramSize = 9
)

var instructions = []uint32{
	0x00100513,
	0x000105B7,
	0x20058593,
	0x00200613,
	0x04000893,
	0x00000073,
	0x00700513,
	0x05D00893,
	0x00000073,
}

var openReadProgram = []uint32{
	0xF9C00513,
	0x000105B7,
	0x30058593,
	0x00000613,
	0x00000693,
	0x03800893,
	0x00000073,
	0x000125B7,
	0x00200613,
	0x03F00893,
	0x00000073,
	0x00100513,
	0x000125B7,
	0x00200613,
	0x04000893,
	0x00000073,
	0x00700513,
	0x05D00893,
	0x00000073,
}

func buildImage(t *testing.T) []byte {
	t.Helper()
	return buildImageWith(t, instructions, "")
}

func buildImageWith(t *testing.T, program []uint32, path string) []byte {
	t.Helper()
	raw := make([]byte, imageDataOffset+0x10)
	copy(raw[0:4], "\x7fELF")
	raw[4] = 2
	raw[5] = 1
	raw[6] = 1
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], imageEntry)
	binary.LittleEndian.PutUint64(raw[32:], imagePhdrOffset)
	binary.LittleEndian.PutUint32(raw[48:], 0)
	binary.LittleEndian.PutUint16(raw[52:], imageHeaderSize)
	binary.LittleEndian.PutUint16(raw[54:], imagePhdrSize)
	binary.LittleEndian.PutUint16(raw[56:], 2)
	putProgramHeader(raw[imagePhdrOffset:], 1, 5, 0, imageCodeBase, imageCodeSize, imageCodeSize, 0x1000)
	putProgramHeader(raw[imagePhdrOffset+imagePhdrSize:], 1, 6, imageDataOffset, imageDataBase, 0x10, 0x100, 0x1000)
	copy(raw[imageMessageAt:], "hi")
	if path != "" {
		copy(raw[imagePathAt:], append([]byte(path), 0))
	}
	for index, instruction := range program {
		binary.LittleEndian.PutUint32(raw[imageProgramAt+index*4:], instruction)
	}
	return raw
}

func putProgramHeader(dst []byte, kind, flags uint32, offset, vaddr, filesz, memsz, align uint64) {
	binary.LittleEndian.PutUint32(dst[0:], kind)
	binary.LittleEndian.PutUint32(dst[4:], flags)
	binary.LittleEndian.PutUint64(dst[8:], offset)
	binary.LittleEndian.PutUint64(dst[16:], vaddr)
	binary.LittleEndian.PutUint64(dst[32:], filesz)
	binary.LittleEndian.PutUint64(dst[40:], memsz)
	binary.LittleEndian.PutUint64(dst[48:], align)
}

type stream struct{ output bytes.Buffer }

func (s *stream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *stream) Write(buffer []byte) (int, error) { return s.output.Write(buffer) }

func (s *stream) Close() error { return nil }

type fixedEntropy struct{}

func (fixedEntropy) Read(_ context.Context, buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(index)
	}
	return len(buffer), nil
}

var _ entropy.Source = fixedEntropy{}

type harness struct {
	process proc.Proc
	stdout  *stream
	system  vmfs.System
}

func newHarness(t *testing.T, image []byte, mutate func(*capability.Set)) *harness {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if image != nil {
		file, err := system.Open(context.Background(), "/program", vmfs.Create|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(image); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	const pid = 9
	table := fd.New()
	output := &stream{}
	table.Attach(pid, fd.Streams{Output: output})
	set := capability.Set{
		FS:       system,
		FD:       table,
		Clock:    &clock.Virtual{},
		Identity: identity.SingleUser{},
		Entropy:  fixedEntropy{},
	}
	if mutate != nil {
		mutate(&set)
	}
	return &harness{
		system: system,
		stdout: output,
		process: proc.Proc{
			ID: pid, Parent: 1, Program: "/program",
			Args: []string{"/program", "arg"}, Dir: "/",
			Env:       map[string]string{"B": "2", "A": "1"},
			Resources: set,
		},
	}
}

func (h *harness) run(t *testing.T) (uint8, error) {
	t.Helper()
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	return program.Run(context.Background(), h.process)
}

func TestRunsELFProgram(t *testing.T) {
	harness := newHarness(t, buildImage(t), nil)
	status, err := harness.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if status != 7 {
		t.Fatalf("退出状态=%d want 7", status)
	}
	if harness.stdout.output.String() != "hi" {
		t.Fatalf("标准输出=%q want hi", harness.stdout.output.String())
	}
}

func TestProgramImageErrors(t *testing.T) {

	missingFS := newHarness(t, buildImage(t), func(set *capability.Set) { set.FS = nil })
	if _, err := missingFS.run(t); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("缺文件系统=%v want ENOSYS", err)
	}

	missingFile := newHarness(t, nil, nil)
	if _, err := missingFile.run(t); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺程序=%v want ENOENT", err)
	}

	broken := newHarness(t, []byte("not an elf at all"), nil)
	if _, err := broken.run(t); !errors.Is(err, errno.NoExec) {
		t.Fatalf("畸形映像=%v want ENOEXEC", err)
	}
}

func TestFactoryValidation(t *testing.T) {
	if _, _, err := (Factory{}).New(""); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空路径=%v want EINVAL", err)
	}
	program, args, err := Factory{}.New("/bin/prog")
	if err != nil {
		t.Fatal(err)
	}
	if program == nil || len(args) != 1 || args[0] != "/bin/prog" {
		t.Fatalf("程序=%v 参数=%v", program, args)
	}
}

func TestArgvAndEnvironment(t *testing.T) {
	bare := proc.Proc{}
	if got := argv(bare, "/prog"); len(got) != 1 || got[0] != "/prog" {
		t.Fatalf("argv=%v", got)
	}
	withArgs := proc.Proc{Args: []string{"/prog", "a"}}
	if got := argv(withArgs, "/other"); len(got) != 2 || got[0] != "/prog" {
		t.Fatalf("argv=%v", got)
	}

	withArgs.Args[0] = "mutated"
	if got := argv(withArgs, "/other"); got[0] != "mutated" {
		t.Fatalf("argv 必须是副本: %v", got)
	}
	environment := environment(proc.Proc{Env: map[string]string{"B": "2", "A": "1"}})
	if len(environment) != 2 || environment[0] != "A=1" || environment[1] != "B=2" {
		t.Fatalf("环境=%v", environment)
	}
}

func TestCapabilitiesDegradeToNotImplemented(t *testing.T) {
	space := memory.New(0)
	handler := syscalls(context.Background(), proc.Proc{}, space, heapPlan(t), nil)
	if handler.Descriptors != nil {
		t.Fatal("没有描述符能力时不应装配描述符端口")
	}
	if handler.Clock != nil {
		t.Fatal("没有时钟能力时不应装配时钟端口")
	}
	write := common.Call{Syscall: common.SysWrite, Args: [6]uint64{1, 0x1000, 4}}
	if reply := handler.Handle(context.Background(), write); reply.Errno != errno.NotImplemented {
		t.Fatalf("缺端口写入=%+v want ENOSYS", reply)
	}
}

func TestAddressSpaceFollowsPlan(t *testing.T) {
	plan, err := loaderelf.Load(buildImage(t), loaderelf.Options{Argv: []string{"/program"}})
	if err != nil {
		t.Fatal(err)
	}
	space, err := addressSpace(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	message := make([]byte, 2)
	if err := space.Read(ctx, imageCodeBase+imageMessageAt, message); err != nil {
		t.Fatal(err)
	}
	if string(message) != "hi" {
		t.Fatalf("消息=%q", message)
	}

	if err := space.Write(ctx, imageCodeBase+imageMessageAt, []byte("no")); !errors.Is(err, errno.Permission) {
		t.Fatalf("写代码段=%v want EACCES", err)
	}
	regions, err := space.Regions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 段×2 + 栈 + 栈保护页(E13) + 信号返回蹭径页(K08)。
	if len(regions) != 5 {
		t.Fatalf("映射=%+v", regions)
	}
}

func TestMeterChargesTheRun(t *testing.T) {
	meters := &countingMeter{}
	harness := newHarness(t, buildImage(t), nil)
	harness.process.Resources = capability.Set{FS: harness.system, Resources: meters}
	if _, err := harness.run(t); err != nil {
		t.Fatal(err)
	}
	if meters.charged == 0 {
		t.Fatal("计量端口没有被调用")
	}
}

type countingMeter struct{ charged uint64 }

func (m *countingMeter) View(context.Context, uint64) (resources.View, error) {
	return resources.View{}, nil
}

func (m *countingMeter) Charge(_ context.Context, _ uint64, work uint64) error {
	m.charged += work
	return nil
}

func (m *countingMeter) ChargeKind(_ context.Context, _ uint64, _ resources.Kind, work uint64) error {
	m.charged += work
	return nil
}

func (m *countingMeter) ReleaseKind(context.Context, uint64, resources.Kind, uint64) error {
	return nil
}

var _ vmfs.System = (*memfs.FS)(nil)

type brokenEntropy struct{}

func (brokenEntropy) Read(context.Context, []byte) (int, error) { return 0, errno.Range }

func TestRandomBytesFollowsEntropy(t *testing.T) {
	ctx := context.Background()
	if got := randomBytesFor(ctx, proc.Proc{}); got != [randomBytes]byte{} {
		t.Fatalf("没有能力时=%v want 全零", got)
	}
	withEntropy := proc.Proc{Resources: capability.Set{Entropy: fixedEntropy{}}}
	got := randomBytesFor(ctx, withEntropy)
	for index, value := range got {
		if value != byte(index) {
			t.Fatalf("熵源字节[%d]=%d", index, value)
		}
	}
	broken := proc.Proc{Resources: capability.Set{Entropy: brokenEntropy{}}}
	if got := randomBytesFor(ctx, broken); got != [randomBytes]byte{} {
		t.Fatalf("熵源失败时=%v want 全零", got)
	}
}

func TestMetersOfReportsUnwired(t *testing.T) {
	if meters := metersOf(proc.Proc{}); meters != nil {
		t.Fatalf("没有能力时=%v want nil", meters)
	}
	if meters := metersOf(proc.Proc{Resources: capability.Set{}}); meters != nil {
		t.Fatalf("没有计量后端时=%v want nil", meters)
	}
	meters := &countingMeter{}
	if got := metersOf(proc.Proc{Resources: capability.Set{Resources: meters}}); got != resources.Meter(meters) {
		t.Fatalf("计量后端=%v want 注入的实例", got)
	}
}

func TestRunsWithoutOptionalCapabilities(t *testing.T) {
	harness := newHarness(t, buildImage(t), func(set *capability.Set) {
		set.FD, set.Clock, set.Identity, set.Entropy = nil, nil, nil, nil
	})
	status, err := harness.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if status != 7 {
		t.Fatalf("退出状态=%d want 7", status)
	}

	if harness.stdout.output.Len() != 0 {
		t.Fatalf("没有描述符能力却写出了 %q", harness.stdout.output.String())
	}
}

func TestAddressSpaceRejectsOutOfRangePlan(t *testing.T) {
	const highBase = 1 << 50
	raw := buildImage(t)

	binary.LittleEndian.PutUint64(raw[imagePhdrOffset+16:], highBase)
	binary.LittleEndian.PutUint64(raw[24:], highBase+imageProgramAt)
	plan, err := loaderelf.Load(raw, loaderelf.Options{Argv: []string{"/program"}})
	if err != nil {
		t.Fatalf("装载器应接受结构合法的映像: %v", err)
	}
	if _, err := addressSpace(context.Background(), plan); !errors.Is(err, errno.Invalid) {
		t.Fatalf("超出地址空间=%v want EINVAL", err)
	}
}

func TestRunsProgramThatOpensAndReadsFile(t *testing.T) {
	harness := newHarness(t, buildImageWith(t, openReadProgram, "/data.txt"), nil)
	ctx := context.Background()
	system, err := harness.process.Resources.Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(ctx, "/data.txt", vmfs.Create|vmfs.WriteOnly, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	harness.process.Dir = "/"
	status, err := harness.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if status != 7 {
		t.Fatalf("退出状态=%d want 7", status)
	}
	if harness.stdout.output.String() != "hi" {
		t.Fatalf("标准输出=%q want hi", harness.stdout.output.String())
	}
}

func TestFileSyscallsNeedInstallCapability(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}

	readOnly := proc.Proc{ID: 1, Resources: capability.Set{FS: system, FD: &readOnlyDescriptors{}}}
	plan := heapPlan(t)
	if handler := syscalls(ctx, readOnly, space, plan, nil); handler.Files != nil {
		t.Fatal("只读描述符视图下不应装配文件打开端口")
	}

	table := fd.New()
	table.Attach(1, fd.Streams{})
	process := proc.Proc{ID: 1, Dir: "/", Resources: capability.Set{FS: system, FD: table}}
	handler := syscalls(ctx, process, space, plan, nil)
	if handler.Files == nil {
		t.Fatal("完整描述符表下应装配文件打开端口")
	}

	bare := proc.Proc{ID: 1, Resources: capability.Set{FD: table}}
	if handler := syscalls(ctx, bare, space, plan, nil); handler.Files != nil {
		t.Fatal("没有文件系统时不应装配文件打开端口")
	}
}

type readOnlyDescriptors struct{}

func (readOnlyDescriptors) Get(context.Context, uint64, uint32) (fd.Stream, error) {
	return nil, errno.BadHandle
}

func (readOnlyDescriptors) Dup(context.Context, uint64, uint32, uint32) error { return errno.BadHandle }

func (readOnlyDescriptors) Close(context.Context, uint64, uint32) error { return errno.BadHandle }

func (readOnlyDescriptors) Terminal(context.Context, uint64, uint32) (fd.Terminal, error) {
	return fd.Terminal{}, errno.BadHandle
}

type recordingBinder struct {
	pid     uint64
	regions int
	bound   bool
}

func (b *recordingBinder) BindAddressSpace(pid uint64, inner *memory.Table) (memory.Space, error) {
	b.bound = true
	b.pid = pid
	if regions, err := inner.Regions(context.Background()); err == nil {
		b.regions = len(regions)
	}
	return memory.NewGuard(inner, nil), nil
}

func TestRunRegistersAddressSpace(t *testing.T) {
	binder := &recordingBinder{}
	harness := newHarness(t, buildImage(t), func(set *capability.Set) { set.Spaces = binder })
	if _, err := harness.run(t); err != nil {
		t.Fatal(err)
	}
	if !binder.bound {
		t.Fatal("地址空间没有登记到内核")
	}
	if binder.pid != uint64(harness.process.ID) {
		t.Fatalf("登记进程=%d want %d", binder.pid, harness.process.ID)
	}

	if binder.regions != 5 {
		t.Fatalf("登记映射数=%d want 5(含栈保护页与信号蹭径页)", binder.regions)
	}
}

type recordingMeter struct {
	charged map[resources.Kind]uint64
	fail    error
}

func (m *recordingMeter) record(kind resources.Kind, amount uint64) {
	if m.charged == nil {
		m.charged = map[resources.Kind]uint64{}
	}
	m.charged[kind] += amount
}

func (m *recordingMeter) View(context.Context, uint64) (resources.View, error) {
	return resources.View{}, nil
}

func (m *recordingMeter) Charge(_ context.Context, _ uint64, work uint64) error {
	if m.fail != nil {
		return m.fail
	}
	m.record(resources.KindWork, work)
	return nil
}

func (m *recordingMeter) ChargeKind(_ context.Context, _ uint64, kind resources.Kind, amount uint64) error {
	if m.fail != nil {
		return m.fail
	}
	m.record(kind, amount)
	return nil
}

func (m *recordingMeter) ReleaseKind(context.Context, uint64, resources.Kind, uint64) error {
	return nil
}

func TestRunChargesMemoryAndWork(t *testing.T) {
	meters := &recordingMeter{}
	harness := newHarness(t, buildImage(t), func(set *capability.Set) { set.Resources = meters })
	if _, err := harness.run(t); err != nil {
		t.Fatal(err)
	}

	if meters.charged[resources.KindMemory] < 2*4096 {
		t.Fatalf("内存扣减=%d want ≥%d", meters.charged[resources.KindMemory], 2*4096)
	}
	if meters.charged[resources.KindWork] == 0 {
		t.Fatal("指令数没有被扣减")
	}
}

func TestRunRejectsWhenMemoryBudgetIsExhausted(t *testing.T) {
	meters := &recordingMeter{fail: errno.Limit}
	binder := &recordingBinder{}
	harness := newHarness(t, buildImage(t), func(set *capability.Set) {
		set.Resources = meters
		set.Spaces = binder
	})
	if _, err := harness.run(t); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽=%v want EAGAIN", err)
	}
	if binder.bound {
		t.Fatal("预算不足却已经登记了地址空间")
	}
	if harness.stdout.output.Len() != 0 {
		t.Fatalf("预算不足却产生了输出: %q", harness.stdout.output.String())
	}
}

func heapPlan(t *testing.T) *loaderelf.Plan {
	t.Helper()
	plan, err := loaderelf.Load(buildImage(t), loaderelf.Options{Argv: []string{"/program"}})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestHeapGrowsAndShrinks(t *testing.T) {
	ctx := context.Background()
	plan := heapPlan(t)
	space, err := addressSpace(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	meters := &recordingMeter{}
	process := proc.Proc{ID: 3, Resources: capability.Set{Resources: meters}}
	heap := newHeap(space, process, plan)
	if heap == nil {
		t.Fatal("有可写段却没有堆")
	}
	start, err := heap.Break(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if start != heapStart(plan) || start%memory.PageSize != 0 {
		t.Fatalf("初始 break=%#x want %#x", start, heapStart(plan))
	}

	after, err := heap.Grow(ctx, start+1)
	if err != nil {
		t.Fatal(err)
	}
	if after != start+memory.PageSize {
		t.Fatalf("增长后 break=%#x want %#x", after, start+memory.PageSize)
	}
	if !hasRegion(t, space, "heap") {
		t.Fatalf("堆映射没有出现: %+v", regionNames(t, space))
	}
	if meters.charged[resources.KindMemory] != memory.PageSize {
		t.Fatalf("内存扣减=%d want %d", meters.charged[resources.KindMemory], memory.PageSize)
	}

	if back, err := heap.Grow(ctx, start); err != nil || back != start {
		t.Fatalf("收缩=%#x err=%v", back, err)
	}
	if hasRegion(t, space, "heap") {
		t.Fatalf("堆映射没有解除: %+v", regionNames(t, space))
	}

	if back, err := heap.Grow(ctx, 0x1000); err != nil || back != start {
		t.Fatalf("过度收缩=%#x err=%v want %#x", back, err, start)
	}
}

func TestHeapRefusesGrowthBeyondStack(t *testing.T) {
	ctx := context.Background()
	plan := heapPlan(t)
	space, err := addressSpace(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	heap := newHeap(space, proc.Proc{ID: 4}, plan)
	start, err := heap.Break(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after, err := heap.Grow(ctx, plan.Stack.Region.Start+memory.PageSize); err != nil || after != start {
		t.Fatalf("越过栈的请求=%#x err=%v want %#x", after, err, start)
	}
}

func hasRegion(t *testing.T, space *memory.Table, name string) bool {
	t.Helper()
	for _, region := range regionNames(t, space) {
		if region == name {
			return true
		}
	}
	return false
}

func regionNames(t *testing.T, space *memory.Table) []string {
	t.Helper()
	regions, err := space.Regions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(regions))
	for _, region := range regions {
		names = append(names, region.Name)
	}
	return names
}

var brkProgram = []uint32{
	0x00000513,
	0x0D600893,
	0x00000073,
	0x00050413,
	0x10000693,
	0x00D50533,
	0x0D600893,
	0x00000073,
	0x04100593,
	0x00B40023,
	0x00044503,
	0x05D00893,
	0x00000073,
}

func TestRunsProgramThatGrowsHeap(t *testing.T) {
	harness := newHarness(t, buildImageWith(t, brkProgram, ""), nil)
	status, err := harness.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if status != 0x41 {
		t.Fatalf("退出状态=%#x want 0x41（堆上新映射的字节）", status)
	}
}

func TestMapperPlacesAndReplacesAnonymous(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: 1 << 40}

	first, err := m.Anonymous(ctx, common.MapRequest{Length: 2 * memory.PageSize, Prot: common.ProtRead | common.ProtWrite})
	if err != nil {
		t.Fatal(err)
	}
	if first != mmapFloor {
		t.Fatalf("首个匿名映射应从 floor 开始: %#x want %#x", first, uint64(mmapFloor))
	}
	if err := space.Write(ctx, first, []byte("HELLO")); err != nil {
		t.Fatal(err)
	}

	second, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead})
	if err != nil {
		t.Fatal(err)
	}
	if second != first+2*memory.PageSize {
		t.Fatalf("第二个映射应紧随其后: %#x want %#x", second, first+2*memory.PageSize)
	}

	hinted, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead, Hint: mmapFloor + 8*memory.PageSize})
	if err != nil {
		t.Fatal(err)
	}
	if hinted != mmapFloor+8*memory.PageSize {
		t.Fatalf("空闲提示应被采纳: %#x", hinted)
	}

	blocked, err := m.Anonymous(ctx, common.MapRequest{Length: 3 * memory.PageSize, Prot: common.ProtRead, Hint: first})
	if err != nil {
		t.Fatal(err)
	}
	if blocked == first {
		t.Fatalf("被占用提示应改由内核选址: %#x", blocked)
	}
	if blocked != first+3*memory.PageSize {
		t.Fatalf("应跳过占用区间放到下一空隙: %#x want %#x", blocked, first+3*memory.PageSize)
	}
}

func TestMapperFixedAndNoReplace(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: 1 << 40}
	address, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead | common.ProtWrite})
	if err != nil {
		t.Fatal(err)
	}
	if err := space.Write(ctx, address, []byte("OLD")); err != nil {
		t.Fatal(err)
	}

	replaced, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead, Hint: address, Fixed: true})
	if err != nil {
		t.Fatal(err)
	}
	if replaced != address {
		t.Fatalf("MAP_FIXED 必须落在指定地址: %#x want %#x", replaced, address)
	}
	got := make([]byte, 3)
	if err := space.Read(ctx, address, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x00\x00\x00" {
		t.Fatalf("替换后旧数据应清零: %q", got)
	}

	if _, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead | common.ProtWrite, Hint: address, Fixed: true, NoReplace: true}); !errors.Is(err, errno.Exist) {
		t.Fatalf("NOREPLACE 命中应报 EEXIST: %v", err)
	}
	got = make([]byte, 1)
	if err := space.Read(ctx, address, got); err != nil {
		t.Fatalf("失败时原映射应保留: %v", err)
	}
}

func TestMapperValidatesProtAndBounds(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: mmapFloor + 4*memory.PageSize}

	if _, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead | 0x8}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知保护位应报 EINVAL: %v", err)
	}
	if _, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead | common.ProtWrite | common.ProtExec}); !errors.Is(err, errno.Permission) {
		t.Fatalf("W^X 组合应报 EACCES: %v", err)
	}
	if _, err := m.Anonymous(ctx, common.MapRequest{Length: 8 * memory.PageSize, Prot: common.ProtRead}); !errors.Is(err, errno.NoMemory) {
		t.Fatalf("超出 ceiling 应报 ENOMEM: %v", err)
	}

	freed := &mapper{table: space, ceiling: 1 << 40}
	address, err := freed.Anonymous(ctx, common.MapRequest{Length: 2 * memory.PageSize, Prot: common.ProtRead})
	if err != nil {
		t.Fatal(err)
	}
	if err := freed.Unmap(ctx, address+memory.PageSize, address+2*memory.PageSize); err != nil {
		t.Fatal(err)
	}
	if err := space.Check(ctx, address, 1, memory.ProtRead); err != nil {
		t.Fatalf("未解除的前半段应保留: %v", err)
	}
	if err := space.Check(ctx, address+memory.PageSize, 1, memory.ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("解除的后半段应未映射: %v", err)
	}
	if err := freed.Unmap(ctx, address+memory.PageSize, address+2*memory.PageSize); !errors.Is(err, errno.Range) {
		t.Fatalf("重复解除应报未映射: %v", err)
	}
}

func TestMapperMremapShrinksAndGrows(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: 1 << 40}
	start, err := m.Anonymous(ctx, common.MapRequest{Length: 4 * memory.PageSize, Prot: common.ProtRead | common.ProtWrite})
	if err != nil {
		t.Fatal(err)
	}
	if err := space.Write(ctx, start, []byte("DATA")); err != nil {
		t.Fatal(err)
	}

	// 原地扩张:尾部空闲时收缩前先验证扩张。
	grown, err := m.Mremap(ctx, start, start+4*memory.PageSize, start+5*memory.PageSize, false, false, 0)
	if err != nil || grown != start {
		t.Fatalf("原地扩张: addr=%#x err=%v", grown, err)
	}

	// 收缩:返回原地址,尾部释放。
	got, err := m.Mremap(ctx, start, start+5*memory.PageSize, start+2*memory.PageSize, false, false, 0)
	if err != nil || got != start {
		t.Fatalf("收缩: addr=%#x err=%v", got, err)
	}
	if err := space.Check(ctx, start, 4, common.ProtWrite); err != nil {
		t.Fatalf("收缩后头部应可写: %v", err)
	}
	if err := space.Check(ctx, start+3*memory.PageSize, 1, common.ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("收缩的尾部应未映射: %v", err)
	}

	// 尾部被新映射占用后,扩张必须迁移。
	if _, err := m.Anonymous(ctx, common.MapRequest{Length: memory.PageSize, Prot: common.ProtRead, Hint: start + 3*memory.PageSize}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Mremap(ctx, start, start+2*memory.PageSize, start+4*memory.PageSize, false, false, 0); !errors.Is(err, errno.NoMemory) {
		t.Fatalf("尾部被占用且无 MAYMOVE 应报 ENOMEM: %v", err)
	}

	// 子区间扩张会与同一映射的其余页重叠,Linux 报 ENOMEM。
	if _, err := m.Mremap(ctx, start+memory.PageSize, start+2*memory.PageSize, start+3*memory.PageSize, false, false, 0); !errors.Is(err, errno.NoMemory) {
		t.Fatalf("子区间扩张应报 ENOMEM: %v", err)
	}
	// 未映射的旧区间必须是 EFAULT,不能与内存不足混淆。
	if _, err := m.Mremap(ctx, 0x7000, 0x8000, 0x9000, false, false, 0); !errors.Is(err, errno.Fault) {
		t.Fatalf("未映射旧区间应报 EFAULT: %v", err)
	}

	// 子区间收缩只解除请求的尾部,保留其余映射。
	partial, err := m.Anonymous(ctx, common.MapRequest{Length: 4 * memory.PageSize, Prot: common.ProtRead | common.ProtWrite})
	if err != nil {
		t.Fatal(err)
	}
	shrunk, err := m.Mremap(ctx, partial+memory.PageSize, partial+3*memory.PageSize, partial+2*memory.PageSize, false, false, 0)
	if err != nil || shrunk != partial+memory.PageSize {
		t.Fatalf("子区间收缩: addr=%#x err=%v", shrunk, err)
	}
	if err := space.Check(ctx, partial, 1, common.ProtRead); err != nil {
		t.Fatalf("子区间收缩不得影响前部: %v", err)
	}
	if err := space.Check(ctx, partial+2*memory.PageSize, 1, common.ProtRead); !errors.Is(err, errno.Range) {
		t.Fatalf("子区间收缩的尾部应未映射: %v", err)
	}
	if err := space.Check(ctx, partial+3*memory.PageSize, 1, common.ProtRead); err != nil {
		t.Fatalf("子区间收缩不得影响后部: %v", err)
	}
}

func TestMapperMadviseDropsContents(t *testing.T) {
	ctx := context.Background()
	space := memory.New(0)
	m := &mapper{table: space, ceiling: 1 << 40}
	start, err := m.Anonymous(ctx, common.MapRequest{Length: 2 * memory.PageSize, Prot: common.ProtRead | common.ProtWrite})
	if err != nil {
		t.Fatal(err)
	}
	if err := space.Write(ctx, start, []byte("SECRET")); err != nil {
		t.Fatal(err)
	}
	if err := m.Madvise(ctx, start, start+2*memory.PageSize, common.MadvDontneed); err != nil {
		t.Fatalf("DONTNEED 应成功: %v", err)
	}
	if err := space.Check(ctx, start, 1, common.ProtWrite); err != nil {
		t.Fatalf("映射应保留: %v", err)
	}
	got := make([]byte, 6)
	if err := space.Read(ctx, start, got); err != nil {
		t.Fatal(err)
	}
	for _, b := range got {
		if b != 0 {
			t.Fatalf("丢弃后应零填充: %x", got)
		}
	}
	if err := m.Madvise(ctx, start, start+2*memory.PageSize, 0x40); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知 advice 应报 EINVAL: %v", err)
	}
	if err := m.Madvise(ctx, 9*memory.PageSize, 10*memory.PageSize, common.MadvDontneed); !errors.Is(err, errno.Range) {
		t.Fatalf("未映射范围应报 ENOMEM: %v", err)
	}
}

// O09:恶意客体故障——系统调用风暴、坏指针与非法指令不使宿主崩溃,
// 且取消与错误路径保持可用。
func TestHostileGuestProgramsStayContained(t *testing.T) {
	// 系统调用风暴:无限 getpid 循环,取消后立即退出且宿主可继续工作。
	storm := buildImageWith(t, []uint32{
		0x0AC00893, // li a7, 172 (getpid)
		0x00000073, // ecall
		0xFF9FF06F, // jal x0, -8
	}, "")
	harness := newHarness(t, storm, nil)
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := program.Run(ctx, harness.process)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后应返回错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("系统调用风暴未在取消后停止")
	}

	// 坏指针:从未映射地址读取 → 客体故障而非宿主崩溃。
	badPointer := buildImageWith(t, []uint32{
		0x000302B7, // lui t0, 0x30 → 0x30000(未映射)
		0x0002A503, // lw a0, 0(t0)
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
	}, "")
	faulted := newHarness(t, badPointer, nil)
	if _, err := faulted.run(t); err == nil {
		t.Fatal("坏指针应返回故障")
	}

	// 非法指令 → 客体故障而非宿主崩溃。
	illegal := buildImageWith(t, []uint32{
		0xFFFFFFFF, // 非法编码
	}, "")
	illegalHarness := newHarness(t, illegal, nil)
	if _, err := illegalHarness.run(t); err == nil {
		t.Fatal("非法指令应返回故障")
	}

	// 同一宿主继续运行正常程序,关闭能力未受影响。
	healthy := newHarness(t, buildImage(t), nil)
	if status, err := healthy.run(t); err != nil || status != 7 {
		t.Fatalf("宿主状态受损: status=%d err=%v", status, err)
	}
}

// sigControlStub 是登记 rt_sigaction/rt_sigprocmask 的进程控制替身,
// 同时满足 proc.Control 与 proc.SignalDispositionControl。
type sigControlStub struct {
	actions      map[signal.Number]signal.ActionConfig
	mask         uint64
	signal       signal.Number
	deliverAfter int
	pollMu       sync.Mutex
	polls        int
	delivered    bool
	alt          signal.AltStack
	childExited  chan proc.ChildState
	childID      proc.PID
	parentID     proc.PID
	parent       proc.Proc
}

func (s *sigControlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.Invalid
}

// Finish 记录子进程退出码并唤醒阻塞的 WaitChild。
func (s *sigControlStub) Finish(parent, id proc.PID, code uint8) error {
	s.childExited <- proc.ChildState{PID: id, ExitCode: code}
	return nil
}

// ForkControl:返回登记的子进程替身;地址空间由执行器兜底克隆。
func (s *sigControlStub) Fork() (proc.Proc, *memory.Table, error) {
	child := proc.Proc{
		ID: s.childID, Parent: s.parentID, Program: s.parent.Program,
		Resources: s.parent.Resources, Control: s,
	}
	return child, nil, nil
}

// WaitControl:阻塞等待子进程 Finish。
func (s *sigControlStub) WaitChild(ctx context.Context, target proc.PID, nohang bool) (proc.ChildState, bool, error) {
	if nohang {
		select {
		case state := <-s.childExited:
			return state, true, nil
		default:
			return proc.ChildState{}, false, nil
		}
	}
	select {
	case state := <-s.childExited:
		return state, true, nil
	case <-ctx.Done():
		return proc.ChildState{}, false, ctx.Err()
	}
}

func (s *sigControlStub) HasChildren() bool { return true }
func (s *sigControlStub) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.Invalid
}

func (s *sigControlStub) SetSignalAction(sig signal.Number, config signal.ActionConfig) error {
	if sig == signal.KILL || sig == signal.STOP {
		return errno.Invalid
	}
	s.actions[sig] = config
	return nil
}
func (s *sigControlStub) SignalAction(sig signal.Number) (signal.ActionConfig, error) {
	return s.actions[sig], nil
}
func (s *sigControlStub) SetSignalMask(how signal.MaskHow, set uint64) (uint64, error) {
	old := s.mask
	switch how {
	case signal.MaskBlock:
		s.mask |= set
	case signal.MaskUnblock:
		s.mask &^= set
	case signal.MaskSetMask:
		s.mask = set
	default:
		return 0, errno.Invalid
	}
	return old, nil
}
func (s *sigControlStub) SignalMask() (uint64, error) { return s.mask, nil }

// SignalDeliveryControl:边界投递轮询;deliverAfter 次轮询后可投递。
func (s *sigControlStub) PendingDeliverable() (signal.Number, signal.ActionConfig, bool) {
	// fork 后父子两个运行器并发轮询同一替身:polls/delivered 需原子访问。
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	s.polls++
	if s.delivered || s.polls < s.deliverAfter {
		return 0, signal.ActionConfig{}, false
	}
	config, ok := s.actions[s.signal]
	if !ok || config.Handler <= signal.SigIgnore {
		return 0, signal.ActionConfig{}, false
	}
	return s.signal, config, true
}

func (s *sigControlStub) ConsumeSignal(n signal.Number) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()
	s.delivered = true
}

func (s *sigControlStub) BlockForHandler(n signal.Number, handlerMask uint64, nodefer bool) (uint64, error) {
	old := s.mask
	s.mask |= handlerMask
	if !nodefer {
		s.mask |= uint64(1) << (n - 1)
	}
	return old, nil
}

func (s *sigControlStub) RestoreHandlerMask(mask uint64) error {
	s.mask = mask
	return nil
}

func (s *sigControlStub) AlternateStack() (signal.AltStack, bool) {
	return s.alt, s.alt.Present
}

func (s *sigControlStub) SetAlternateStack(stack signal.AltStack) error {
	s.alt = stack
	return nil
}

// buildSigactionImage 构造带 64 字节数据段的 ELF:数据区前 8 字节是
// sigset,偏移 0x10 起是 struct sigaction。
func buildSigactionImage(t *testing.T, program []uint32, set [8]byte, action [32]byte) []byte {
	t.Helper()
	// 数据段 0x100 字节:容纳 sigset、sigaction 与 waitid 的 siginfo。
	raw := make([]byte, imageDataOffset+0x100)
	copy(raw[0:4], "\x7fELF")
	raw[4] = 2
	raw[5] = 1
	raw[6] = 1
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], imageEntry)
	binary.LittleEndian.PutUint64(raw[32:], imagePhdrOffset)
	binary.LittleEndian.PutUint16(raw[52:], imageHeaderSize)
	binary.LittleEndian.PutUint16(raw[54:], imagePhdrSize)
	binary.LittleEndian.PutUint16(raw[56:], 2)
	putProgramHeader(raw[imagePhdrOffset:], 1, 5, 0, imageCodeBase, imageCodeSize, imageCodeSize, 0x1000)
	putProgramHeader(raw[imagePhdrOffset+imagePhdrSize:], 1, 6, imageDataOffset, imageDataBase, 0x100, 0x100, 0x1000)
	copy(raw[imageDataOffset:], set[:])
	copy(raw[imageDataOffset+0x10:], action[:])
	for index, instruction := range program {
		binary.LittleEndian.PutUint32(raw[imageProgramAt+index*4:], instruction)
	}
	return raw
}

func TestRealELFRegistersSignalDisposition(t *testing.T) {
	// 数据区:偏移 0 是 sigset(屏蔽 USR2=12),偏移 0x10 是 sigaction
	// (handler 0xCAFE、SA_SIGINFO|SA_RESTORER、restorer 0x9000)。
	var set [8]byte
	binary.LittleEndian.PutUint64(set[0:], 1<<(12-1))
	var action [32]byte
	binary.LittleEndian.PutUint64(action[0:], 0xCAFE)
	binary.LittleEndian.PutUint64(action[8:], 0x04000004) // SA_RESTORER|SA_SIGINFO
	binary.LittleEndian.PutUint64(action[16:], 1<<(10-1))
	binary.LittleEndian.PutUint64(action[24:], 0x9000)

	guestProgram := []uint32{
		0x00A00513, // li a0, 10 (SIGUSR1)
		0x000125B7, // lui a1, 0x12
		0x01058593, // addi a1, a1, 16 → act
		0x00000613, // li a2, 0 (oldact=NULL)
		0x00800693, // li a3, 8 (sigsetsize)
		0x08600893, // li a7, 134 (rt_sigaction)
		0x00000073, // ecall
		0x00000513, // li a0, 0 (SIG_BLOCK:asm-generic 为 0)
		0x000125B7, // lui a1, 0x12 → set
		0x00000613, // li a2, 0
		0x00800693, // li a3, 8
		0x08700893, // li a7, 135 (rt_sigprocmask)
		0x00000073, // ecall
		0x00700513, // li a0, 7
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
	}
	image := buildSigactionImage(t, guestProgram, set, action)
	harness := newHarness(t, image, nil)
	stub := &sigControlStub{actions: map[signal.Number]signal.ActionConfig{}}
	harness.process.Control = stub

	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := program.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if code != 7 {
		t.Fatalf("退出码=%d want 7", code)
	}
	// 真实 ELF 经完整栈(adaper→handler→端口)登记的处置与屏蔽。
	registered := stub.actions[signal.USR1]
	if registered.Handler != 0xCAFE || registered.Flags != 0x04000004 || registered.Restorer != 0x9000 || registered.Mask != 1<<9 {
		t.Fatalf("ELF 登记的处置=%+v", registered)
	}
	if stub.mask != 1<<(12-1) {
		t.Fatalf("ELF 登记的屏蔽=%x", stub.mask)
	}
}

// TestRealELFDeliversSignalHandlerAndReturns 用真实 RISC-V ELF 验证
// 信号帧投递全链路:登记处理器 → 边界投递(帧保存整数/浮点/屏蔽集)
// → 处理器写内存(0x12030) → rt_sigreturn 恢复原指令流 → 主程序读到计数格
// 并以之作为退出码(0x42)。
func TestRealELFDeliversSignalHandlerAndReturns(t *testing.T) {
	var set [8]byte
	binary.LittleEndian.PutUint64(set[0:], 0)
	main := []uint32{
		0x00A00513, // li a0, 10 (SIGUSR1)
		0x000125B7, // lui a1, 0x12
		0x01058593, // addi a1, a1, 16 → act
		0x00000613, // li a2, 0
		0x00800693, // li a3, 8
		0x08600893, // li a7, 134 (rt_sigaction)
		0x00000073, // ecall
		0x0AC00893, // li a7, 172 (getpid)
		0x00000073, // ecall ×12:边界轮询期间处理器被投递
		0x0AC00893,
		0x00000073,
		0x0AC00893,
		0x00000073,
		0x0AC00893,
		0x00000073,
		0x0AC00893,
		0x00000073,
		0x0AC00893,
		0x00000073,
		0x0AC00893,
		0x00000073,
		0x000123B7, // poll: lui t2, 0x12
		0x03038393, // addi t2, t2, 48 → 0x12030
		0x0003A283, // lw t0, 0(t2)
		0xFE028AE3, // beq t0, x0, poll(-12)
		0x00028513, // mv a0, t0
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
	}
	handler := []uint32{
		0x000123B7, // lui t2, 0x12 → 0x12000
		0x03038393, // addi t2, t2, 48 → 0x12030(计数格,数据段内)
		0x04200313, // li t1, 0x42
		0x0063A023, // sw t1, 0(t2)
		0x08B00893, // li a7, 139 (rt_sigreturn)
		0x00000073, // ecall
	}
	// 处理器地址 = 入口 + 主程序指令数 × 4。
	var action [32]byte
	binary.LittleEndian.PutUint64(action[0:], imageEntry+uint64(len(main))*4)
	binary.LittleEndian.PutUint64(action[8:], 0x04000004) // SA_RESTORER|SA_SIGINFO
	binary.LittleEndian.PutUint64(action[24:], 0x9000)
	image := buildSigactionImage(t, append(append([]uint32{}, main...), handler...), set, action)
	harness := newHarness(t, image, nil)
	stub := &sigControlStub{
		actions:      map[signal.Number]signal.ActionConfig{},
		signal:       signal.USR1,
		deliverAfter: 5,
	}
	harness.process.Control = stub

	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !stub.delivered {
		t.Fatal("处理器未投递")
	}
	// 退出码 = 处理器写入的计数格:证明投递、帧内执行与
	// rt_sigreturn 恢复全部发生。
	if code != 0x42 {
		t.Fatalf("退出码=%#x want 0x42(处理器写入经恢复后由主程序读出)", code)
	}
	// 屏蔽集在 rt_sigreturn 后恢复为帧内保存的投递前值。
	if stub.mask != 0 {
		t.Fatalf("恢复后屏蔽集=%x want 0", stub.mask)
	}
}

// TestRealELFForkParentChildReturnValues 用真实 RISC-V ELF 验证 fork
// 复制语义:clone(SIGCHLD) 后父进程拿到子 PID、子进程拿到 0;
// 子进程以 33 退出,父进程 waitid 读回 si_status 并以之退出。
func TestRealELFForkParentChildReturnValues(t *testing.T) {
	image := buildSigactionImage(t, []uint32{
		0x01100513, // li a0, 17 (SIGCHLD)
		0x00000593, // li a1, 0
		0x00000613, // li a2, 0
		0x00000693, // li a3, 0
		0x00000713, // li a4, 0
		0x0DC00893, // li a7, 220 (clone)
		0x00000073, // ecall
		0x02050663, // beq a0, x0, +44 → 子进程路径
		// 父进程:waitid(P_ALL, 0, infop, WEXITED)
		0x00000513, // li a0, 0
		0x00000513, // li a1, 0
		0x00012637, // lui a2, 0x12
		0x02060613, // addi a2, a2, 32 → infop 0x12020
		0x00100693, // li a3, 1 (WEXITED)
		0x05F00893, // li a7, 95 (waitid)
		0x00000073, // ecall
		0x01862283, // lw t0, 24(a2) → si_status
		0x00028513, // mv a0, t0
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
		// 子进程:以 33 退出
		0x02100513, // li a0, 33
		0x05D00893, // li a7, 93 (exit)
		0x00000073, // ecall
	}, [8]byte{}, [32]byte{})
	harness := newHarness(t, image, nil)
	stub := &sigControlStub{
		actions:     map[signal.Number]signal.ActionConfig{},
		childExited: make(chan proc.ChildState, 2),
		childID:     77,
		parentID:    9,
		parent:      harness.process,
	}
	harness.process.Control = stub

	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatal(runErr)
	}
	// 父进程从 waitid 的 siginfo 读到子进程退出码并作为自身退出码。
	if code != 33 {
		t.Fatalf("父进程退出码=%d want 33(经 waitid siginfo 传递)", code)
	}
}
