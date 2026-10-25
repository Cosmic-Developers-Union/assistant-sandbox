package instance

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// TestCreationRollbackReleasesResources 验证 C09:逐个注入初始化失败,
// 已创建的内核被关闭、句柄释放,不留半成品。
func TestCreationRollbackReleasesResources(t *testing.T) {
	injected := errors.New("注入的初始化失败")
	cases := []struct {
		name  string
		apply func(*initFaults)
	}{
		{"挂载表之后", func(f *initFaults) { f.mounts = injected }},
		{"设备注册表之后", func(f *initFaults) { f.devices = injected }},
		{"内核创建之后", func(f *initFaults) { f.kernel = injected }},
		{"常驻会话之后", func(f *initFaults) { f.session = injected }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var observed *vm.Kernel
			opts := Options{Deterministic: true}
			testCase.apply(&opts.faults)
			opts.faults.observe = func(kernel *vm.Kernel) { observed = kernel }
			machine, err := New(t.Context(), opts)
			if !errors.Is(err, injected) {
				t.Fatalf("err=%v want 注入失败", err)
			}
			if machine != nil {
				t.Fatal("失败时不应返回半成品 VM")
			}
			if observed == nil {
				return // 失败发生在内核创建之前,无需回滚内核
			}
			// 已创建的内核必须被关闭:状态进入终态、不再接受任务。
			closed, err := observed.Closed()
			if err != nil || !closed {
				t.Fatalf("回滚后内核未关闭: %v %v", closed, err)
			}
			if _, err := observed.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Cancelled) {
				t.Fatalf("回滚后的内核仍接受任务: %v", err)
			}
			// 句柄已释放:没有任何进程持有描述符。
			for _, process := range observed.Processes() {
				opened, err := observed.OpenFDs(process.ID)
				if err != nil {
					continue
				}
				if len(opened) != 0 {
					t.Fatalf("进程 %d 仍持有描述符: %v", process.ID, opened)
				}
			}
		})
	}
}

// TestImageCreatesIsolatedVMs 验证 C10:同一镜像可创建多台互相隔离
// 的 VM;实例上的写入不改变镜像,断开连接也不改变镜像。
func TestImageCreatesIsolatedVMs(t *testing.T) {
	image, err := NewImage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	digest := image.Digest()

	first, err := image.Instantiate(t.Context(), Options{Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close(context.Background()) }()
	second, err := image.Instantiate(t.Context(), Options{Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()

	// 第一台 VM 的写入不进入第二台 VM。
	if err := writeFile(t, first, "/work/only-first", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Filesystem().Open(t.Context(), "/work/only-first", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("第二台 VM 看到了第一台的写入: %v", err)
	}
	// 镜像本身不变:摘要保持一致,新实例看不到实例上的写入。
	if image.Digest() != digest {
		t.Fatalf("镜像摘要改变:%s → %s", digest, image.Digest())
	}
	third, err := image.Instantiate(t.Context(), Options{Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = third.Close(context.Background()) }()
	if _, err := third.Filesystem().Open(t.Context(), "/work/only-first", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("新实例继承了旧实例的写入: %v", err)
	}
	// 断开连接不改变镜像。
	connection, err := first.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if image.Digest() != digest {
		t.Fatalf("断开连接后镜像摘要改变:%s → %s", digest, image.Digest())
	}
}

// writeFile 在 VM 的客体文件系统里写入一个文件。
func writeFile(t *testing.T, machine *VM, path, content string) error {
	t.Helper()
	file, err := machine.Filesystem().Open(t.Context(), path, vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte(content)); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
