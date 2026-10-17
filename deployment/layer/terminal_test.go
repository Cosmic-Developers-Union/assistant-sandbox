package layer

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

func terminalKernel(t *testing.T) (*vm.Kernel, proc.Proc) {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set:      capability.Set{FD: fd.New(), Clock: &clock.Virtual{}},
		Programs: registry,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}

func TestAttachTerminalWiresStandardStreams(t *testing.T) {
	ctx := context.Background()
	kernel, process := terminalKernel(t)
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: true}, tty.Size{Rows: 24, Cols: 80}, 1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := AttachTerminal(terminals, kernel, process)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{0, 1, 2} {
		info, err := kernel.Terminal(ctx, process.ID, id)
		if err != nil {
			t.Fatalf("描述符 %d 查询失败: %v", id, err)
		}
		if !info.TTY || info.Rows != 24 || info.Columns != 80 {
			t.Fatalf("描述符 %d 未接上终端: %+v", id, info)
		}
	}

	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 8)); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("未整行不应可读: %v", err)
	}
	if _, err := terminals.Input([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	n, err := kernel.ReadFD(ctx, process.ID, 0, buffer)
	if err != nil || string(buffer[:n]) != "hello\n" {
		t.Fatalf("读到的行=%q err=%v", buffer[:n], err)
	}

	if output := string(terminals.Drain()); output != "hello\n" {
		t.Fatalf("回显=%q", output)
	}

	cleanup()
	for _, id := range []uint32{0, 1, 2} {
		if _, err := kernel.Terminal(ctx, process.ID, id); !errors.Is(err, errno.BadHandle) {
			t.Fatalf("清理后描述符 %d 应无效: %v", id, err)
		}
	}
}

func TestAttachTerminalRejectsInvalidInput(t *testing.T) {
	kernel, process := terminalKernel(t)
	terminals, err := tty.New(tty.Flags{}, tty.Size{}, 64, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AttachTerminal(nil, kernel, process); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 终端应报 EINVAL: %v", err)
	}
	if _, err := AttachTerminal(terminals, nil, process); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 内核应报 EINVAL: %v", err)
	}
}

func TestKernelInstallAndCloseFD(t *testing.T) {
	ctx := context.Background()
	kernel, process := terminalKernel(t)
	terminals, err := tty.New(tty.Flags{}, tty.Size{}, 64, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := kernel.InstallFD(process.ID, 1, terminals); err != nil {
		t.Fatal(err)
	}
	if info, err := kernel.Terminal(ctx, process.ID, 1); err != nil || !info.TTY {
		t.Fatalf("替换后应为终端: %+v err=%v", info, err)
	}
	if err := kernel.InstallFD(process.ID, 1, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 对象应报 EINVAL: %v", err)
	}
	if err := kernel.InstallFD(proc.PID(999), 1, terminals); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
	if err := kernel.CloseFD(process.ID, 42); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("无效编号应报 EBADF: %v", err)
	}
	if err := kernel.CloseFD(process.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := kernel.CloseFD(process.ID, 1); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("重复关闭应报 EBADF: %v", err)
	}
}

func TestTerminalReadFDBlocksUntilLine(t *testing.T) {
	kernel, process := terminalKernel(t)
	terminals, err := tty.New(tty.Flags{Canonical: true}, tty.Size{}, 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AttachTerminal(terminals, kernel, process); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := kernel.ReadContextFD(context.Background(), process.ID, 0, make([]byte, 8))
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
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("整行到达后应唤醒读")
	}
}

var _ = io.Discard
