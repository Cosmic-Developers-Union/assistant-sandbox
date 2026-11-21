package instance

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// TestDualVMIsolation 验证 O08:文件、共享内存、设备、PID、socket 与
// 进程耗尽在两个 VM 间互不污染。
func TestDualVMIsolation(t *testing.T) {
	opts := func(seed uint64) Options {
		return Options{
			Deterministic: true, Seed: seed,
			MaxProcesses: 3,
			Session:      resources.Limits{Memory: 128 << 20, Work: 1 << 32},
			Process:      resources.Limits{Memory: 64 << 20, Work: 1 << 32},
			Programs: map[string]executable.Program{
				"hold": executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
					<-ctx.Done()
					return 0, ctx.Err()
				}),
			},
		}
	}
	first, err := New(t.Context(), opts(1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	second, err := New(t.Context(), opts(2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()

	// 文件:同一路径各自内容。
	for index, machine := range []*VM{first, second} {
		file, err := machine.Filesystem().Open(t.Context(), "/work/shared.txt", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		data := []byte("vm" + string(rune('1'+index)))
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for index, machine := range []*VM{first, second} {
		file, err := machine.Filesystem().Open(t.Context(), "/work/shared.txt", vmfs.ReadOnly, 0)
		if err != nil {
			t.Fatal(err)
		}
		data := make([]byte, 16)
		n, _ := file.Read(data)
		_ = file.Close()
		if string(data[:n]) != "vm"+string(rune('1'+index)) {
			t.Fatalf("VM%d 文件=%q", index+1, data[:n])
		}
	}
	// 共享内存与设备。
	shm, err := first.Filesystem().Open(t.Context(), "/dev/shm/only-first", vmfs.Create|vmfs.ReadWrite, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shm.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = shm.Close()
	if _, err := second.Filesystem().Open(t.Context(), "/dev/shm/only-first", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("共享内存跨 VM 可见: %v", err)
	}
	null, err := second.Filesystem().Open(t.Context(), "/dev/null", vmfs.ReadWrite, 0)
	if err != nil || null.Close() != nil {
		t.Fatalf("第二 VM 设备不可用: %v", err)
	}
	// PID:相同编号属于不同 VM 的进程表。
	if first.Kernel() == second.Kernel() {
		t.Fatal("两台 VM 共享内核")
	}

	// 进程耗尽只影响第一台 VM。
	firstConnection, err := first.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	holdCtx, cancelHold := context.WithCancel(t.Context())
	defer cancelHold()
	if _, err := firstConnection.Exec(holdCtx, ExecSpec{Program: "hold"}); err != nil {
		t.Fatal(err)
	}
	if _, err := firstConnection.Exec(t.Context(), ExecSpec{Program: "hold"}); !errors.Is(err, errno.Limit) {
		t.Fatalf("耗尽后创建 err=%v want EAGAIN", err)
	}
	secondConnection, err := second.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondConnection.Exec(t.Context(), ExecSpec{Program: "hold"}); err != nil {
		t.Fatalf("另一台 VM 不应受耗尽影响: %v", err)
	}
	cancelHold()
}

// TestDualVMNetworkNamespaces 验证 O08/M01:两台 VM 同时运行真实网络
// 探针(相同回环端口)互不冲突。
func TestDualVMNetworkNamespaces(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/m-net")
	if err != nil {
		t.Fatal(err)
	}
	first, second := newNetVM(t), newNetVM(t)
	outputs := make([]*bytes.Buffer, 2)
	wait := make([]func(), 2)
	for index, machine := range []*VM{first, second} {
		probe, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := probe.Write(image); err != nil {
			t.Fatal(err)
		}
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
		connection, err := machine.Connect(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		buffer := &bytes.Buffer{}
		outputs[index] = buffer
		process, err := connection.Exec(t.Context(), ExecSpec{Program: "/probe", Dir: "/work", Streams: fd.Streams{Output: buffer}})
		if err != nil {
			t.Fatal(err)
		}
		wait[index] = func() {
			state, err := connection.Wait(context.Background(), process.ID)
			if err != nil || state.ExitCode != 0 {
				t.Errorf("VM%d 网络探针=%+v err=%v 输出=%q", index+1, state, err, buffer.String())
			}
			if !strings.Contains(buffer.String(), "M-NET-OK") {
				t.Errorf("VM%d 输出=%q", index+1, buffer.String())
			}
		}
	}
	for _, fn := range wait {
		fn()
	}
}

func newNetVM(t *testing.T) *VM {
	t.Helper()
	machine, err := New(t.Context(), Options{
		Deterministic: true, Seed: 3,
		MaxProcesses: 16,
		Session:      resources.Limits{Memory: 256 << 20, Work: 1 << 32},
		Process:      resources.Limits{Memory: 128 << 20, Work: 1 << 32},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	return machine
}

// TestExecContextCancellationStopsBuiltin 验证取消 Exec 上下文会结束
// 创建时使用的内置程序(排队/等待不遗留宿主 goroutine)。
func TestExecContextCancellationStopsBuiltin(t *testing.T) {
	machine, err := New(t.Context(), Options{
		Deterministic: true, Seed: 1,
		Programs: map[string]executable.Program{
			"hold": executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	process, err := connection.Exec(ctx, ExecSpec{Program: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() {
		_, waitErr := connection.Wait(context.Background(), process.ID)
		done <- waitErr
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消上下文后进程未结束")
	}
}
