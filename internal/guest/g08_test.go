package guest_test

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
)

// TestSyscallTraceRecordsGuestCalls 验证 G08:真实客体程序经内核执行
// 时,受限轨迹记录下 PID/TID、调用编号、结果与耗时;轨迹不含任何
// 路径、参数或缓冲区内容。
func TestSyscallTraceRecordsGuestCalls(t *testing.T) {
	kernel, _, code := runGuestOnKernel(t, guest.SessionProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
	entries := kernel.Trace().Entries()
	if len(entries) == 0 {
		t.Fatal("未记录任何系统调用轨迹")
	}
	sawWrite, sawExit := false, false
	for _, entry := range entries {
		if entry.PID == 0 || entry.TID == 0 {
			t.Fatalf("轨迹缺少进程标识: %+v", entry)
		}
		switch entry.Number {
		case 64: // write
			sawWrite = true
			if entry.Result != 10 {
				t.Fatalf("write 结果=%d want 10", entry.Result)
			}
		case 93: // exit
			sawExit = true
		}
	}
	if !sawWrite {
		t.Fatal("轨迹缺少 write 调用")
	}
	if !sawExit {
		t.Fatal("轨迹缺少 exit 调用")
	}
}

// TestSyscallTraceStaysBounded 验证 G08:轨迹条数有界——上限收紧后
// 更早的条目被丢弃并计数,长时间运行的 VM 不无界占用宿主内存。
func TestSyscallTraceStaysBounded(t *testing.T) {
	kernel, system := guestKernel(t, guest.SessionProbe())
	defer func() { _ = kernel.Close(context.Background()) }()
	kernel.SetTraceLimit(4)
	// 再执行一次探针:调用次数远超上限。
	if _, code := runGuestOnKernelWith(t, kernel, system, guest.SessionProbe()); code != 0 {
		t.Fatalf("重跑探针退出码=%d want 0", code)
	}
	if kernel.Trace().Len() > 4 {
		t.Fatalf("保留条数=%d want ≤4", kernel.Trace().Len())
	}
	if kernel.Trace().Dropped() == 0 {
		t.Fatal("超出上限的条目应被丢弃并计数")
	}
}
