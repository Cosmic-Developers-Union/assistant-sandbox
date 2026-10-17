package layer

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
)

func writeMarker(t *testing.T, system *memfs.FS, path, content string) {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func stateKernel(t *testing.T) (*vm.Kernel, *memfs.FS) {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{
		"noop": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set:      capability.Set{FS: system, FD: fd.New()},
		Programs: registry,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, system
}

func TestCompositeSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	kernel, system := stateKernel(t)
	writeMarker(t, system, "/marker", "v1")

	envelope, err := storage.CaptureEncoded(ctx, storage.Source{Filesystem: system, Language: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.CaptureState(ctx, KernelState{Kernel: kernel}); err != nil {
		t.Fatal(err)
	}
	if !envelope.HasProcTable() {
		t.Fatal("应携带进程表")
	}

	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := storage.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.HasProcTable() || !decoded.HasFSBody() {
		t.Fatalf("往返后应同时保留本体与进程表: body=%v table=%v", decoded.HasFSBody(), decoded.HasProcTable())
	}

	fresh, freshSystem := stateKernel(t)
	writeMarker(t, freshSystem, "/marker", "v1")
	snapshot, err := memfs.DecodeSnapshot(ctx, decoded.FSBody)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Resume(ctx, freshSystem, snapshot); err != nil {
		t.Fatalf("文件系统恢复应成功: %v", err)
	}
	if err := decoded.RestoreState(ctx, KernelState{Kernel: fresh}); err != nil {
		t.Fatalf("进程表恢复应成功: %v", err)
	}

	if err := fresh.EnsureQuietPoint(); err != nil {
		t.Fatalf("恢复后应处于安静点: %v", err)
	}
	init := fresh.Init()
	if init == 0 {
		t.Fatal("应存在 init 进程")
	}

	if _, err := fresh.Begin(0, "noop", nil, "/", nil, fd.Streams{}); err != nil {
		t.Fatalf("恢复后应仍可创建进程: %v", err)
	}
}

func TestRestoreStateRequiresQuietPoint(t *testing.T) {
	ctx := context.Background()
	kernel, system := stateKernel(t)
	envelope, err := storage.CaptureEncoded(ctx, storage.Source{Filesystem: system})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.CaptureState(ctx, KernelState{Kernel: kernel}); err != nil {
		t.Fatal(err)
	}

	if _, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{}); err != nil {
		t.Fatal(err)
	}
	if err := envelope.RestoreState(ctx, KernelState{Kernel: kernel}); !errors.Is(err, errno.Busy) {
		t.Fatalf("非安静点应报 EBUSY: %v", err)
	}
}

func TestCaptureStateRequiresQuietPoint(t *testing.T) {
	ctx := context.Background()
	kernel, system := stateKernel(t)
	envelope, err := storage.CaptureEncoded(ctx, storage.Source{Filesystem: system})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Begin(0, "noop", nil, "/", nil, fd.Streams{}); err != nil {
		t.Fatal(err)
	}
	if err := envelope.CaptureState(ctx, KernelState{Kernel: kernel}); !errors.Is(err, errno.Busy) {
		t.Fatalf("非安静点捕获应报 EBUSY: %v", err)
	}
}

func TestStateAdapterAndEnvelopeValidation(t *testing.T) {
	ctx := context.Background()

	if err := (KernelState{}).QuietPoint(); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺内核应报 EINVAL: %v", err)
	}
	if _, err := (KernelState{}).ProcTable(ctx); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺内核应报 EINVAL: %v", err)
	}
	if err := (KernelState{}).RestoreProcTable(ctx, []byte("x")); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺内核应报 EINVAL: %v", err)
	}

	kernel, system := stateKernel(t)
	envelope, err := storage.CaptureEncoded(ctx, storage.Source{Filesystem: system})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.CaptureState(ctx, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 状态源应报 EINVAL: %v", err)
	}

	if err := envelope.RestoreState(ctx, KernelState{Kernel: kernel}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺进程表应报 EINVAL: %v", err)
	}
	var nilEnvelope *storage.Envelope
	if err := nilEnvelope.CaptureState(ctx, KernelState{Kernel: kernel}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 信封应报 EINVAL: %v", err)
	}
	if err := nilEnvelope.RestoreState(ctx, KernelState{Kernel: kernel}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 信封应报 EINVAL: %v", err)
	}
	if nilEnvelope.HasProcTable() {
		t.Fatal("nil 信封不应报告有进程表")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := envelope.CaptureState(cancelled, KernelState{Kernel: kernel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := envelope.RestoreState(cancelled, KernelState{Kernel: kernel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}

func TestDecodeRejectsForeignFormat(t *testing.T) {
	ctx := context.Background()
	if _, err := storage.Decode([]byte(`{"format":"assistant-sandbox-snapshot-0"}`)); !errors.Is(err, errno.Version) {
		t.Fatalf("旧格式应报 EPROTO: %v", err)
	}

	kernel, system := stateKernel(t)
	envelope, err := storage.CaptureEncoded(ctx, storage.Source{Filesystem: system})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.CaptureState(ctx, KernelState{Kernel: kernel}); err != nil {
		t.Fatal(err)
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := storage.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.HasProcTable() {
		t.Fatal("往返后应仍有进程表")
	}
}
