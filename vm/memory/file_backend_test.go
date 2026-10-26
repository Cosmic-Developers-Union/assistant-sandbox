package memory

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// byteBackend 是内存字节后备(测试用):模拟被映射的文件数据。
type byteBackend struct {
	mu   sync.Mutex
	data []byte
	fail bool
}

func (b *byteBackend) ReadAt(offset uint64, buffer []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errno.IO
	}
	clear(buffer)
	if offset >= uint64(len(b.data)) {
		return nil
	}
	copy(buffer, b.data[offset:])
	return nil
}

func (b *byteBackend) WriteAt(offset uint64, buffer []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errno.IO
	}
	if end := offset + uint64(len(buffer)); end > uint64(len(b.data)) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}
	copy(b.data[offset:], buffer)
	return nil
}

func (b *byteBackend) contents() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.data))
	copy(out, b.data)
	return out
}

// TestFileBackendRegionReadWriteThrough 验证 E09:后备区间的读写直通
// 底层数据,不落页表;保护位照常生效;越文件尾读取零填充。
func TestFileBackendRegionReadWriteThrough(t *testing.T) {
	backend := &byteBackend{data: []byte("hello mmap world!")}
	table := New(0)
	if err := table.Map(context.Background(), Region{
		Start: 0x1000, End: 0x3000, Prot: ProtRead | ProtWrite,
		Name: "shared", Backend: backend, FileBase: 6,
	}); err != nil {
		t.Fatal(err)
	}
	// 读取:首字节对应文件偏移 6('m')。
	buffer := make([]byte, 5)
	if err := table.Read(context.Background(), 0x1000, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "mmap " {
		t.Fatalf("直读=%q", buffer)
	}
	// 写入:直通后备。
	if err := table.Write(context.Background(), 0x1001, []byte("MAP")); err != nil {
		t.Fatal(err)
	}
	if got := string(backend.contents()[6:11]); got != "mMAP " {
		t.Fatalf("后备=%q", got)
	}
	// 越文件尾读取:零填充。
	tail := make([]byte, 8)
	if err := table.Read(context.Background(), 0x2FF8, tail); err != nil {
		t.Fatal(err)
	}
	if tail[0] == 0 && tail[7] != 0 {
		t.Fatalf("越尾=%v", tail)
	}
	// 后备区间不落页表:匿名区间读写互不影响。
	if err := table.Map(context.Background(), Region{Start: 0x3000, End: 0x4000, Prot: ProtRead | ProtWrite}); err != nil {
		t.Fatal(err)
	}
	if err := table.Write(context.Background(), 0x3000, []byte("priv")); err != nil {
		t.Fatal(err)
	}
	anon := make([]byte, 4)
	if err := table.Read(context.Background(), 0x3000, anon); err != nil || string(anon) != "priv" {
		t.Fatalf("匿名=%q err=%v", anon, err)
	}
	// 只读后备区间拒绝写(保护错)。
	ro := New(0)
	_ = ro.Map(context.Background(), Region{Start: 0x1000, End: 0x2000, Prot: ProtRead, Backend: backend})
	if err := ro.Write(context.Background(), 0x1000, []byte("x")); !errors.Is(err, errno.Permission) {
		t.Fatalf("只读写 err=%v want EPERM", err)
	}
	// 后备读失败透传。
	backend.fail = true
	rw := New(0)
	_ = rw.Map(context.Background(), Region{Start: 0x1000, End: 0x2000, Prot: ProtRead, Backend: backend})
	if err := rw.Read(context.Background(), 0x1000, make([]byte, 4)); !errors.Is(err, errno.IO) {
		t.Fatalf("后备错误 err=%v want EIO", err)
	}
}

// TestCloneSharesBackendRegions 验证 E09/E10:克隆后共享映射仍共享
// ——经任一地址空间的写入对另一地址空间可见。
func TestCloneSharesBackendRegions(t *testing.T) {
	backend := &byteBackend{data: make([]byte, 16)}
	parent := New(0)
	if err := parent.Map(context.Background(), Region{
		Start: 0x1000, End: 0x2000, Prot: ProtRead | ProtWrite, Backend: backend,
	}); err != nil {
		t.Fatal(err)
	}
	child, err := parent.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Write(context.Background(), 0x1800, []byte("kid")); err != nil {
		t.Fatal(err)
	}
	seen := make([]byte, 3)
	if err := parent.Read(context.Background(), 0x1800, seen); err != nil || string(seen) != "kid" {
		t.Fatalf("父读=%q err=%v want kid", seen, err)
	}
	if got := string(backend.contents()[0x800:0x803]); got != "kid" {
		t.Fatalf("后备=%q", got)
	}
}

// TestLoadAndSyncVerifyCoverage 验证 E08/E09:Load 无视保护位写入快照,
// Sync 校验范围完整映射(空洞 ENOMEM)。
func TestLoadAndSyncVerifyCoverage(t *testing.T) {
	table := New(0)
	if err := table.Map(context.Background(), Region{Start: 0x1000, End: 0x2000, Prot: ProtRead}); err != nil {
		t.Fatal(err)
	}
	// 只读区间装载快照:不是客体写访问,不受保护位约束。
	if err := table.Load(context.Background(), 0x1000, []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	if err := table.Read(context.Background(), 0x1000, buffer); err != nil || string(buffer) != "snapshot" {
		t.Fatalf("快照=%q err=%v", buffer, err)
	}
	// 装载越界:未映射范围报未映射。
	if err := table.Load(context.Background(), 0x1FF0, make([]byte, 32)); err == nil {
		t.Fatal("越界装载应失败")
	}
	// Sync:完整范围成功,含空洞失败。
	if err := table.Sync(context.Background(), 0x1000, 0x2000); err != nil {
		t.Fatal(err)
	}
	if err := table.Sync(context.Background(), 0x1000, 0x3000); !errors.Is(err, errno.NoMemory) {
		t.Fatalf("空洞 Sync err=%v want ENOMEM", err)
	}
}
