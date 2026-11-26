package instance

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// TestSysfsExposesReadOnlySystemView 验证 S4 基础 sysfs 合同:
// 内核与 CPU 属性可读,且不能从客体侧修改。
func TestSysfsExposesReadOnlySystemView(t *testing.T) {
	machine := newTestVM(t)
	release := readProcFile(t, machine, t.Context(), "/sys/kernel/osrelease")
	if !strings.Contains(release, "assistant-sandbox") {
		t.Fatalf("osrelease=%q", release)
	}
	online := readProcFile(t, machine, t.Context(), "/sys/devices/system/cpu/online")
	if strings.TrimSpace(online) == "" {
		t.Fatal("cpu online 为空")
	}
	procRoot := "/proc/" + strconv.FormatUint(uint64(machine.Kernel().Init()), 10)
	cgroup := readProcFile(t, machine, t.Context(), procRoot+"/cgroup")
	if !strings.Contains(cgroup, "0::/assistant-sandbox") {
		t.Fatalf("cgroup=%q", cgroup)
	}
	controllers := readProcFile(t, machine, t.Context(), "/sys/fs/cgroup/cgroup.controllers")
	if !strings.Contains(controllers, "memory") {
		t.Fatalf("cgroup controllers=%q", controllers)
	}
	memoryMax := readProcFile(t, machine, t.Context(), "/sys/fs/cgroup/memory.max")
	if strings.TrimSpace(memoryMax) == "" {
		t.Fatal("cgroup memory.max 为空")
	}
	namespace, err := machine.Filesystem().Readlink(t.Context(), procRoot+"/ns/mnt")
	if err != nil || !strings.HasPrefix(namespace, "mnt:[") {
		t.Fatalf("mnt namespace=%q err=%v", namespace, err)
	}
	_, err = machine.Filesystem().Open(t.Context(), "/sys/kernel/osrelease",
		vmfs.ReadWrite, 0)
	if !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("sysfs 写入应报只读: %v", err)
	}
}
