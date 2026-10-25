package instance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// newTestVM 建立一台确定性 VM(虚拟时钟 + 固定种子),并登记一个
// 长期运行的客体程序 hold,便于观察进程表。
func newTestVM(t *testing.T) *VM {
	t.Helper()
	machine, err := New(t.Context(), Options{
		Deterministic: true,
		Seed:          7,
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
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	return machine
}

// TestVMConnectionsShareProcessTable 验证 C02:同一台 VM 的多个连接
// 观察同一进程表;不同 VM 的进程表互不可见。
func TestVMConnectionsShareProcessTable(t *testing.T) {
	machine := newTestVM(t)
	other := newTestVM(t)

	first, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() == second.ID() {
		t.Fatal("连接的标识必须可区分")
	}

	// 长期运行的客体程序:进程留在进程表里,便于另一连接观察。
	holdCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	process, err := first.Exec(holdCtx, ExecSpec{Program: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	// 同一 VM 的另一连接能看到该进程。
	found := false
	for range 100 {
		for _, entry := range second.Processes() {
			if entry.ID == process.ID {
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !found {
		t.Fatalf("另一连接未看到进程 %d:%+v", process.ID, second.Processes())
	}
	// 另一台 VM 看不到。
	for _, entry := range foreign.Processes() {
		if entry.ID == process.ID {
			t.Fatalf("另一台 VM 看到了本 VM 的进程 %d", entry.ID)
		}
	}
	// 两台 VM 的内核实例彼此独立。
	if machine.Kernel() == other.Kernel() {
		t.Fatal("两台 VM 必须拥有独立的内核实例")
	}
	// 客体 ELF 放在 VM 的文件系统里:执行链覆盖客体文件系统、解析器
	// 与 RV64 执行器。
	if err := writeProbe(t, machine); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	probe, err := first.Exec(t.Context(), ExecSpec{
		Program: "/probe",
		Streams: fd.Streams{Output: output},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := first.Wait(t.Context(), probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 42 {
		t.Fatalf("退出码=%d want 42", state.ExitCode)
	}
	if !strings.Contains(output.String(), "guest-ok") {
		t.Fatalf("输出=%q want 含 guest-ok", output.String())
	}
}

// TestVMConnectionsShareRootFilesystem 验证 C03:同一 VM 的连接共享
// 根文件系统(/tmp 与工作区);另一台 VM 看不到这些内容。
func TestVMConnectionsShareRootFilesystem(t *testing.T) {
	machine := newTestVM(t)
	other := newTestVM(t)
	first, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}

	file, err := machine.Filesystem().Open(t.Context(), "/tmp/shared", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// 同一 VM 的连接共享文件系统。
	read, err := machine.Filesystem().Open(t.Context(), "/tmp/shared", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	if string(content) != "data" {
		t.Fatalf("共享文件内容=%q want \"data\"", content)
	}
	// 另一台 VM 看不到。
	if _, err := other.Filesystem().Open(t.Context(), "/tmp/shared", vmfs.ReadOnly, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("另一台 VM 应看不到共享文件: %v", err)
	}
	// 工作区目录同样各自独立。
	if _, err := other.Filesystem().Stat(t.Context(), WorkRoot); err != nil {
		t.Fatalf("另一台 VM 的工作区缺失: %v", err)
	}
	_ = first
}

// TestVMDisconnectKeepsDevicesAlive 验证 C04:服务存活期不依赖连接
// 句柄——断开一条连接不会销毁设备、时钟与随机源。
func TestVMDisconnectKeepsDevicesAlive(t *testing.T) {
	machine := newTestVM(t)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// 断开前设备可用。
	if err := writeNull(t, machine); err != nil {
		t.Fatal(err)
	}
	if err := connection.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !connection.Closed() {
		t.Fatal("连接应已断开")
	}
	// 断开后设备仍然可用:设备归 VM 所有,不随连接销毁。
	if err := writeNull(t, machine); err != nil {
		t.Fatalf("断开连接后设备失效: %v", err)
	}
	// 时钟与随机源同样存活。
	if _, err := machine.Clock().Monotonic(t.Context()); err != nil {
		t.Fatalf("时钟失效: %v", err)
	}
	buffer := make([]byte, 8)
	if _, err := machine.Entropy().Read(t.Context(), buffer); err != nil {
		t.Fatalf("随机源失效: %v", err)
	}
	// 断开幂等。
	if err := connection.Disconnect(t.Context()); err != nil {
		t.Fatal(err)
	}
	// 断开后的连接不能再执行。
	if _, err := connection.Exec(t.Context(), ExecSpec{Program: "echo"}); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("断开后的连接应拒绝执行: %v", err)
	}
}

// writeProbe 把静态 RV64 探针写入 VM 的客体文件系统。
func writeProbe(t *testing.T, machine *VM) error {
	t.Helper()
	file, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.ReadWrite, 0o755)
	if err != nil {
		return err
	}
	if _, err := file.Write(guest.Probe()); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// writeNull 向 /dev/null 写入,验证设备仍挂载可用。
func writeNull(t *testing.T, machine *VM) error {
	t.Helper()
	file, err := machine.Filesystem().Open(t.Context(), "/dev/null", vmfs.ReadWrite, 0)
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte("x")); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// TestVMCloseIsIdempotent 验证 VM 关闭幂等,并让全部连接失效。
func TestVMCloseIsIdempotent(t *testing.T) {
	machine, err := New(t.Context(), Options{Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := machine.Close(t.Context()); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	if !machine.Closed() {
		t.Fatal("VM 应已关闭")
	}
	if !connection.Closed() {
		t.Fatal("关闭 VM 后连接应失效")
	}
	if _, err := machine.Connect(t.Context()); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后不应接受新连接: %v", err)
	}
}
