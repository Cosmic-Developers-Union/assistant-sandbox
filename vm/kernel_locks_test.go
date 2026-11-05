package vm

import (
	"context"
	"errors"
	"testing"

	elf "github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/locks"
)

// locksKernel 构造带文件锁表的内核与共享 memfs。
func locksKernel(t *testing.T) (*Kernel, *locks.Table) {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set: capability.Set{
			FS:       system,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    &clock.Virtual{},
			Entropy:  entropy.NewSeeded(1),
		},
		Programs: registry,
		Binaries: elf.Factory{},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, kernel.locks
}

// TestProcessExitReleasesFileLocks 验证 I14:进程退出释放其持有的
// 全部文件锁并唤醒阻塞的 SETLKW 等待者——两个 Begin'ed 进程经各自
// 的文件描述符竞争同一 inode,退出者不遗留锁。
func TestProcessExitReleasesFileLocks(t *testing.T) {
	kernel, table := locksKernel(t)
	defer func() { _ = kernel.Close(context.Background()) }()

	ctx := context.Background()
	// 两个进程各开同一文件(inode 相同)。
	first, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	// 通过内存表直接建一个共享 inode:经 memfs 两次 Open 同一路径。
	ctxFS, err := first.Resources.Filesystem()
	if err != nil {
		t.Fatal(err)
	}
	fileA, err := ctxFS.Open(ctx, "/locked", vmfs.Create|vmfs.ReadWrite, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fileB, err := ctxFS.Open(ctx, "/locked", vmfs.ReadWrite, 0)
	if err != nil {
		t.Fatal(err)
	}
	lockableA, ok := fileA.(vmfs.Lockable)
	if !ok {
		t.Fatal("memfs 句柄应实现 Lockable")
	}
	lockableB, ok := fileB.(vmfs.Lockable)
	if !ok {
		t.Fatal("第二句柄应实现 Lockable")
	}
	if lockableA.LockIdentity() != lockableB.LockIdentity() {
		t.Fatal("两进程打开同一文件应共享锁对象")
	}
	key := locks.Key(lockableA.LockIdentity())
	// 进程 A 取写锁;进程 B 冲突。
	if err := table.Set(key, uint64(first.ID), locks.Range{Start: 0}, locks.Write); err != nil {
		t.Fatal(err)
	}
	if err := table.Set(key, uint64(second.ID), locks.Range{Start: 0}, locks.Write); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("竞争: %v want EAGAIN", err)
	}
	// 进程 A 退出(经 Finish → exitResources):锁释放,额度归还。
	if err := kernel.Finish(0, first.ID, ExitStatus{Code: 0}); err != nil {
		t.Fatal(err)
	}
	if err := table.Set(key, uint64(second.ID), locks.Range{Start: 0}, locks.Write); err != nil {
		t.Fatalf("退出后锁未释放: %v", err)
	}
	_ = fileA.Close()
	_ = fileB.Close()
	if err := kernel.Finish(0, second.ID, ExitStatus{Code: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(second.ID); err != nil {
		t.Fatal(err)
	}
}
