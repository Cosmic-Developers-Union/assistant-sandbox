package instance

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// TestCgroupViewMatchesProcessTable 验证 S4 的 cgroup v2 只读视图:
// 进程路径、cgroup.procs、pids.current、memory.max 与 VM 配置和内核
// 进程表一致;namespace 标识在 VM 内稳定、跨 VM 不复用。
func TestCgroupViewMatchesProcessTable(t *testing.T) {
	machine := newTestVM(t)
	other := newTestVM(t)
	initPID := machine.Kernel().Init()
	procPath := "/proc/" + itoaPID(initPID)

	cgroup := readProcFile(t, machine, t.Context(), procPath+"/cgroup")
	if cgroup != "0::/assistant-sandbox\n" {
		t.Fatalf("cgroup=%q", cgroup)
	}
	if rootProcs := readProcFile(t, machine, t.Context(), "/sys/fs/cgroup/cgroup.procs"); rootProcs != "" {
		t.Fatalf("根 cgroup.procs=%q", rootProcs)
	}

	processes := machine.Kernel().Processes()
	procs := readProcFile(t, machine, t.Context(), "/sys/fs/cgroup/assistant-sandbox/cgroup.procs")
	for _, process := range processes {
		line := strconv.FormatUint(uint64(process.ID), 10) + "\n"
		if !strings.Contains(procs, line) {
			t.Fatalf("cgroup.procs=%q 缺少 PID %d", procs, process.ID)
		}
	}
	currentText := strings.TrimSpace(readProcFile(t, machine, t.Context(),
		"/sys/fs/cgroup/assistant-sandbox/pids.current"))
	current, err := strconv.Atoi(currentText)
	if err != nil || current != len(processes) {
		t.Fatalf("pids.current=%q 进程数=%d err=%v", currentText, len(processes), err)
	}
	memoryMax := strings.TrimSpace(readProcFile(t, machine, t.Context(),
		"/sys/fs/cgroup/assistant-sandbox/memory.max"))
	if memoryMax != strconv.FormatUint(machine.memoryBytes, 10) {
		t.Fatalf("memory.max=%q want %d", memoryMax, machine.memoryBytes)
	}
	memoryCurrent := strings.TrimSpace(readProcFile(t, machine, t.Context(),
		"/sys/fs/cgroup/assistant-sandbox/memory.current"))
	if _, err := strconv.ParseUint(memoryCurrent, 10, 64); err != nil {
		t.Fatalf("memory.current=%q err=%v", memoryCurrent, err)
	}
	controllers := readProcFile(t, machine, t.Context(),
		"/sys/fs/cgroup/assistant-sandbox/cgroup.controllers")
	if controllers != "cpu memory pids\n" {
		t.Fatalf("controllers=%q", controllers)
	}

	namespaces := map[string]string{}
	for _, name := range []string{"cgroup", "ipc", "mnt", "net", "pid", "time", "user", "uts"} {
		target, err := machine.Filesystem().Readlink(t.Context(), procPath+"/ns/"+name)
		if err != nil {
			t.Fatalf("readlink ns/%s: %v", name, err)
		}
		if !strings.HasPrefix(target, name+":[") || !strings.HasSuffix(target, "]") {
			t.Fatalf("ns/%s=%q", name, target)
		}
		for otherName, otherTarget := range namespaces {
			if otherTarget == target {
				t.Fatalf("namespace %s 与 %s 共用标识 %q", name, otherName, target)
			}
		}
		namespaces[name] = target
	}
	pidChildren, err := machine.Filesystem().Readlink(t.Context(), procPath+"/ns/pid_for_children")
	if err != nil || pidChildren != namespaces["pid"] {
		t.Fatalf("pid_for_children=%q pid=%q err=%v", pidChildren, namespaces["pid"], err)
	}
	otherTarget, err := other.Filesystem().Readlink(t.Context(), "/proc/1/ns/mnt")
	if err != nil {
		t.Fatal(err)
	}
	if otherTarget == namespaces["mnt"] {
		t.Fatalf("两台 VM 的 mnt namespace 相同: %q", otherTarget)
	}

	_, err = machine.Filesystem().Open(t.Context(),
		"/sys/fs/cgroup/assistant-sandbox/pids.max", vmfs.WriteOnly, 0)
	if !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("写 cgroup 应报只读: %v", err)
	}
	if _, err := machine.Filesystem().Stat(t.Context(),
		"/sys/fs/cgroup/assistant-sandbox/not-exist"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未知 cgroup 文件 err=%v", err)
	}
}
