package instance

import (
	"os"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// TestRuntimeGapTracking 验证 Q02:真实 ELF 暴露缺失文件与缺失系统
// 调用时,内核记录首个缺口并生成可落盘的最小复现源码。
func TestRuntimeGapTracking(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/o02-gaps")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := New(t.Context(), Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
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
	run := func(mode string) {
		process, err := connection.Exec(t.Context(), ExecSpec{Program: "/probe", Args: []string{mode}, Streams: fd.Streams{}})
		if err != nil {
			t.Fatal(err)
		}
		state, err := connection.Wait(t.Context(), process.ID)
		if err != nil || state.ExitCode != 0 {
			t.Fatalf("%s 退出=%+v err=%v(断言码见 o02-gaps.c)", mode, state, err)
		}
	}
	run("file")
	run("syscall")

	var fileGap, syscallGap *vm.GapReport
	reports := machine.GapReports()
	for index := range reports {
		gap := &reports[index]
		switch gap.Kind {
		case "file":
			fileGap = gap
		case "syscall":
			syscallGap = gap
		}
	}
	if fileGap == nil || fileGap.Path != "/work/missing-gap" || !strings.Contains(fileGap.Repro, `open("/work/missing-gap"`) || !strings.Contains(fileGap.Repro, "O_RDONLY") {
		t.Fatalf("文件缺口=%+v", fileGap)
	}
	if syscallGap == nil || syscallGap.Syscall != 232 || !strings.Contains(syscallGap.Repro, "syscall(232") {
		t.Fatalf("系统调用缺口=%+v", syscallGap)
	}
	// 缺口复现源码可直接落盘,作为独立任务的最小用例。
	dir := t.TempDir()
	for index, gap := range machine.GapReports() {
		path := dir + "/gap-" + gap.Kind + ".c"
		if err := os.WriteFile(path, []byte(gap.Repro), 0o644); err != nil {
			t.Fatal(err)
		}
		if index == 0 && len(gap.Repro) == 0 {
			t.Fatal("空复现源码")
		}
	}
}
