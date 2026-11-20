package instance

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// TestSharedMemoryDirectoryAcrossProcesses 验证 N09:/dev/shm 可被同一
// VM 的不同进程读写,另一台 VM 不可见,删除后容量正确回收。
func TestSharedMemoryDirectoryAcrossProcesses(t *testing.T) {
	writeProgram := executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		system, err := p.Resources.Filesystem()
		if err != nil {
			return 0, err
		}
		file, err := system.Open(ctx, "/dev/shm/value", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
		if err != nil {
			return 0, err
		}
		_, writeErr := file.Write([]byte("shared-object"))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return 0, errors.Join(writeErr, closeErr)
		}
		return 0, nil
	})
	readProgram := executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		system, err := p.Resources.Filesystem()
		if err != nil {
			return 0, err
		}
		file, err := system.Open(ctx, "/dev/shm/value", vmfs.ReadOnly, 0)
		if err != nil {
			return 3, nil //nolint:nilerr // 以退出码断言读取失败
		}
		data, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return 0, errors.Join(readErr, closeErr)
		}
		if string(data) != "shared-object" {
			return 4, nil
		}
		return 0, nil
	})
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Seed:          1,
		Programs: map[string]executable.Program{
			"shm-write": writeProgram,
			"shm-read":  readProgram,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	first, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := first.Exec(t.Context(), ExecSpec{Program: "shm-write"})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := first.Wait(t.Context(), writer.ID); err != nil || state.ExitCode != 0 {
		t.Fatalf("写入进程=%+v err=%v", state, err)
	}
	reader, err := second.Exec(t.Context(), ExecSpec{Program: "shm-read"})
	if err != nil {
		t.Fatal(err)
	}
	if state, err := second.Wait(t.Context(), reader.ID); err != nil || state.ExitCode != 0 {
		t.Fatalf("读取进程=%+v err=%v", state, err)
	}
	// 另一台 VM 看到独立的 /dev/shm。
	other, err := New(t.Context(), Options{Deterministic: true, Seed: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close(context.Background()) }()
	if _, err := other.Filesystem().Open(t.Context(), "/dev/shm/value", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("跨 VM /dev/shm err=%v", err)
	}
	// 删除后同一路径可重新创建。
	if err := machine.Filesystem().Remove(t.Context(), "/dev/shm/value"); err != nil {
		t.Fatal(err)
	}
	file, err := machine.Filesystem().Open(t.Context(), "/dev/shm/value", vmfs.Create|vmfs.ReadWrite, 0o600)
	if err != nil {
		t.Fatalf("回收后重建: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSharedMemoryCapacity 验证 N09:共享内存容量受限,删除后额度归还。
func TestSharedMemoryCapacity(t *testing.T) {
	machine, err := New(t.Context(), Options{Deterministic: true, Seed: 1, ShmBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	file, err := machine.Filesystem().Open(t.Context(), "/dev/shm/big", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	written := 0
	var writeErr error
	for written < 64 {
		n, err := file.Write([]byte(strings.Repeat("x", 64-written)))
		written += n
		if err != nil {
			writeErr = err
			break
		}
		if n == 0 {
			break
		}
	}
	if !errors.Is(writeErr, errno.NoSpace) || written != 32 {
		t.Fatalf("写入=%d err=%v want 32/ENOSPC", written, writeErr)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := machine.Filesystem().Remove(t.Context(), "/dev/shm/big"); err != nil {
		t.Fatal(err)
	}
	again, err := machine.Filesystem().Open(t.Context(), "/dev/shm/big", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := again.Write([]byte(strings.Repeat("y", 32))); err != nil || n != 32 {
		t.Fatalf("回收后写入=%d err=%v", n, err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}
