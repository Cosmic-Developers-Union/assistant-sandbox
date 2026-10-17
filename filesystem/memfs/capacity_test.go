package memfs

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func capacityOf(t *testing.T, f *FS) vmfs.Capacity {
	t.Helper()
	capacity, ok := any(f).(vmfs.Capacity)
	if !ok {
		t.Fatal("memfs 必须实现 vmfs.Capacity")
	}
	return capacity
}

func TestSpaceReportsInodeQuotaAndUndeclaredBytes(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/a.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}

	space, err := capacityOf(t, f).Space(ctx, "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if space.Name != "/" || space.Root != "/" {
		t.Fatalf("客体文件系统身份是它的命名空间根: %+v", space)
	}
	want := vmfs.Quota{Declared: true, Total: defaultMaxNodes, Used: 2, Free: defaultMaxNodes - 2}
	if space.Inodes != want {
		t.Fatalf("inode 配额 = %+v，期望 %+v", space.Inodes, want)
	}
	if space.Bytes.Declared || space.Bytes.Total != 0 || space.Bytes.Free != 0 {
		t.Fatalf("没有声明 TotalBytes 时不得报告容量: %+v", space.Bytes)
	}

	if space.Bytes.Used != 5 {
		t.Fatalf("已用字节必须是真实计数: %+v", space.Bytes)
	}
}

func TestDeclaredTotalBytesIsEnforced(t *testing.T) {
	clk := &clock.Virtual{}
	f, err := New(clk, Options{TotalBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	file, err := f.Open(ctx, "/a.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := file.Write([]byte("12345678")); n != 8 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := file.Write([]byte("x")); n != 0 || !errors.Is(err, errno.NoSpace) {
		t.Fatalf("超容量写入必须报 ENOSPC 且不写入: n=%d err=%v", n, err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 8 {
		t.Fatalf("超容量写入不得改变文件: %+v %v", info, err)
	}

	other, err := f.Open(ctx, "/b.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := other.Write([]byte("y")); n != 0 || !errors.Is(err, errno.NoSpace) {
		t.Fatalf("文件系统级容量必须跨文件生效: n=%d err=%v", n, err)
	}
	space, err := capacityOf(t, f).Space(ctx, "/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if space.Bytes != (vmfs.Quota{Declared: true, Total: 8, Used: 8, Free: 0}) {
		t.Fatalf("已用量必须是真实计数: %+v", space.Bytes)
	}
}

func TestUnlinkedOpenFileStillOccupiesSpace(t *testing.T) {
	clk := &clock.Virtual{}
	f, err := New(clk, Options{TotalBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	file, err := f.Open(ctx, "/a.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	space, err := capacityOf(t, f).Space(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if space.Bytes.Used != 8 {
		t.Fatalf("已 unlink 但打开中的文件仍占空间: %+v", space.Bytes)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	space, err = capacityOf(t, f).Space(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if space.Bytes.Used != 0 || space.Inodes.Used != 1 {
		t.Fatalf("最后一个句柄关闭后应回收: %+v", space)
	}
}

func TestHardLinkedContentCountedOnce(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/a.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := f.Link(ctx, "/a.txt", "/b.txt"); err != nil {
		t.Fatal(err)
	}
	used, err := capacityOf(t, f).Space(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := used.Bytes.Used; got != 4 {
		t.Fatalf("硬链接内容应只算一次: %d", got)
	}
	if got := f.usedBytes(); got != 4 {
		t.Fatalf("存活表的读数应与对外报告一致: %d", got)
	}
}

func TestSpaceRejectsMissingPath(t *testing.T) {
	f, _ := newFS(t)
	if _, err := capacityOf(t, f).Space(context.Background(), "/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不存在的路径应报 ENOENT: %v", err)
	}
}

func TestFilesystemsListsItselfAndHonorsCancel(t *testing.T) {
	f, _ := newFS(t)
	capacity := capacityOf(t, f)
	list, err := capacity.Filesystems(context.Background())
	if err != nil || len(list) != 1 || list[0].Root != "/" || !list[0].Inodes.Declared {
		t.Fatalf("单棵树实现应返回自己那一项: %+v %v", list, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := capacity.Filesystems(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
	if _, err := capacity.Space(ctx, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
}

func TestRestoreRebuildsUsageAccounting(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	file, err := f.Open(ctx, "/a.txt", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	snap, err := f.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("5678")); err != nil {
		t.Fatal(err)
	}
	if got := f.usedBytes(); got != 8 {
		t.Fatalf("恢复前已用: %d", got)
	}
	if err := snap.Restore(ctx, f); err != nil {
		t.Fatal(err)
	}

	if got := f.usedBytes(); got != 12 {
		t.Fatalf("恢复后已用（树内 4 + 旧句柄 8）: %d", got)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if got := f.usedBytes(); got != 4 {
		t.Fatalf("句柄关闭后只剩恢复出来的树: %d", got)
	}
	space, err := capacityOf(t, f).Space(ctx, "/a.txt")
	if err != nil || space.Inodes.Used != 2 {
		t.Fatalf("恢复后的 inode 计数: %+v %v", space, err)
	}
	if _, err := f.Stat(ctx, "/a.txt"); err != nil {
		t.Fatalf("恢复出来的文件必须可读: %v", err)
	}
}
