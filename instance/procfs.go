package instance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/procfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// kernelProcfs 把 VM 内核的动态进程表与地址空间投影为 /proc(N06/N07):
// 每台 VM 独立,条目随进程退出/回收实时消失。
type kernelProcfs struct {
	kernel  *vm.Kernel
	machine *VM
}

const cgroupPath = "/assistant-sandbox"

const (
	namespaceCgroup = "cgroup"
	namespaceIPC    = "ipc"
	namespaceMnt    = "mnt"
	namespaceNet    = "net"
	namespacePID    = "pid"
	namespacePIDFor = "pid_for_children"
	namespaceTime   = "time"
	namespaceUser   = "user"
	namespaceUTS    = "uts"
)

var namespaceInodeSeed atomic.Uint64

// newNamespaceView 为每台 VM 生成一组稳定的 namespace 标识。同一 VM
// 的所有进程共享相同目标;不同 VM 不复用编号,避免把独立内核误报成
// 同一 namespace。
func newNamespaceView() map[string]string {
	base := 4026531834 + namespaceInodeSeed.Add(16)
	return map[string]string{
		namespaceCgroup: fmt.Sprintf("cgroup:[%d]", base+1),
		namespaceIPC:    fmt.Sprintf("ipc:[%d]", base+2),
		namespaceMnt:    fmt.Sprintf("mnt:[%d]", base+3),
		namespaceNet:    fmt.Sprintf("net:[%d]", base+4),
		namespacePID:    fmt.Sprintf("pid:[%d]", base+5),
		namespacePIDFor: fmt.Sprintf("pid:[%d]", base+5),
		namespaceTime:   fmt.Sprintf("time:[%d]", base+6),
		namespaceUser:   fmt.Sprintf("user:[%d]", base+7),
		namespaceUTS:    fmt.Sprintf("uts:[%d]", base+8),
	}
}

// SystemFiles 生成 /proc 顶层的系统视图文件(N08):
// 数值来自 VM 配置与挂载表,不读取宿主/proc。
func (p kernelProcfs) SystemFiles(ctx context.Context) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	memoryKB := p.machine.memoryBytes / 1024
	var usedMemory uint64
	if p.machine.meters != nil {
		if view, err := p.machine.meters.SessionView(ctx); err == nil {
			usedMemory = view.MemoryUsed / 1024
		}
	}
	var freeMemoryKB uint64
	if usedMemory < memoryKB {
		freeMemoryKB = memoryKB - usedMemory
	}
	meminfo := fmt.Sprintf("MemTotal:       %d kB\nMemFree:        %d kB\nMemAvailable:   %d kB\nBuffers:        0 kB\nCached:         0 kB\n",
		memoryKB, freeMemoryKB, freeMemoryKB)
	var cpu strings.Builder
	for index := 0; index < p.machine.cpus; index++ {
		fmt.Fprintf(&cpu, "processor\t: %d\nmodel name\t: Assistant Sandbox RV64GC\nisa\t\t: rv64imafdc\nmmu\t\t: none\n\n", index)
	}
	var mounts strings.Builder
	for _, point := range p.machine.mounts.Points() {
		fmt.Fprintf(&mounts, "sandbox %s vmfs rw 0 0\n", point)
	}
	if p.machine.shm != nil {
		fmt.Fprintf(&mounts, "tmpfs /dev/shm tmpfs rw,nosuid,nodev 0 0\n")
	}
	monotonic, err := p.machine.clock.Monotonic(ctx)
	if err != nil {
		return nil, err
	}
	wall, err := p.machine.clock.Wall(ctx)
	if err != nil {
		return nil, err
	}
	const nanosecondsPerSecond = 1_000_000_000
	uptime := float64(monotonic) / nanosecondsPerSecond
	bootTime := (wall - int64(monotonic)) / nanosecondsPerSecond //nolint:gosec // 纳秒读数在 int64 范围内
	processes := p.kernel.Processes()
	running := 0
	var lastPID proc.PID
	for _, process := range processes {
		if process.State == proc.Running {
			running++
		}
		if process.ID > lastPID {
			lastPID = process.ID
		}
	}
	var stat strings.Builder
	cpuTicks := monotonic / 10_000_000
	fmt.Fprintf(&stat, "cpu 0 0 0 %d 0 0 0 0 0 0\n", cpuTicks)
	for index := 0; index < p.machine.cpus; index++ {
		fmt.Fprintf(&stat, "cpu%d 0 0 0 %d 0 0 0 0 0 0\n", index, cpuTicks)
	}
	fmt.Fprintf(&stat, "intr 0\nctxt 0\nbtime %d\nprocesses %d\nprocs_running %d\nprocs_blocked 0\n",
		bootTime, len(processes), running)
	return map[string][]byte{
		"meminfo": []byte(meminfo),
		"cpuinfo": []byte(cpu.String()),
		"mounts":  []byte(mounts.String()),
		"stat":    []byte(stat.String()),
		"uptime":  fmt.Appendf(nil, "%.2f %.2f\n", uptime, uptime),
		"loadavg": fmt.Appendf(nil, "0.00 0.00 0.00 %d/%d %d\n", running, len(processes), lastPID),
	}, nil
}

