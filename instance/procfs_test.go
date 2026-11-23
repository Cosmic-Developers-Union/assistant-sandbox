package instance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func readProcFile(t *testing.T, machine *VM, ctx context.Context, name string) string {
	t.Helper()
	file, err := machine.Filesystem().Open(ctx, name, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatalf("打开 %s: %v", name, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("读取 %s: %v", name, err)
	}
	return string(data)
}

// TestProcfsReflectsLiveProcessesPerVM 验证 N06/N07:/proc 是每 VM 独立
// 的动态视图,/proc/self 指向调用进程,exe/cwd/fd/status/cmdline/maps
// 与实际状态一致;进程回收后条目消失。
func TestProcfsReflectsLiveProcessesPerVM(t *testing.T) {
	machine := newTestVM(t)
	other := newTestVM(t)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	probe, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(guest.SpinForeverProbe()); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	process, err := connection.Exec(runCtx, ExecSpec{Program: "/probe", Dir: "/work", Streams: fd.Streams{}})
	if err != nil {
		t.Fatal(err)
	}

	caller := proc.WithContext(t.Context(), process)
	status := readProcFile(t, machine, caller, "/proc/self/status")
	if !strings.Contains(status, "Pid:\t"+itoaPID(process.ID)) || !strings.Contains(status, "Name:\t/probe") {
		t.Fatalf("status=%q", status)
	}
	cmdline := readProcFile(t, machine, caller, "/proc/self/cmdline")
	if !strings.HasPrefix(cmdline, "/probe") {
		t.Fatalf("cmdline=%q", cmdline)
	}
	if exe, err := machine.Filesystem().Readlink(caller, "/proc/self/exe"); err != nil || exe != "/probe" {
		t.Fatalf("exe=%q err=%v", exe, err)
	}
	if cwd, err := machine.Filesystem().Readlink(caller, "/proc/self/cwd"); err != nil || cwd != "/work" {
		t.Fatalf("cwd=%q err=%v", cwd, err)
	}
	descriptors, err := machine.Filesystem().ReadDir(caller, "/proc/self/fd")
	if err != nil || len(descriptors) != 3 {
		t.Fatalf("fd=%v err=%v", descriptors, err)
	}
	maps := ""
	deadlineMaps := time.Now().Add(2 * time.Second)
	for maps == "" {
		maps = readProcFile(t, machine, caller, "/proc/self/maps")
		if time.Now().After(deadlineMaps) {
			t.Fatal("maps 为空:地址空间未绑定")
		}
		time.Sleep(time.Millisecond)
	}
	if !strings.Contains(maps, "-") || !strings.Contains(maps, "r-x") {
		t.Fatalf("maps=%q", maps)
	}
	stat := readProcFile(t, machine, caller, "/proc/self/stat")
	fields := strings.Fields(stat)
	if len(fields) < 22 || fields[0] != itoaPID(process.ID) || !strings.HasPrefix(fields[1], "(/probe)") {
		t.Fatalf("stat=%q", stat)
	}

	// 每台 VM 独立:另一台 VM 看不到该 PID。
	if _, err := other.Filesystem().Stat(t.Context(), "/proc/"+itoaPID(process.ID)); !errors.Is(err, errno.NotFound) {
		t.Fatalf("跨 VM /proc 可见 err=%v", err)
	}
	// 退出并回收后条目及时消失。
	cancel()
	if _, err := connection.Wait(t.Context(), process.ID); err != nil {
		t.Fatalf("等待回收: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := machine.Filesystem().Stat(t.Context(), "/proc/"+itoaPID(process.ID))
		if errors.Is(err, errno.NotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("回收后 /proc 条目仍存在: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func itoaPID(pid proc.PID) string {
	if pid == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for pid > 0 {
		index--
		buffer[index] = byte('0' + pid%10)
		pid /= 10
	}
	return string(buffer[index:])
}

// TestGuestReadsProcSelf 验证 N06/N07 的真实客体路径:静态 musl ELF
// 经 open/read/readlink/opendir 读取 /proc/self,看到自己的 PID、exe、
// cwd、maps、fd 与 cmdline。
func TestGuestReadsProcSelf(t *testing.T) {
	machine := newTestVM(t)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile("../internal/guest/testdata/n06-procfs")
	if err != nil {
		t.Fatal(err)
	}
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
	output := &bytes.Buffer{}
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "/probe", Dir: "/work", Streams: fd.Streams{Output: output}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 || state.ExitSignal != 0 {
		t.Fatalf("探针退出=%+v 输出=%q(断言码见 n06-procfs.c)", state, output.String())
	}
	if got := output.String(); !strings.HasPrefix(got, "N06-OK pid=") {
		t.Fatalf("输出=%q", got)
	}
}

// TestProcSystemViewsReflectVMConfiguration 验证 N08:meminfo/cpuinfo/
// mounts 报告客体配置与挂载表,不读取宿主 /proc。
func TestProcSystemViewsReflectVMConfiguration(t *testing.T) {
	machine, err := New(t.Context(), Options{
		Deterministic: true, Seed: 1,
		MemoryBytes: 64 << 20,
		CPUs:        2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	meminfo := readProcFile(t, machine, t.Context(), "/proc/meminfo")
	if !strings.Contains(meminfo, "MemTotal:") || !strings.Contains(meminfo, "65536 kB") {
		t.Fatalf("meminfo=%q", meminfo)
	}
	cpuinfo := readProcFile(t, machine, t.Context(), "/proc/cpuinfo")
	if strings.Count(cpuinfo, "processor") != 2 || !strings.Contains(cpuinfo, "rv64imafdc") {
		t.Fatalf("cpuinfo=%q", cpuinfo)
	}
	mounts := readProcFile(t, machine, t.Context(), "/proc/mounts")
	for _, point := range []string{"/", "/dev", "/proc"} {
		if !strings.Contains(mounts, " "+point+" ") {
			t.Fatalf("mounts 缺少 %s:\n%s", point, mounts)
		}
	}
	stat := readProcFile(t, machine, t.Context(), "/proc/stat")
	if !strings.HasPrefix(stat, "cpu ") || !strings.Contains(stat, "btime ") ||
		!strings.Contains(stat, "processes ") {
		t.Fatalf("stat=%q", stat)
	}
	if uptime := readProcFile(t, machine, t.Context(), "/proc/uptime"); !strings.Contains(uptime, ".") {
		t.Fatalf("uptime=%q", uptime)
	}
	if loadavg := readProcFile(t, machine, t.Context(), "/proc/loadavg"); !strings.Contains(loadavg, "/") {
		t.Fatalf("loadavg=%q", loadavg)
	}
}
