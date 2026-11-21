package vm

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/trace"
)

// CrashMapping 是崩溃报告中的一段客体地址区间(T02)。
type CrashMapping struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
	Prot  string `json:"prot"`
	Name  string `json:"name,omitempty"`
}

// CrashCall 是崩溃前最近一次有限系统调用记录(T02)。
type CrashCall struct {
	Number        uint64 `json:"number"`
	Result        int64  `json:"result"`
	DurationNanos int64  `json:"duration_nanos"`
}

// CrashReport 是客体致命故障的只读快照(T02):PID/TID、程序身份、
// 故障 CPU 状态、地址空间映射与最近有限调用。字段均为值类型,可
// 直接序列化;报告不持有内核指针。
type CrashReport struct {
	PID     uint64   `json:"pid"`
	TID     uint64   `json:"tid"`
	TGID    uint64   `json:"tgid"`
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Dir     string   `json:"dir"`

	Signal uint8  `json:"signal,omitempty"`
	Trap   string `json:"trap,omitempty"`

	PC        uint64   `json:"pc"`
	Registers []uint64 `json:"registers"`
	Floats    []uint64 `json:"floats"`
	FCSR      uint32   `json:"fcsr"`

	Mappings []CrashMapping `json:"mappings"`
	Recent   []CrashCall    `json:"recent_calls"`
}

const crashReportLimit = 64

// recordCrash 追加一份崩溃报告;保留最近 crashReportLimit 份。
func (k *Kernel) recordCrash(report CrashReport) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.crashes) >= crashReportLimit {
		copy(k.crashes, k.crashes[len(k.crashes)-crashReportLimit+1:])
		k.crashes = k.crashes[:crashReportLimit-1]
	}
	k.crashes = append(k.crashes, report)
}

// CrashReports 返回保留的崩溃报告(最旧在前,T02)。
func (k *Kernel) CrashReports() []CrashReport {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]CrashReport, len(k.crashes))
	for index, report := range k.crashes {
		out[index] = report
		out[index].Args = append([]string(nil), report.Args...)
		out[index].Registers = append([]uint64(nil), report.Registers...)
		out[index].Floats = append([]uint64(nil), report.Floats...)
		out[index].Mappings = append([]CrashMapping(nil), report.Mappings...)
		out[index].Recent = append([]CrashCall(nil), report.Recent...)
	}
	return out
}

// buildCrashReport 汇总一次致命故障(T02);调用方不得持有 k.mu。
func (k *Kernel) buildCrashReport(process proc.Proc, result executable.Result, recent []trace.Entry) CrashReport {
	report := CrashReport{
		PID:     uint64(process.ID),
		TID:     uint64(process.TID),
		TGID:    uint64(process.TGID),
		Program: process.Program,
		Args:    append([]string(nil), process.Args...),
		Dir:     process.Dir,
		Signal:  uint8(result.Signal),
	}
	if result.Diagnostic != nil {
		report.PC = result.Diagnostic.PC
		report.Registers = append([]uint64(nil), result.Diagnostic.Registers...)
		report.Floats = append([]uint64(nil), result.Diagnostic.Floats...)
		report.FCSR = result.Diagnostic.FCSR
		report.Trap = result.Diagnostic.Trap
	}
	if space, err := k.AddressSpace(process.ID); err == nil {
		if regions, err := space.Regions(context.Background()); err == nil {
			report.Mappings = make([]CrashMapping, 0, len(regions))
			for _, region := range regions {
				report.Mappings = append(report.Mappings, CrashMapping{
					Start: region.Start, End: region.End, Prot: region.Prot.String(), Name: region.Name,
				})
			}
		}
	}
	report.Recent = make([]CrashCall, 0, len(recent))
	for _, entry := range recent {
		report.Recent = append(report.Recent, CrashCall{
			Number: entry.Number, Result: entry.Result, DurationNanos: int64(entry.Duration),
		})
	}
	return report
}

// crashSignal 报告致命信号是否应生成崩溃报告(T02)。
func crashSignal(number signal.Number) bool {
	switch number {
	case signal.SEGV, signal.ILL, signal.BUS, signal.TRAP:
		return true
	default:
		return false
	}
}
