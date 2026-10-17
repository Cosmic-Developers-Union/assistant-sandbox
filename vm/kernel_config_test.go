package vm

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func testKernelForConfig(t *testing.T) (*Kernel, proc.Proc) {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{
		"cat": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New(), Clock: &clock.Virtual{}, FS: memory}, Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, "cat", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	return kernel, process
}

func TestSystemConfigurationIsDeclaredFacts(t *testing.T) {
	kernel, parent := testKernelForConfig(t)
	defer func() {
		if err := kernel.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	config, err := kernel.SystemConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if !config.BootDeclared {
		t.Fatal("装配了时钟就应声明开机时刻")
	}
	if config.BootTime != 0 {
		t.Fatalf("零值逻辑时钟的开机时刻应为 0: %d", config.BootTime)
	}
	if config.ChildMax <= 0 {
		t.Fatalf("进程预算应已声明: %d", config.ChildMax)
	}
	if config.ArgMax != proc.ArgMax {
		t.Fatalf("ArgMax 应与 proc.ArgMax 同源: %d != %d", config.ArgMax, proc.ArgMax)
	}

	control, ok := parent.Control.(proc.SystemControl)
	if !ok {
		t.Fatal("procControl 应实现 proc.SystemControl")
	}
	viaPort, err := control.SystemConfiguration()
	if err != nil || viaPort != config {
		t.Fatalf("端口读数 = %+v err=%v，内核 = %+v", viaPort, err, config)
	}
}

func TestHasProgramIsPureLookup(t *testing.T) {
	kernel, _ := testKernelForConfig(t)
	defer func() {
		if err := kernel.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if !kernel.HasProgram("cat") {
		t.Fatal("登记命令 cat 应可解析")
	}
	if !kernel.HasProgram("/bin/cat") {
		t.Fatal("/bin/cat 回落形式应可解析")
	}
	if kernel.HasProgram("definitely-not-a-command") {
		t.Fatal("未知名字不应可解析")
	}
	if kernel.HasProgram("/missing/dir/tool") {
		t.Fatal("缺失路径不应可解析")
	}
	if kernel.HasProgram("") {
		t.Fatal("空名字不应可解析")
	}
}
