package memfs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func fifoOf(t *testing.T, f *FS) vmfs.Fifos {
	t.Helper()
	fifos, ok := any(f).(vmfs.Fifos)
	if !ok {
		t.Fatal("memfs 必须实现 vmfs.Fifos")
	}
	return fifos
}

func TestMkfifoCreatesNamespaceFactWithoutIOSemantics(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	fifos := fifoOf(t, f)
	if err := fifos.Mkfifo(ctx, "/pipe", 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := f.Lstat(ctx, "/pipe")
	if err != nil || info.Mode()&stdfs.ModeNamedPipe == 0 || info.Mode().Perm() != 0o640 {
		t.Fatalf("节点类型/权限: %v %v", info.Mode(), err)
	}
	if info.IsDir() || info.Size() != 0 {
		t.Fatalf("管道不是目录、也没有内容: %v size=%d", info.IsDir(), info.Size())
	}
	entries, err := f.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == "pipe" && entry.Type()&stdfs.ModeNamedPipe != 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("目录项里应能看见管道及其类型: %v", entries)
	}
	reader, err := f.Open(ctx, "/pipe", vmfs.ReadOnly|vmfs.NonBlock, 0)
	if err != nil {
		t.Fatalf("非阻塞读侧应能打开: %v", err)
	}
	if info, err := reader.Stat(); err != nil || info.Mode()&stdfs.ModeNamedPipe == 0 {
		t.Fatalf("fd 的 fstat 应报告命名管道: %v %v", info, err)
	}
	var buffer [1]byte
	if _, err := reader.Read(buffer[:]); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("空命名管道非阻塞读应报 EAGAIN: %v", err)
	}
	writer, err := f.Open(ctx, "/pipe", vmfs.WriteOnly|vmfs.NonBlock, 0)
	if err != nil {
		t.Fatalf("非阻塞写侧应能打开: %v", err)
	}
	if _, err := writer.Write([]byte("x")); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("写侧等待语义尚未接入时应如实拒绝: %v", err)
	}
}

func TestMkfifoErrorsAndSnapshotRoundTrip(t *testing.T) {
	f, _ := newFS(t)
	ctx := context.Background()
	fifos := fifoOf(t, f)
	if err := fifos.Mkfifo(ctx, "/pipe", 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fifos.Mkfifo(ctx, "/pipe", 0o644); !errors.Is(err, errno.Exist) {
		t.Fatalf("同名应报 EEXIST: %v", err)
	}
	if err := fifos.Mkfifo(ctx, "/nope/pipe", 0o644); !errors.Is(err, errno.NotFound) {
		t.Fatalf("父目录不存在应报 ENOENT: %v", err)
	}
	snap, err := f.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.Restore(ctx, f); err != nil {
		t.Fatal(err)
	}
	info, err := f.Lstat(ctx, "/pipe")
	if err != nil || info.Mode()&stdfs.ModeNamedPipe == 0 || info.Mode().Perm() != 0o644 {
		t.Fatalf("快照往返后仍是命名管道: %v %v", info.Mode(), err)
	}
	reader, err := f.Open(ctx, "/pipe", vmfs.ReadOnly|vmfs.NonBlock, 0)
	if err != nil {
		t.Fatalf("恢复后读侧应能打开: %v", err)
	}
	_ = reader.Close()
}
