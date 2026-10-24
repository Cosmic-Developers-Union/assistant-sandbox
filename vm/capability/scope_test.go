package capability

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/ipc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/language"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
)

type failingMeter struct{ exhausted bool }

func (m *failingMeter) View(context.Context, uint64) (resources.View, error) {
	return resources.View{}, nil
}

func (m *failingMeter) Charge(context.Context, uint64, uint64) error {
	if m.exhausted {
		return errno.Limit
	}
	return nil
}

func (m *failingMeter) ChargeKind(ctx context.Context, pid uint64, kind resources.Kind, amount uint64) error {
	return m.Charge(ctx, pid, amount)
}

func (m *failingMeter) ReleaseKind(context.Context, uint64, resources.Kind, uint64) error {
	if m.exhausted {
		return errno.Limit
	}
	return nil
}

func newTestFS(t *testing.T) vmfs.System {
	t.Helper()
	base, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func TestScopePinsDescriptorsAndIdentity(t *testing.T) {
	table := fd.New()
	var out []byte
	table.Attach(7, fd.Streams{Output: writerFunc(func(p []byte) (int, error) {
		out = append(out, p...)
		return len(p), nil
	})})
	set := Set{
		FD:       table,
		Identity: identity.SingleUser{},
	}
	scoped := Scope(7, set, nil)

	descriptors, err := scoped.Descriptors()
	if err != nil {
		t.Fatal(err)
	}

	stream, err := descriptors.Get(context.Background(), 99, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("pinned")); err != nil {
		t.Fatal(err)
	}
	if string(out) != "pinned" {
		t.Fatalf("out=%q", out)
	}

	credentials, err := scoped.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Lookup(context.Background(), 8); !errors.Is(err, errno.Permission) {
		t.Fatalf("越权查询应拒绝: %v", err)
	}
	self, err := credentials.Lookup(context.Background(), 7)
	if err != nil || self.Name != "sandbox" {
		t.Fatalf("self=%+v err=%v", self, err)
	}
}

func TestScopeChargesBeforeSideEffects(t *testing.T) {
	base := newTestFS(t)
	meter := &failingMeter{}
	set := Set{FS: base}
	scoped := Scope(3, set, meter)
	system, err := scoped.Filesystem()
	if err != nil {
		t.Fatal(err)
	}

	if err := system.Mkdir(context.Background(), "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Stat(context.Background(), "/d"); err != nil {
		t.Fatal(err)
	}

	meter.exhausted = true
	if err := system.Mkdir(context.Background(), "/blocked", 0o755); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := base.Stat(context.Background(), "/blocked"); !errors.Is(err, errno.NotFound) {
		t.Fatal("拒绝的副作用不应发生")
	}

	if _, err := system.Open(context.Background(), "/f", vmfs.Create, 0o644); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := base.Stat(context.Background(), "/f"); !errors.Is(err, errno.NotFound) {
		t.Fatal("拒绝的副作用不应发生")
	}
}

func TestScopeWriteChargesBuckets(t *testing.T) {
	base := newTestFS(t)
	var charges uint64
	meter := countMeter{total: &charges}
	scoped := Scope(3, Set{FS: base}, meter)
	system, _ := scoped.Filesystem()
	file, err := system.Open(context.Background(), "/f", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	before := charges
	small := make([]byte, 10)
	if _, err := file.Write(small); err != nil {
		t.Fatal(err)
	}
	if charges-before != 1 {
		t.Fatalf("小写入应计 1 桶: %d", charges-before)
	}
	before = charges
	big := make([]byte, costWriteBucket*2+1)
	if _, err := file.Write(big); err != nil {
		t.Fatal(err)
	}
	if charges-before != 3 {
		t.Fatalf("大写入应计 3 桶: %d", charges-before)
	}
}

type countMeter struct{ total *uint64 }

func (m countMeter) View(context.Context, uint64) (resources.View, error) {
	return resources.View{}, nil
}

func (m countMeter) Charge(_ context.Context, _ uint64, work uint64) error {
	*m.total += work
	return nil
}

func (m countMeter) ChargeKind(_ context.Context, _ uint64, kind resources.Kind, amount uint64) error {
	if kind != resources.KindWork {
		return errno.Invalid
	}
	return m.Charge(context.Background(), 0, amount)
}

func (m countMeter) ReleaseKind(context.Context, uint64, resources.Kind, uint64) error {
	return nil
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestScopeUnconfiguredCapabilitiesStillFail(t *testing.T) {
	scoped := Scope(1, Set{}, nil)
	if _, err := scoped.Time(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := scoped.Random(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := scoped.Network(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := scoped.Meter(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := scoped.Filesystem(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
}

func TestScopePassesThroughConfiguredBackends(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	table := fd.New()
	table.Attach(5, fd.Streams{Output: writerFunc(func(p []byte) (int, error) { return len(p), nil })})
	set := Set{
		FS:        base,
		FD:        table,
		Identity:  identity.SingleUser{},
		Clock:     &clock.Virtual{},
		Entropy:   entropy.NewSeeded(1),
		Bridge:    stubBridge{},
		Image:     stubImage{},
		Audit:     stubAudit{},
		Language:  stubLanguage{},
		Sessions:  storage.New(0),
		Snapshots: storage.New(0),
		Replay:    replay.Record(),
		Net:       net.Denied{},
		IPC:       ipc.Denied{},
	}
	scoped := Scope(5, set, nil)

	if _, err := scoped.Time(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Random(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Host(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.InitialImage(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Events(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Languages(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.SessionStore(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.SnapshotStore(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Journal(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Network(); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Communication(); err != nil {
		t.Fatal(err)
	}

	descriptors, _ := scoped.Descriptors()

	if err := descriptors.Dup(ctx, 99, 0, 3); err != nil {
		t.Fatal(err)
	}
	if err := descriptors.Close(ctx, 99, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Terminal(ctx, 99, 0); err != nil {
		t.Fatal(err)
	}

	system, _ := scoped.Filesystem()
	if _, err := system.Stat(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Lstat(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := system.ReadDir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := system.Remove(ctx, "/d"); err != nil {
		t.Fatal(err)
	}
	file, err := base.Open(ctx, "/f", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if err := base.Symlink(ctx, "/", "/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Readlink(ctx, "/link"); err != nil {
		t.Fatal(err)
	}

	if err := system.Link(ctx, "/f", "/f2"); err != nil {
		t.Fatal(err)
	}
	if err := system.Rename(ctx, "/f2", "/f3"); err != nil {
		t.Fatal(err)
	}
	if err := base.Remove(ctx, "/link"); err != nil {
		t.Fatal(err)
	}
	if err := base.Remove(ctx, "/f"); err != nil {
		t.Fatal(err)
	}
	if err := base.Remove(ctx, "/f3"); err != nil {
		t.Fatal(err)
	}

	if _, err := Scope(5, Set{}, (*resources.Meters)(nil)).Meter(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
}

type stubBridge struct{}

func (stubBridge) Call(context.Context, uint64, string, []byte) ([]byte, error) {
	return nil, nil
}

type stubImage struct{}

func (stubImage) Manifest(context.Context) (image.Manifest, error) { return image.Manifest{}, nil }
func (stubImage) DigestFor(context.Context, string) (string, error) {
	return "", errno.NotFound
}
func (stubImage) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errno.NotFound
}

type stubAudit struct{}

func (stubAudit) Append(context.Context, audit.Event) error { return nil }

type stubLanguage struct{}

func (stubLanguage) Open(context.Context, uint64, string) (language.Slot, error) {
	return nil, errno.NotImplemented
}

func (stubLanguage) Lookup(context.Context, uint64) (language.Slot, error) {
	return nil, errno.NotFound
}

func (stubLanguage) Close(context.Context, uint64) error { return errno.NotFound }

func TestScopeCoversMeterViewAndChargedPaths(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	meters, err := resources.NewSession(resources.Limits{Memory: 1 << 20, Work: 64}, resources.Limits{Memory: 1 << 20, Work: 64})
	if err != nil {
		t.Fatal(err)
	}
	if err := meters.Spawn(7, resources.Limits{}); err != nil {
		t.Fatal(err)
	}
	scoped := Scope(7, Set{FS: base}, meters)

	meter, err := scoped.Meter()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := meter.View(ctx, 99); err != nil {
		t.Fatal(err)
	}
	if err := meter.Charge(ctx, 99, 1); err != nil {
		t.Fatal(err)
	}

	system, _ := scoped.Filesystem()
	if _, err := system.Open(ctx, "/open-missing", 0, 0); err == nil {
		t.Fatal("missing file should fail")
	}
	if _, err := system.Stat(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Lstat(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if _, err := system.ReadDir(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/m", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := system.Rename(ctx, "/m", "/m2"); err != nil {
		t.Fatal(err)
	}
	if err := system.Remove(ctx, "/m2"); err != nil {
		t.Fatal(err)
	}

	meters.Discard(7)
	if err := system.Mkdir(ctx, "/after", 0o755); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("err=%v", err)
	}
}

func TestScopeChargedRejectionBranchesAndFileOps(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	meter := &failingMeter{exhausted: true}
	scoped := Scope(3, Set{FS: base}, meter)
	system, _ := scoped.Filesystem()

	if _, err := system.Stat(ctx, "/"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := system.Lstat(ctx, "/"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := system.ReadDir(ctx, "/"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/a", 0o755); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if err := system.Remove(ctx, "/a"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if err := system.Rename(ctx, "/a", "/b"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if err := system.Link(ctx, "/a", "/b"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if err := system.Symlink(ctx, "/a", "/b"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := system.Readlink(ctx, "/a"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}

	meter.exhausted = false
	file, err := system.Open(ctx, "/life", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 3)
	if _, err := file.Read(data); err != nil || string(data) != "abc" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	set := Set{FS: base, Identity: identity.SingleUser{}, FD: fd.New()}
	scoped = Scope(3, set, nil)
	credentials, err := scoped.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Lookup(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Descriptors(); err != nil {
		t.Fatal(err)
	}
}

func TestScopeSetTimesSnapshotAndRemainingBranches(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	meter := &failingMeter{exhausted: true}
	scoped := Scope(3, Set{FS: base}, meter)
	system, _ := scoped.Filesystem()

	setTimes, canTimes := system.(vmfs.Timestamps)
	snapshooter, canSnap := system.(vmfs.Snapshotter)
	if !canTimes || !canSnap {
		t.Fatal("计量包装应透传时间戳与快照端口")
	}
	if err := setTimes.SetTimes(ctx, "/", 0, 0); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := snapshooter.Snapshot(ctx); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	meter.exhausted = false

	if err := setTimes.SetTimes(ctx, "/", 1, 2); err != nil {
		t.Fatal(err)
	}

	if _, err := base.Stat(ctx, "/"); err != nil {
		t.Fatal(err)
	}

	if _, err := Scope(3, Set{FS: base}, nil).Credentials(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if _, err := Scope(3, Set{FS: base}, nil).Descriptors(); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}

	meter.exhausted = true
	if err := system.Symlink(ctx, "/x", "/y"); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
}

type recordingMeter struct {
	charges   int
	work      uint64
	exhausted bool
}

func (m *recordingMeter) View(context.Context, uint64) (resources.View, error) {
	return resources.View{}, nil
}

func (m *recordingMeter) Charge(_ context.Context, _ uint64, work uint64) error {
	if m.exhausted {
		return errno.Limit
	}
	m.charges++
	m.work += work
	return nil
}

func (m *recordingMeter) ChargeKind(ctx context.Context, pid uint64, kind resources.Kind, amount uint64) error {
	return m.Charge(ctx, pid, amount)
}

func (m *recordingMeter) ReleaseKind(context.Context, uint64, resources.Kind, uint64) error {
	return nil
}

func TestScopeChargesNamedAndMetadataOperations(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	if _, err := base.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	meter := &recordingMeter{}
	system, err := Scope(4, Set{FS: base}, meter).Filesystem()
	if err != nil {
		t.Fatal(err)
	}

	before := meter.charges
	if _, err := system.Readlink(ctx, "/data"); err == nil {
		t.Fatal("/data 不是符号链接，应返回错误")
	}
	if meter.charges != before+1 {
		t.Fatalf("Readlink 应计一次: %d -> %d", before, meter.charges)
	}

	beforeCharges, beforeWork := meter.charges, meter.work
	if err := system.Symlink(ctx, "/data", "/link"); err != nil {
		t.Fatal(err)
	}
	if meter.charges != beforeCharges+1 || meter.work-beforeWork != costNamespace {
		t.Fatalf("Symlink 计费不符: charges=%d work=%d", meter.charges, meter.work)
	}
	before = meter.charges
	if err := system.Link(ctx, "/data", "/hard"); err != nil {
		t.Fatal(err)
	}
	if meter.charges != before+1 {
		t.Fatal("Link 必须先计量")
	}

	meter.exhausted = true
	if err := system.Symlink(ctx, "/data", "/blocked"); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽应报 EAGAIN: %v", err)
	}
	if _, err := base.Lstat(ctx, "/blocked"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("拒绝时不得产生副作用: %v", err)
	}
}

func TestScopeAuthorityIsFrozenAtCreation(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	set := Set{FS: base}
	scoped := Scope(5, set, nil)
	system, err := scoped.Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := system.Stat(ctx, "/"); err != nil {
		t.Fatal(err)
	}

	set.FS = nil
	if _, err := scoped.Filesystem(); err != nil {
		t.Fatalf("装配后改值不应影响已发放的能力: %v", err)
	}

	if _, err := scoped.Credentials(); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("未授予的能力应保持 ENOSYS: %v", err)
	}
}

func TestScopeChargesOpenButNotRead(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	created, err := base.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	meter := &recordingMeter{}
	system, err := Scope(7, Set{FS: base}, meter).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	before := meter.charges
	file, err := system.Open(ctx, "/data", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	if meter.charges != before+1 {
		t.Fatalf("打开应按元数据成本计费一次: %d -> %d", before, meter.charges)
	}
	before = meter.charges
	if _, err := file.Read(make([]byte, 4)); err != nil {
		t.Fatal(err)
	}
	if meter.charges != before {
		t.Fatalf("读取本身不应计费: %d -> %d", before, meter.charges)
	}

	// O_RDONLY 句柄写入按 Linux 报 EBADF;计量器按尝试扣费,失败仍计费。
	before = meter.charges
	if _, err := file.Write([]byte("more")); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("只读句柄写入: %v want EBADF", err)
	}
	if meter.charges != before+1 {
		t.Fatalf("失败的写入仍应计费: %d -> %d", before, meter.charges)
	}
	writable, err := system.Open(ctx, "/data", vmfs.ReadWrite, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := writable.Close(); err != nil {
			t.Error(err)
		}
	}()
	before = meter.charges
	if _, err := writable.Write([]byte("more")); err != nil {
		t.Fatal(err)
	}
	if meter.charges != before+1 {
		t.Fatalf("写入应计费一次分桶: %d -> %d", before, meter.charges)
	}

	before = meter.charges
	if _, err := writable.Write(make([]byte, costWriteBucket*2+1)); err != nil {
		t.Fatal(err)
	}
	if meter.charges != before+1 {
		t.Fatalf("计费按次而非按桶累加: %d -> %d", before, meter.charges)
	}
}

func TestScopedDescriptorsPassThroughInstall(t *testing.T) {
	table := fd.New()
	table.Attach(7, fd.Streams{})
	scoped, err := Scope(7, Set{FD: table}, nil).Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	installer, ok := scoped.(fd.Installer)
	if !ok {
		t.Fatal("完整描述符表的作用域视图应实现安装能力")
	}

	id, err := installer.InstallNext(999, &countingStream{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Get(context.Background(), 7, id); err != nil {
		t.Fatalf("描述符没有装到作用域的进程上: %v", err)
	}

	readOnly, err := Scope(7, Set{FD: readOnlyTable{}}, nil).Descriptors()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readOnly.(fd.Installer); ok {
		t.Fatal("只读描述符视图不应实现安装能力")
	}
}

type countingStream struct{}

func (*countingStream) Read([]byte) (int, error)         { return 0, io.EOF }
func (*countingStream) Write(buffer []byte) (int, error) { return len(buffer), nil }
func (*countingStream) Close() error                     { return nil }

type readOnlyTable struct{}

func (readOnlyTable) Get(context.Context, uint64, uint32) (fd.Stream, error) {
	return nil, errno.BadHandle
}

func (readOnlyTable) Dup(context.Context, uint64, uint32, uint32) error { return errno.BadHandle }

func (readOnlyTable) Close(context.Context, uint64, uint32) error { return errno.BadHandle }

func (readOnlyTable) Terminal(context.Context, uint64, uint32) (fd.Terminal, error) {
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

func TestScopedAddressSpacePinsPID(t *testing.T) {
	binder := &recordingBinder{}
	scoped, err := Scope(7, Set{Spaces: binder}, nil).AddressSpace()
	if err != nil {
		t.Fatal(err)
	}
	table := memory.New(0)
	if err := table.Map(context.Background(), memory.Region{
		Start: 0x1000, End: 0x2000,
		Prot: memory.ProtRead | memory.ProtWrite, Name: "image",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.BindAddressSpace(999, table); err != nil {
		t.Fatal(err)
	}
	if !binder.bound || binder.pid != 7 {
		t.Fatalf("登记进程=%d bound=%v want 7", binder.pid, binder.bound)
	}
	if binder.regions != 1 {
		t.Fatalf("登记的地址空间映射数=%d want 1", binder.regions)
	}

	if _, err := Scope(7, Set{}, nil).AddressSpace(); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("未装配托管端口=%v want ENOSYS", err)
	}
}

type noModesFS struct{ vmfs.System }

func TestScopeForwardsModesAndRefusesWithoutCapability(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	if _, err := base.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	meter := &recordingMeter{}
	system, err := Scope(4, Set{FS: base}, meter).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	modes, ok := system.(vmfs.Modes)
	if !ok {
		t.Fatal("能力视图必须转发 Modes")
	}
	beforeCharges, beforeWork := meter.charges, meter.work
	if err := modes.Chmod(ctx, "/data", 0o600); err != nil {
		t.Fatal(err)
	}
	if meter.charges != beforeCharges+1 || meter.work-beforeWork != costMeta {
		t.Fatalf("Chmod 计费不符: charges=%d work=%d", meter.charges, meter.work)
	}
	if info, err := base.Stat(ctx, "/data"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("模式位 %v %v", info.Mode(), err)
	}

	meter.exhausted = true
	if err := modes.Chmod(ctx, "/data", 0o777); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽应报 EAGAIN: %v", err)
	}
	if info, err := base.Stat(ctx, "/data"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("拒绝时不得改模式: %v %v", info.Mode(), err)
	}

	plain, err := Scope(4, Set{FS: noModesFS{System: base}}, nil).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.(vmfs.Modes).Chmod(ctx, "/data", 0o600); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("能力缺失应报 ENOTSUP: %v", err)
	}
}

func TestScopeForwardsAccessTime(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	if _, err := base.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644); err != nil {
		t.Fatal(err)
	}
	timestamps, ok := base.(vmfs.Timestamps)
	if !ok {
		t.Fatal("测试前提：memfs 实现 Timestamps")
	}
	if err := timestamps.SetTimes(ctx, "/data", 4242, 4242); err != nil {
		t.Fatal(err)
	}
	meter := &recordingMeter{}
	system, err := Scope(4, Set{FS: base}, meter).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	times, ok := system.(vmfs.AccessTimes)
	if !ok {
		t.Fatal("能力视图必须转发 AccessTimes")
	}
	before := meter.charges
	if got, err := times.AccessTime(ctx, "/data"); err != nil || got != 4242 {
		t.Fatalf("atime = %d, %v", got, err)
	}
	if meter.charges != before+1 {
		t.Fatalf("读取访问时间应计费一次: %d", meter.charges)
	}

	plain, err := Scope(4, Set{FS: noModesFS{System: base}}, nil).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.(vmfs.AccessTimes).AccessTime(ctx, "/data"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("能力缺失应报 ENOTSUP: %v", err)
	}
}

type mutableUmask struct{ mask stdfs.FileMode }

func (m *mutableUmask) Umask(uint64) (stdfs.FileMode, error) { return m.mask, nil }

func TestScopeAppliesUmaskAtCreation(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	source := &mutableUmask{mask: 0o077}
	system, err := Scope(3, Set{FS: base, UmaskSource: source}, nil).Filesystem()
	if err != nil {
		t.Fatal(err)
	}

	view := Scope(3, Set{FS: base, UmaskSource: source}, nil)
	if mask, err := view.Umask(); err != nil || mask != 0o077 {
		t.Fatalf("Umask() = %o, %v", mask, err)
	}
	file, err := system.Open(ctx, "/masked", vmfs.Create|vmfs.ReadWrite, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if info, err := base.Stat(ctx, "/masked"); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("创建模式 = %v, %v（掩码未生效？）", info.Mode(), err)
	}
	if err := system.Mkdir(ctx, "/dir", 0o777); err != nil {
		t.Fatal(err)
	}
	if info, err := base.Stat(ctx, "/dir"); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("目录模式 = %v, %v", info.Mode(), err)
	}

	source.mask = 0o022
	if mask, err := view.Umask(); err != nil || mask != 0o022 {
		t.Fatalf("改后 Umask() = %o, %v", mask, err)
	}
	file, err = system.Open(ctx, "/after", vmfs.Create|vmfs.ReadWrite, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if info, err := base.Stat(ctx, "/after"); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("改掩码后的创建模式 = %v, %v", info.Mode(), err)
	}

	plain, err := Scope(3, Set{FS: base}, nil).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Scope(3, Set{FS: base}, nil).Umask(); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("未装配应报 ENOSYS: %v", err)
	}
	file, err = plain.Open(ctx, "/unmasked", vmfs.Create|vmfs.ReadWrite, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if info, err := base.Stat(ctx, "/unmasked"); err != nil || info.Mode().Perm() != 0o666 {
		t.Fatalf("未装配时不应剥位: %v, %v", info.Mode(), err)
	}
}

type noCapacityFS struct{ vmfs.System }

func TestScopeForwardsCapacity(t *testing.T) {
	ctx := context.Background()
	base, err := memfs.New(&clock.Virtual{}, memfs.Options{TotalBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	file, err := base.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	meter := &recordingMeter{}
	system, err := Scope(4, Set{FS: base}, meter).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	capacity, ok := system.(vmfs.Capacity)
	if !ok {
		t.Fatal("能力视图必须转发 Capacity")
	}
	beforeCharges, beforeWork := meter.charges, meter.work
	space, err := capacity.Space(ctx, "/data")
	if err != nil {
		t.Fatal(err)
	}
	if meter.charges != beforeCharges+1 || meter.work-beforeWork != costMeta {
		t.Fatalf("Space 计费不符: charges=%d work=%d", meter.charges, meter.work)
	}
	if want := (vmfs.Quota{Declared: true, Total: 4096, Used: 4, Free: 4092}); space.Bytes != want {
		t.Fatalf("空间事实 = %+v，期望 %+v", space.Bytes, want)
	}

	beforeCharges = meter.charges
	list, err := capacity.Filesystems(ctx)
	if err != nil || len(list) != 1 || meter.charges != beforeCharges+1 {
		t.Fatalf("Filesystems: %+v err=%v charges=%d", list, err, meter.charges)
	}

	meter.exhausted = true
	if _, err := capacity.Space(ctx, "/data"); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽应报 EAGAIN: %v", err)
	}

	plain, err := Scope(4, Set{FS: noCapacityFS{System: base}}, nil).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.(vmfs.Capacity).Space(ctx, "/data"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("能力缺失应报 ENOTSUP: %v", err)
	}
	if _, err := plain.(vmfs.Capacity).Filesystems(ctx); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("能力缺失应报 ENOTSUP: %v", err)
	}
}

type noFifoFS struct{ vmfs.System }

func TestScopeForwardsFifosAndAppliesUmask(t *testing.T) {
	ctx := context.Background()
	base := newTestFS(t)
	source := &mutableUmask{mask: 0o077}
	meter := &recordingMeter{}
	system, err := Scope(3, Set{FS: base, UmaskSource: source}, meter).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	fifos, ok := system.(vmfs.Fifos)
	if !ok {
		t.Fatal("能力视图必须转发 Fifos")
	}
	before := meter.work
	if err := fifos.Mkfifo(ctx, "/pipe", 0o666); err != nil {
		t.Fatal(err)
	}
	if meter.work-before != costNamespace {
		t.Fatalf("Mkfifo 计费不符: %d", meter.work-before)
	}
	info, err := base.Lstat(ctx, "/pipe")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("创建路径必须按掩码剥位: %v %v", info.Mode(), err)
	}
	if info.Mode()&stdfs.ModeNamedPipe == 0 {
		t.Fatalf("节点必须是命名管道: %v", info.Mode())
	}

	meter.exhausted = true
	if err := fifos.Mkfifo(ctx, "/other", 0o644); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽应报 EAGAIN: %v", err)
	}
	if _, err := base.Lstat(ctx, "/other"); !errors.Is(err, errno.NotFound) {
		t.Fatal("被拒绝时不得留下节点")
	}

	plain, err := Scope(3, Set{FS: noFifoFS{System: base}}, nil).Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.(vmfs.Fifos).Mkfifo(ctx, "/elsewhere", 0o644); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("能力缺失应报 ENOTSUP: %v", err)
	}
}
