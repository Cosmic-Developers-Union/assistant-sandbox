package vm

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

func fdKernel(t *testing.T) (*Kernel, proc.Proc, *fd.Table) {
	t.Helper()
	descriptors := fd.New()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: descriptors}, Programs: registry}, 8)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process, descriptors
}

func TestKernelTerminalReportsInstalledTerminal(t *testing.T) {
	ctx := context.Background()
	kernel, process, _ := fdKernel(t)

	if info, err := kernel.Terminal(ctx, process.ID, 0); err != nil || info.TTY {
		t.Fatalf("标准流不应是终端: %+v err=%v", info, err)
	}
	terminals, err := tty.New(tty.Flags{}, tty.Size{Rows: 12, Cols: 40}, 128, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(process.ID, 0, terminals); err != nil {
		t.Fatal(err)
	}
	info, err := kernel.Terminal(ctx, process.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !info.TTY || info.Rows != 12 || info.Columns != 40 {
		t.Fatalf("终端信息不符: %+v", info)
	}

	if _, err := kernel.Terminal(ctx, process.ID, 9); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("无效描述符应报 EBADF: %v", err)
	}
	if _, err := kernel.Terminal(ctx, proc.PID(999), 0); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := kernel.Terminal(cancelled, process.ID, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestKernelTerminalWithoutDescriptorTable(t *testing.T) {
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Terminal(context.Background(), kernel.Init(), 0); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("未装配 FD 表应报 ENOSYS: %v", err)
	}
	if _, err := kernel.ReadFD(context.Background(), kernel.Init(), 0, nil); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("未装配 FD 表读应报 ENOSYS: %v", err)
	}
}

func TestKernelInstallAndCloseFDByNumber(t *testing.T) {
	kernel, process, descriptors := fdKernel(t)
	handle := &countingStream{}
	if err := kernel.InstallFD(process.ID, 5, handle); err != nil {
		t.Fatal(err)
	}
	if opened := descriptors.Opened(uint64(process.ID)); !containsPID(opened, 5) {
		t.Fatalf("安装后应可见: %v", opened)
	}

	replacement := &countingStream{}
	if err := kernel.InstallFD(process.ID, 5, replacement); err != nil {
		t.Fatal(err)
	}
	if handle.closes != 1 {
		t.Fatalf("替换应释放旧对象: %d", handle.closes)
	}
	if err := kernel.InstallFD(process.ID, 5, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 对象应报 EINVAL: %v", err)
	}
	if err := kernel.InstallFD(proc.PID(999), 5, replacement); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
	if err := kernel.CloseFD(proc.PID(999), 5); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程关闭应报 ESRCH: %v", err)
	}
	if err := kernel.CloseFD(process.ID, 42); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("无效编号应报 EBADF: %v", err)
	}
	if err := kernel.CloseFD(process.ID, 5); err != nil {
		t.Fatal(err)
	}
	if err := kernel.CloseFD(process.ID, 5); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭应报 EBADF: %v", err)
	}
}

func TestKernelReadWriteFD(t *testing.T) {
	ctx := context.Background()
	kernel, process, _ := fdKernel(t)
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{}, 128, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(process.ID, 0, terminals); err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(process.ID, 1, terminals); err != nil {
		t.Fatal(err)
	}

	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 4)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("未整行应报 EWOULDBLOCK: %v", err)
	}

	done := make(chan error, 1)
	var read []byte
	go func() {
		buffer := make([]byte, 8)
		n, err := kernel.ReadContextFD(ctx, process.ID, 0, buffer)
		read = buffer[:n]
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("整行未到达不应返回: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err := terminals.Input([]byte("ok\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil || string(read) != "ok\n" {
			t.Fatalf("read=%q err=%v", read, err)
		}
	case <-time.After(time.Second):
		t.Fatal("整行到达后应唤醒读")
	}

	if _, err := kernel.WriteFD(ctx, process.ID, 1, []byte("out")); err != nil {
		t.Fatal(err)
	}
	if output := string(terminals.Drain()); output != "out" {
		t.Fatalf("写入未进入终端: %q", output)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := kernel.ReadFD(cancelled, process.ID, 0, make([]byte, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消读应报取消: %v", err)
	}
	if _, err := kernel.WriteFD(cancelled, process.ID, 1, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消写应报取消: %v", err)
	}

	plain := &countingStream{reader: bytes.NewReader(nil)}
	if err := kernel.InstallFD(process.ID, 3, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.ReadContextFD(ctx, process.ID, 3, make([]byte, 4)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("无阻塞能力的流应回退非阻塞读: %v", err)
	}
}

type countingStream struct {
	reader *bytes.Reader
	closes int
}

func (s *countingStream) Read(p []byte) (int, error)  { return 0, errno.WouldBlock }
func (s *countingStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *countingStream) Close() error                { s.closes++; return nil }

func containsPID(opened []uint32, want uint32) bool {
	for _, id := range opened {
		if id == want {
			return true
		}
	}
	return false
}

func TestKernelFDQueriesAfterClose(t *testing.T) {
	ctx := context.Background()
	kernel, process, _ := fdKernel(t)
	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Terminal(ctx, process.ID, 0); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后查询终端应报 ECANCELED: %v", err)
	}
	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 4)); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后读应报 ECANCELED: %v", err)
	}
	if _, err := kernel.WriteFD(ctx, process.ID, 0, []byte("x")); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后写应报 ECANCELED: %v", err)
	}
	if err := kernel.InstallFD(process.ID, 3, &countingStream{}); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后安装应报 ECANCELED: %v", err)
	}
	if err := kernel.CloseFD(process.ID, 3); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后关闭描述符应报 ECANCELED: %v", err)
	}
}

func TestRestoreTableRefusesWhileExecutorActive(t *testing.T) {
	var kernel *Kernel
	var data []byte
	registry, err := executable.New(map[string]executable.Program{
		"probe": executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {

			if err := kernel.RestoreTable(ctx, data); !errors.Is(err, errno.Busy) {
				t.Errorf("运行中恢复应报 EBUSY: %v", err)
			}
			return 0, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err = New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 8)
	if err != nil {
		t.Fatal(err)
	}
	data, err = kernel.SnapshotTable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Exec(context.Background(), "probe", nil); err != nil {
		t.Fatal(err)
	}
}
