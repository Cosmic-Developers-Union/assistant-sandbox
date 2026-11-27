package instance

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// buildSysfs 构造最小只读 sysfs 视图。当前暴露内核版本、CPU 拓扑、
// transparent hugepage 与 clocksource 等开发工具常用的只读属性；
// 设备枚举与可调参数仍由后续 S4 工作补齐。
func buildSysfs(ctx context.Context, machine *VM) (vmfs.System, error) {
	base, err := memfs.New(machine.clock, memfs.Options{
		MaxNodes: 256, MaxFileBytes: 1 << 20, TotalBytes: 4 << 20,
	})
	if err != nil {
		return nil, err
	}
	type sysfsFile struct {
		name    string
		content string
	}
	files := []sysfsFile{
		{"/sys/kernel/ostype", "Linux\n"},
		{"/sys/kernel/osrelease", "6.8.0-assistant-sandbox\n"},
		{"/sys/kernel/version", "#1 SMP Assistant Sandbox\n"},
		{"/sys/kernel/hostname", "sandbox\n"},
		{"/sys/kernel/mm/transparent_hugepage/enabled", "always [madvise] never\n"},
		{"/sys/kernel/mm/transparent_hugepage/defrag", "always madvise [never]\n"},
		{"/sys/kernel/mm/transparent_hugepage/shmem_enabled", "always [never] deny force\n"},
		{"/sys/kernel/mm/transparent_hugepage/hpage_pmd_size", "2097152\n"},
		{"/sys/devices/system/clocksource/clocksource0/current_clocksource", "arch_timer\n"},
		{"/sys/devices/system/clocksource/clocksource0/available_clocksource", "arch_timer\n"},
		{"/sys/class/net/lo/address", "00:00:00:00:00:00\n"},
		{"/sys/class/net/lo/type", "772\n"},
		{"/sys/class/net/lo/operstate", "unknown\n"},
	}
	cpus := cpuRange(machine.cpus)
	files = append(files,
		sysfsFile{"/sys/devices/system/cpu/online", cpus + "\n"},
		sysfsFile{"/sys/devices/system/cpu/possible", cpus + "\n"},
		sysfsFile{"/sys/devices/system/cpu/present", cpus + "\n"},
		sysfsFile{"/sys/devices/system/cpu/isolated", "\n"},
		sysfsFile{"/sys/devices/system/cpu/offline", "\n"},
	)
	for cpu := range machine.cpus {
		prefix := fmt.Sprintf("/sys/devices/system/cpu/cpu%d", cpu)
		files = append(files,
			sysfsFile{prefix + "/online", "1\n"},
			sysfsFile{prefix + "/topology/core_id", fmt.Sprintf("%d\n", cpu)},
			sysfsFile{prefix + "/topology/physical_package_id", "0\n"},
			sysfsFile{prefix + "/topology/core_siblings_list", cpus + "\n"},
			sysfsFile{prefix + "/topology/thread_siblings_list", fmt.Sprintf("%d\n", cpu)},
		)
	}
	for _, file := range files {
		name := strings.TrimPrefix(file.name, "/sys")
		if err := writeSysfsFile(ctx, base, name, file.content); err != nil {
			return nil, err
		}
	}
	if err := mkdirSysfsAll(ctx, base, "/fs/cgroup"); err != nil {
		return nil, err
	}
	return readonly.New(base)
}

func cpuRange(cpus int) string {
	if cpus <= 1 {
		return "0"
	}
	return fmt.Sprintf("0-%d", cpus-1)
}

func writeSysfsFile(ctx context.Context, system vmfs.System, name, content string) error {
	if err := mkdirSysfsAll(ctx, system, path.Dir(name)); err != nil {
		return err
	}
	file, err := system.Open(ctx, name, vmfs.Create|vmfs.Truncate|vmfs.WriteOnly, 0o444)
	if err != nil {
		return err
	}
	if _, err := file.Write([]byte(content)); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func mkdirSysfsAll(ctx context.Context, system vmfs.System, name string) error {
	if info, err := system.Stat(ctx, name); err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("%s 不是目录", name)
	}
	parent := path.Dir(name)
	if parent != name {
		if err := mkdirSysfsAll(ctx, system, parent); err != nil {
			return err
		}
	}
	return system.Mkdir(ctx, name, 0o755)
}
