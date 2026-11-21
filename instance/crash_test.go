package instance

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// TestGuestCrashReportCapturesStateAndMappings 验证 T02:真实 ELF 触发
// SIGSEGV 后报告 PID/TID、PC/寄存器、映射与最近调用,重复执行得到
// 相同故障点,并可序列化为可复现快照。
func TestGuestCrashReportCapturesStateAndMappings(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/m-fault")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := New(t.Context(), Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
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
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := func() uint64 {
		process, err := connection.Exec(t.Context(), ExecSpec{Program: "/probe", Args: []string{"crash"}, Dir: "/work", Streams: fd.Streams{}})
		if err != nil {
			t.Fatal(err)
		}
		state, err := connection.Wait(t.Context(), process.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.ExitSignal != uint8(signal.SEGV) {
			t.Fatalf("崩溃退出=%+v want SIGSEGV(%d)", state, signal.SEGV)
		}
		status, err := machine.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var report *vm.CrashReport
		for index := range status.Crashes {
			if status.Crashes[index].PID == uint64(process.ID) {
				report = &status.Crashes[index]
			}
		}
		if report == nil {
			t.Fatalf("缺少崩溃报告: %+v", status.Crashes)
		}
		if report.TID != uint64(process.ID) || report.TGID != uint64(process.ID) {
			t.Fatalf("崩溃线程标识=%+v", report)
		}
		if report.Program != "/probe" || report.Dir != "/work" || len(report.Args) != 2 || report.Args[1] != "crash" {
			t.Fatalf("崩溃程序身份=%+v", report)
		}
		if report.PC == 0 || len(report.Registers) != 32 || len(report.Floats) != 32 {
			t.Fatalf("崩溃 CPU 状态=%+v", report)
		}
		if !strings.Contains(report.Trap, "memory-fault") {
			t.Fatalf("崩溃类别=%q", report.Trap)
		}
		executable := false
		for _, mapping := range report.Mappings {
			if strings.Contains(mapping.Prot, "x") && mapping.Name != "" {
				executable = true
			}
		}
		if len(report.Mappings) == 0 || !executable {
			t.Fatalf("崩溃映射=%+v", report.Mappings)
		}
		if len(report.Recent) > 32 {
			t.Fatalf("最近调用未受限: %d", len(report.Recent))
		}
		encoded, err := json.Marshal(report)
		if err != nil || !strings.Contains(string(encoded), `"registers"`) || strings.Contains(string(encoded), "Resources") {
			t.Fatalf("崩溃报告不可序列化: %v %s", err, encoded)
		}
		return report.PC
	}
	first := run()
	second := run()
	if first != second {
		t.Fatalf("同一程序崩溃 PC 不一致: %#x vs %#x", first, second)
	}
	if reports := machine.Kernel().CrashReports(); len(reports) != 2 {
		t.Fatalf("崩溃报告数量=%d want 2", len(reports))
	}
}