func (p kernelProcfs) Processes(ctx context.Context) ([]procfs.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.entries(ctx, p.kernel.Processes())
}

// entries 从一次进程表快照构建 /proc 条目。快照后的进程退出只影响其
// 自身条目，不使稳定进程（例如 PID 1）的查询失败。
func (p kernelProcfs) entries(ctx context.Context, processes []proc.Proc) ([]procfs.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	threadCounts := make(map[proc.PID]int, len(processes))
	for _, item := range processes {
		threadCounts[item.TGID]++
	}
	out := make([]procfs.Entry, 0, len(processes))
	for _, item := range processes {
		descriptors, err := p.kernel.OpenFDs(item.ID)
		if err != nil {
			// Processes 与 OpenFDs 是两次独立快照；respawn/退出的子进程
			// 可能在两者之间消失。跳过该瞬时条目，不能让整个 /proc
			// 视图（包括稳定 PID 1）因此返回 ESRCH。
			if errors.Is(err, errno.NoProcess) {
				continue
			}
			return nil, err
		}
		out = append(out, procfs.Entry{
			ID: item.ID, Parent: item.Parent, Generation: item.Generation,
			Program: item.Program, Args: append([]string(nil), item.Args...),
			Dir: item.Dir, Env: item.Env, State: item.State.String(),
			ExitCode: item.ExitCode, ExitSignal: item.ExitSignal,
			PGID: item.PGID, SID: item.SID,
			ThreadCount: threadCounts[item.TGID],
			Exe:         item.Program, Maps: p.maps(ctx, item.ID),
			OpenFDs:    descriptors,
			Namespaces: p.machine.namespaces,
			Cgroup:     "0::" + cgroupPath + "\n",
		})
	}
	return out, nil
}

func (p kernelProcfs) Self(ctx context.Context) (proc.PID, error) {
	caller, err := proc.FromContext(ctx)
	if err != nil {
		return 0, err
	}
	return caller.ID, nil
}

// maps 渲染 /proc/<pid>/maps 的最小实测视图:每个地址区间一段,
// 权限来自实际映射,未绑定地址空间时为空。
func (p kernelProcfs) maps(ctx context.Context, pid proc.PID) string {
	space, err := p.kernel.AddressSpace(pid)
	if err != nil || space == nil {
		return ""
	}
	regions, err := space.Regions(ctx)
	if err != nil {
		return ""
	}
	var builder strings.Builder
	for _, region := range regions {
		name := region.Name
		if name == "" {
			name = "[anon]"
		}
		fmt.Fprintf(&builder, "%08x-%08x %s %08x 00:00 0 %s\n",
			region.Start, region.End, protString(region.Prot), region.FileBase, name)
	}
	return builder.String()
}

func protString(prot memory.Prot) string {
	value := []byte{'-', '-', '-'}
	if prot&memory.ProtRead != 0 {
		value[0] = 'r'
	}
	if prot&memory.ProtWrite != 0 {
		value[1] = 'w'
	}
	if prot&memory.ProtExec != 0 {
		value[2] = 'x'
	}
	return string(value)
}
