package instance

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// State 是 VM 的管理面状态(T01)。
type State string

const (
	StateRunning State = "running"
	StateFailed  State = "failed"
	StateClosed  State = "closed"
)

// BudgetStatus 是资源预算使用快照(T01/T03):单位与配额一致,
// Memory 为字节、Work 为指令数、其余为条目/字节计数。
type BudgetStatus struct {
	MemoryUsed      uint64 `json:"memory_used"`
	MemoryLimit     uint64 `json:"memory_limit"`
	WorkUsed        uint64 `json:"work_used"`
	WorkLimit       uint64 `json:"work_limit"`
	DescriptorUsed  uint64 `json:"descriptor_used"`
	DescriptorLimit uint64 `json:"descriptor_limit"`
	OutputUsed      uint64 `json:"output_used"`
	OutputLimit     uint64 `json:"output_limit"`
	FileUsed        uint64 `json:"file_used"`
	FileLimit       uint64 `json:"file_limit"`
}

// ProcessStatus 是进程/线程的只读检查快照(T01):字段均为值类型,
// 不暴露内核指针或可写句柄。
type ProcessStatus struct {
	ID         uint64       `json:"id"`
	Parent     uint64       `json:"parent"`
	TID        uint64       `json:"tid"`
	TGID       uint64       `json:"tgid"`
	PGID       uint64       `json:"pgid"`
	SID        uint64       `json:"sid"`
	Program    string       `json:"program"`
	Args       []string     `json:"args"`
	Dir        string       `json:"dir"`
	State      string       `json:"state"`
	WaitReason string       `json:"wait_reason"`
	ExitCode   uint8        `json:"exit_code"`
	ExitSignal uint8        `json:"exit_signal"`
	Nice       int          `json:"nice"`
	FDs        []uint32     `json:"fds"`
	Budget     BudgetStatus `json:"budget"`
}

// NetworkStatus 是每 VM 网络预算使用量(T01/T03)。
type NetworkStatus struct {
	Connections int    `json:"connections"`
	Bytes       uint64 `json:"bytes"`
}

// TraceStatus 是系统调用轨迹的保留量(T01)。
type TraceStatus struct {
	Entries int    `json:"entries"`
	Dropped uint64 `json:"dropped"`
}

// StorageStatus 是挂载点容量快照(T01/T03)。
type StorageStatus struct {
	Name   string     `json:"name"`
	Root   string     `json:"root"`
	Bytes  vmfs.Quota `json:"bytes"`
	Inodes vmfs.Quota `json:"inodes"`
}

// Status 是 VM 的统一管理面快照(T01):状态、连接、进程/线程、
// 等待类别、fd 与预算使用。JSON 编码只包含上述值,不依赖内部指针。
type Status struct {
	State       State            `json:"state"`
	Connections int              `json:"connections"`
	Processes   []ProcessStatus  `json:"processes"`
	Session     BudgetStatus     `json:"session"`
	Network     NetworkStatus    `json:"network"`
	Trace       TraceStatus      `json:"trace"`
	Storage     []StorageStatus  `json:"storage"`
	Crashes     []vm.CrashReport `json:"crashes"`
}

// Status 返回 VM 的统一检查快照(T01)。调用方可在执行期间并发查询;
// 快照与内核状态之间不保证原子一致,但每个字段都是有效值。
func (v *VM) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	v.mu.Lock()
	closed := v.closed
	connections := len(v.connections)
	v.mu.Unlock()
	status := Status{State: StateRunning, Connections: connections}
	if closed {
		status.State = StateClosed
	}
	if v.kernel == nil {
		return status, nil
	}
	if !closed && v.kernel.Failed() {
		status.State = StateFailed
	}
	processes := v.kernel.Processes()
	status.Processes = make([]ProcessStatus, 0, len(processes))
	for _, process := range processes {
		entry := ProcessStatus{
			ID:         uint64(process.ID),
			Parent:     uint64(process.Parent),
			TID:        uint64(process.TID),
			TGID:       uint64(process.TGID),
			PGID:       uint64(process.PGID),
			SID:        uint64(process.SID),
			Program:    process.Program,
			Args:       append([]string(nil), process.Args...),
			Dir:        process.Dir,
			State:      process.State.String(),
			WaitReason: waitReason(process.State),
			ExitCode:   process.ExitCode,
			ExitSignal: process.ExitSignal,
			Nice:       process.Nice,
		}
		if fds, err := v.kernel.OpenFDs(process.ID); err == nil {
			entry.FDs = fds
		}
		if view, err := v.kernel.ProcessBudget(ctx, process.ID); err == nil {
			entry.Budget = budgetStatus(view)
		}
		status.Processes = append(status.Processes, entry)
	}
	if view, err := v.kernel.SessionBudget(ctx); err == nil {
		status.Session = budgetStatus(view)
	}
	usage := v.kernel.NetworkNamespace().Usage()
	status.Network = NetworkStatus{Connections: usage.Connections, Bytes: usage.Bytes}
	status.Trace = TraceStatus{Entries: v.kernel.Trace().Len(), Dropped: v.kernel.Trace().Dropped()}
	status.Crashes = v.kernel.CrashReports()
	if spaces, err := v.mounts.Filesystems(ctx); err == nil {
		status.Storage = make([]StorageStatus, 0, len(spaces))
		for _, space := range spaces {
			status.Storage = append(status.Storage, StorageStatus{
				Name: space.Name, Root: space.Root, Bytes: space.Bytes, Inodes: space.Inodes,
			})
		}
	}
	return status, nil
}

func budgetStatus(view resources.View) BudgetStatus {
	return BudgetStatus{
		MemoryUsed:      view.MemoryUsed,
		MemoryLimit:     view.MemoryLimit,
		WorkUsed:        view.WorkUsed,
		WorkLimit:       view.WorkLimit,
		DescriptorUsed:  view.DescriptorUsed,
		DescriptorLimit: view.DescriptorLimit,
		OutputUsed:      view.OutputUsed,
		OutputLimit:     view.OutputLimit,
		FileUsed:        view.FileUsed,
		FileLimit:       view.FileLimit,
	}
}

// waitReason 把进程状态映射为稳定的等待类别(T01):Linux 的阻塞
// 等待对应 interruptible,作业控制驻停对应 stopped。
func waitReason(state proc.State) string {
	switch state {
	case proc.Waiting:
		return "interruptible"
	case proc.Stopped:
		return "stopped"
	case proc.Zombie:
		return "exited"
	default:
		return ""
	}
}
