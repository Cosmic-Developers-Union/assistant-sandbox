package instance

import (
	"errors"
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
	_, err := machine.Filesystem().Open(t.Context(), "/sys/kernel/osrelease",
		vmfs.ReadWrite, 0)
	if !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("sysfs 写入应报只读: %v", err)
	}
}
