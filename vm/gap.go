package vm

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// GapReport 是运行时启动缺口(Q02):首个缺失文件、系统调用或指令,
// 附带可直接落盘的最小复现源码。值类型,可序列化。
type GapReport struct {
	Kind    string `json:"kind"`
	PID     uint64 `json:"pid"`
	Path    string `json:"path,omitempty"`
	Syscall uint64 `json:"syscall,omitempty"`
	PC      uint64 `json:"pc,omitempty"`
	Trap    string `json:"trap,omitempty"`
	Repro   string `json:"repro"`
}

func (k *Kernel) recordGap(report GapReport) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.gaps == nil {
		k.gaps = map[proc.PID]GapReport{}
	}
	pid := proc.PID(report.PID)
	if _, exists := k.gaps[pid]; exists {
		return
	}
	k.gaps[pid] = report
}

// MissingFileGap 记录进程首次缺失文件(Q02),用于启动缺口追踪。
func (k *Kernel) MissingFileGap(ctx context.Context, pid proc.PID, path string) {
	if err := ctx.Err(); err != nil || path == "" {
		return
	}
	k.recordGap(GapReport{
		Kind: "file", PID: uint64(pid), Path: path,
		Repro: fmt.Sprintf("#include <fcntl.h>\n#include <stdio.h>\nint main(void){ if (open(%s, O_RDONLY) < 0) { perror(\"open\"); return 1; } return 0; }\n", strconv.Quote(path)),
	})
}

// GapReports 返回保留的启动缺口(按首次登记顺序不保证;按 PID 排序由调用方处理)。
func (k *Kernel) GapReports() []GapReport {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]GapReport, 0, len(k.gaps))
	for _, report := range k.gaps {
		out = append(out, report)
	}
	return out
}

func syscallGapRepro(number uint64) string {
	return fmt.Sprintf("#include <unistd.h>\n#include <sys/syscall.h>\nint main(void){ return (int)syscall(%d, 0, 0, 0, 0, 0, 0); }\n", number)
}

func instructionGapRepro(trap string, pc uint64) string {
	return fmt.Sprintf("/* 缺失指令: %s at PC %#x */\n", trap, pc)
}
