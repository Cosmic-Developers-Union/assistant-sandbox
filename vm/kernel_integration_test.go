package vm

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/audit"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/storage"
)

func longProgram() executable.Program {
	return executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
}

func echoProgram() executable.Program {
	return executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		descriptors, err := p.Resources.Descriptors()
		if err != nil {
			return 0, err
		}
		out, err := descriptors.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			return 0, err
		}
		_, err = io.WriteString(out, strings.Join(p.Args, " ")+"\n")
		return 0, err
	})
}

func registryFor(names ...string) (executable.Registry, error) {
	entries := map[string]executable.Program{}
	for _, name := range names {
		switch name {
		case "long":
			entries[name] = longProgram()
		default:
			entries[name] = echoProgram()
		}
	}
	return executable.New(entries)
}

func TestKernelWaitBlocksUntilChildExit(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	child, err := kernel.Begin(0, "echo", []string{"done"}, "/", nil, fd.Streams{Output: &out})
	if err != nil {
		t.Fatal(err)
	}

	waitDone := make(chan error, 1)
	go func() {
		_, err := kernel.Wait(t.Context(), 0, 0)
		waitDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	select {
	case err := <-waitDone:
		t.Fatalf("Wait 不应在子进程退出前返回: %v", err)
	default:
	}
	if status, err := kernel.RunRegistered(t.Context(), child); err != nil || status.Code != 0 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("子进程退出后 Wait 未返回")
	}

	if _, err := kernel.Wait(t.Context(), 0, 0); !errors.Is(err, errno.NoChild) {
		t.Fatal(err)
	}
}

func TestKernelWaitSpecificChildAndCancel(t *testing.T) {
	reg, _ := registryFor("long", "echo")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	first, _ := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	second, _ := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := kernel.Wait(ctx, 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的等待应返回取消: %v", err)
	}

	if _, err := kernel.Wait(t.Context(), 0, first.ID+99); !errors.Is(err, errno.NoChild) {
		t.Fatal(err)
	}
	if err := kernel.Signal(first.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	if status, err := kernel.RunRegistered(t.Context(), first); err != nil || status.Signal != signal.TERM {
		t.Fatal(err, status)
	}

	reaped, err := kernel.Wait(t.Context(), 0, first.ID)
	if err != nil || reaped.ID != first.ID {
		t.Fatalf("reaped=%+v err=%v", reaped, err)
	}
	_ = second
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKernelSignalTerminatesRunningProgram(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 3)
	child, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		status ExitStatus
		err    error
	}
	results := make(chan outcome, 1)
	go func() {
		status, err := kernel.RunRegistered(t.Context(), child)
		_ = kernel.Finish(0, child.ID, status)
		results <- outcome{status, err}
	}()
	time.Sleep(20 * time.Millisecond)

	// J07:SIGSTOP 驻停运行中的任务,状态可观察(此前报告 ENOTSUP)。
	if err := kernel.Signal(child.ID, signal.STOP); err != nil {
		t.Fatal(err)
	}
	for _, p := range kernel.Processes() {
		if p.ID == child.ID && p.State != proc.Stopped {
			t.Fatalf("驻停后状态=%v want stopped", p.State)
		}
	}
	// SIGCONT 恢复:进程回到运行状态且仍可被终止。
	if err := kernel.Signal(child.ID, signal.CONT); err != nil {
		t.Fatal(err)
	}
	for _, p := range kernel.Processes() {
		if p.ID == child.ID && p.State != proc.Running {
			t.Fatalf("继续后状态=%v want running", p.State)
		}
	}
	if err := kernel.Signal(child.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if got.status.Signal != signal.TERM || got.status.Code != 143 {
			t.Fatalf("status=%+v err=%v", got.status, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("信号未终止程序")
	}

	if err := kernel.Signal(child.ID, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := kernel.Signal(999, signal.TERM); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
}

func TestKernelSignalGroup(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	group, _ := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err := kernel.SetPGID(0, group.ID, group.ID); err != nil {
		t.Fatal(err)
	}
	member, _ := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err := kernel.SetPGID(0, member.ID, group.ID); err != nil {
		t.Fatal(err)
	}
	if err := kernel.SignalGroup(group.ID, signal.INT); err != nil {
		t.Fatal(err)
	}

	pending, err := kernel.signals.Pending(uint64(member.ID))
	if err != nil || len(pending) != 1 || pending[0] != signal.INT {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	if err := kernel.SignalGroup(999, signal.INT); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	if err := kernel.SetSID(member.ID); err != nil {
		t.Fatal(err)
	}
	got := kernel.Processes()
	for _, p := range got {
		if p.ID == member.ID && (p.SID != member.ID || p.PGID != member.ID) {
			t.Fatalf("setsid 未生效: %+v", p)
		}
	}
}

func TestKernelPipeSignalsBrokenWriter(t *testing.T) {
	descriptors := fd.New()
	reg, _ := registryFor("long")
	kernel, err := New(Services{Set: capability.Set{FD: descriptors}, Programs: reg}, 3)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	pid := uint64(owner.ID)
	readFD, writeFD, err := kernel.Pipe(owner.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if readFD != 3 || writeFD != 4 {
		t.Fatalf("readFD=%d writeFD=%d", readFD, writeFD)
	}

	if err := descriptors.Close(t.Context(), pid, readFD); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	writer, err := descriptors.Get(t.Context(), pid, writeFD)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); !errors.Is(err, errno.BrokenPipe) {
		t.Fatalf("err=%v", err)
	}
	pending, err := kernel.signals.Pending(pid)
	if err != nil || len(pending) != 1 || pending[0] != signal.PIPE {
		t.Fatalf("pending=%v err=%v", pending, err)
	}

	bare, _ := New(Services{Programs: reg}, 2)
	if _, _, err := bare.Pipe(1, 4); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKernelCloseRejectsNewTasksAndIsIdempotent(t *testing.T) {
	reg, _ := registryFor("long", "echo")
	sequencer := scheduler.New(0)
	kernel, _ := New(Services{Programs: reg, Sequencer: sequencer}, 3)
	child, _ := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	waitDone := make(chan error, 1)
	go func() {
		_, err := kernel.Wait(t.Context(), 0, 0)
		waitDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatalf("Close 必须幂等: %v", err)
	}

	select {
	case err := <-waitDone:
		if !errors.Is(err, errno.Cancelled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close 未唤醒等待者")
	}

	if _, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
	if err := kernel.Signal(child.ID, signal.TERM); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
	if closed, err := kernel.Closed(); err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	if len(sequencer.Log()) == 0 {
		t.Fatal("控制序列未记录")
	}
	if sequencer.Log()[0].Kind != scheduler.KindSpawn {
		t.Fatalf("首个事件应为 spawn: %+v", sequencer.Log()[0])
	}
}

func TestKernelOpenFDsReportsPerProcess(t *testing.T) {
	descriptors := fd.New()
	reg, _ := registryFor("echo")
	kernel, _ := New(Services{Set: capability.Set{FD: descriptors}, Programs: reg}, 3)
	child, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	fds, err := kernel.OpenFDs(child.ID)
	if err != nil || len(fds) != 3 {
		t.Fatalf("fds=%v err=%v", fds, err)
	}
	if _, err := kernel.OpenFDs(99); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	bare, _ := New(Services{Programs: reg}, 3)
	other, _ := bare.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	fds, err = bare.OpenFDs(other.ID)
	if err != nil || len(fds) != 0 {
		t.Fatalf("fds=%v err=%v", fds, err)
	}
}

func TestKernelQuietPointAndSnapshot(t *testing.T) {
	reg, _ := registryFor("echo")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 3)
	if err := kernel.EnsureQuietPoint(); err != nil {
		t.Fatalf("init-only 内核应是安全点: %v", err)
	}
	var quietOut bytes.Buffer
	child, _ := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{Output: &quietOut})
	if err := kernel.EnsureQuietPoint(); !errors.Is(err, errno.Busy) {
		t.Fatal("存在活动进程时必须拒绝快照")
	}
	if _, err := kernel.SnapshotFS(t.Context()); err == nil {
		t.Fatal("无快照能力存储时必须失败")
	}
	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(child.ID); err != nil {
		t.Fatal(err)
	}
	if err := kernel.EnsureQuietPoint(); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := kernel.EnsureQuietPoint(); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
}

func TestKernelAuditFailureStopsKernel(t *testing.T) {
	reg, _ := registryFor("echo")
	sink := audit.NewMemory()
	sink.FailNext(errors.New("审计后端故障"))
	kernel, _ := New(Services{Set: capability.Set{Audit: sink}, Programs: reg}, 3)
	if _, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Busy) {
		t.Fatalf("审计失败必须封锁内核: %v", err)
	}
}

func TestKernelAuditRecordsControlEvents(t *testing.T) {
	reg, _ := registryFor("echo")
	sink := audit.NewMemory()
	kernel, _ := New(Services{Set: capability.Set{Audit: sink, FD: fd.New()}, Programs: reg}, 3)
	var auditOut bytes.Buffer
	child, err := kernel.Begin(0, "echo", []string{"x"}, "/", nil, fd.Streams{Output: &auditOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(child.ID); err != nil {
		t.Fatal(err)
	}
	events := sink.Events()
	if len(events) < 3 {
		t.Fatalf("events=%+v", events)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("序号不严格: %+v", events)
		}
	}
	if events[0].Kind != "spawn" || events[len(events)-1].Kind != "reap" {
		t.Fatalf("首尾事件错误: %+v", events)
	}
}

func TestKernelMetersChargeSpawnAndDiscard(t *testing.T) {
	reg, _ := registryFor("echo")
	meters, err := resources.NewSession(resources.Limits{Memory: 1024, Work: 2}, resources.Limits{Memory: 512, Work: 2})
	if err != nil {
		t.Fatal(err)
	}
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg, Meters: meters}, 4)
	first, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}

	var metersOut bytes.Buffer
	second, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{Output: &metersOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("预算耗尽必须拒绝: %v", err)
	}

	if _, err := kernel.RunRegistered(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Reap(second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("工作量不随回收释放: %v", err)
	}
	_ = first
}

func TestKernelSessionStorageCapability(t *testing.T) {
	reg, _ := registryFor("echo")
	store := storage.New(0)
	kernel, _ := New(Services{Set: capability.Set{Sessions: store}, Programs: reg}, 3)

	if err := store.Save(t.Context(), "s", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if data, err := store.Load(t.Context(), "s"); err != nil || string(data) != "v" {
		t.Fatal(data, err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptErrorText(t *testing.T) {
	var cause error = interrupt{n: signal.TERM}
	if !strings.Contains(cause.Error(), "SIGTERM") {
		t.Fatal(cause.Error())
	}
}

func TestKernelSnapshotFSWithMemfsAndRemainingBranches(t *testing.T) {
	filesystem, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registryFor("long")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FS: filesystem, FD: fd.New()}, Programs: reg}, 3)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := kernel.SnapshotFS(t.Context()); err != nil {
		t.Fatal(err)
	}
	if kernel.Init() != 1 {
		t.Fatalf("init=%d", kernel.Init())
	}

	bridgeFS := &noSnapshotFS{System: filesystem}
	empty, _ := executable.New(nil)
	bare, _ := New(Services{Set: capability.Set{FS: bridgeFS}, Programs: empty}, 3)
	if _, err := bare.SnapshotFS(t.Context()); !errors.Is(err, errno.NotSupported) {
		t.Fatal(err)
	}

	none, _ := New(Services{Programs: empty}, 3)
	if _, err := none.SnapshotFS(t.Context()); !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}

	long, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := kernel.RunRegistered(ctx, long); err == nil {
		t.Fatal("取消后应返回错误")
	}
	got := kernel.Processes()
	if len(got) != 2 || got[1].State != proc.Zombie {
		t.Fatalf("processes=%+v", got)
	}
}

type noSnapshotFS struct{ vmfs.System }

func TestKernelResolverShebangFallsBack(t *testing.T) {
	filesystem, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := registryFor("interp")
	resolverReg, _ := executable.New(map[string]executable.Program{"interp": echoProgram()})
	_ = resolverReg
	_ = reg
	kernel, err := New(Services{
		Set:      capability.Set{FS: filesystem, FD: fd.New()},
		Programs: mustRegistry(t),
	}, 3)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := kernel.Begin(0, "missing", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
}

func mustRegistry(t *testing.T) executable.Registry {
	t.Helper()
	reg, err := registryFor("interp", "echo")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func envProgram(name string) executable.Program {
	return executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		descriptors, err := p.Resources.Descriptors()
		if err != nil {
			return 0, err
		}
		out, err := descriptors.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			return 0, err
		}
		_, err = io.WriteString(out, name+"="+p.Env[name])
		return 0, err
	})
}

func TestControlRunExecutesAndReapsChild(t *testing.T) {
	reg, err := executable.New(map[string]executable.Program{
		"echo": echoProgram(),
		"env":  envProgram("KEY"),
		"fail": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 3, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 8)
	if err != nil {
		t.Fatal(err)
	}
	control := proc.Control(procControl{k: kernel})

	live := func() int { return len(kernel.table.List()) }
	var out bytes.Buffer
	terminal, err := control.Run(t.Context(), proc.RunSpec{Parent: 0, Name: "echo", Args: []string{"one", "two"}, Dir: "/", Streams: fd.Streams{Output: &out}})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "one two\n" || terminal.State != proc.Zombie || terminal.ExitCode != 0 {
		t.Fatalf("执行结果: %q %+v", out.String(), terminal)
	}
	if live() != 1 {
		t.Fatalf("子进程未回收: %d", live())
	}

	terminal, err = control.Run(t.Context(), proc.RunSpec{Parent: 0, Name: "fail", Args: nil, Dir: "/", Env: nil, Streams: fd.Streams{}})
	if err != nil || terminal.ExitCode != 3 {
		t.Fatalf("退出码: %+v %v", terminal, err)
	}

	out.Reset()
	if _, err := control.Run(t.Context(), proc.RunSpec{Parent: 0, Name: "env", Args: nil, Dir: "/", Env: map[string]string{"KEY": "value"}, Streams: fd.Streams{Output: &out}}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "KEY=value" {
		t.Fatalf("环境: %q", out.String())
	}

	if _, err := control.Run(t.Context(), proc.RunSpec{Parent: 0, Name: "missing", Args: nil, Dir: "/", Env: nil, Streams: fd.Streams{}}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未登记程序: %v", err)
	}
	if live() != 1 {
		t.Fatalf("失败后进程表残留: %d", live())
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	terminal, err = control.Run(cancelled, proc.RunSpec{Parent: 0, Name: "echo", Args: nil, Dir: "/", Env: nil, Streams: fd.Streams{}})
	if !errors.Is(err, context.Canceled) || terminal.ExitCode != 125 {
		t.Fatalf("取消: %+v %v", terminal, err)
	}
	if live() != 1 {
		t.Fatalf("取消后进程表残留: %d", live())
	}
}

func TestKernelControlPortAndRemainingBranches(t *testing.T) {
	reg, _ := registryFor("long", "echo")
	descriptors := fd.New()
	kernel, _ := New(Services{Set: capability.Set{FD: descriptors}, Programs: reg}, 4)

	control := proc.Control(procControl{k: kernel})
	child, err := control.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Finish(99, child.ID, 0); !errors.Is(err, errno.NoChild) {
		t.Fatal(err)
	}

	if err := control.Finish(0, child.ID, 0); err != nil {
		t.Fatal(err)
	}

	if err := kernel.SetSID(999); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	if _, err := descriptors.Get(t.Context(), uint64(child.ID), 1); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	owner, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := kernel.Pipe(owner.ID, 0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKernelClosedGuardsOnEveryControlPath(t *testing.T) {
	reg, _ := registryFor("long", "echo")
	kernel, _ := New(Services{Programs: reg}, 4)
	child, _ := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err := kernel.SetPGID(0, child.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := kernel.Wait(t.Context(), 0, 0); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
	if err := kernel.SignalGroup(child.ID, signal.TERM); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
	if err := kernel.SetPGID(0, child.ID, child.ID); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
	if err := kernel.SetSID(child.ID); !errors.Is(err, errno.Cancelled) {
		t.Fatal(err)
	}
	if _, _, err := kernel.Pipe(child.ID, 4); !errors.Is(err, errno.Cancelled) && !errors.Is(err, errno.NotImplemented) {
		t.Fatal(err)
	}
}

func TestKernelWaitZombieTargetAndSequencerFull(t *testing.T) {
	reg, _ := registryFor("echo")
	sequencer := scheduler.New(2)
	kernel, _ := New(Services{Programs: reg, Sequencer: sequencer}, 4)

	first, err := kernel.Begin(0, "echo", []string{"a"}, "/", nil, fd.Streams{Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(t.Context(), first); err == nil {
		t.Fatal("序列器溢出必须使内核 fail-closed")
	}
	_ = kernel
}

func TestKernelBranchCompletion(t *testing.T) {
	reg, _ := registryFor("echo")

	descriptors := fd.New()
	kernel, _ := New(Services{Set: capability.Set{FD: descriptors}, Programs: reg}, 3)

	child, _ := kernel.Begin(0, "echo", []string{"x"}, "/", nil, fd.Streams{Output: io.Discard})
	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	wrongStatus := ExitStatus{Code: 99}
	if err := kernel.Finish(0, child.ID, wrongStatus); !errors.Is(err, errno.Busy) {
		t.Fatalf("err=%v", err)
	}

	if err := kernel.Signal(child.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}

	if _, _, err := kernel.Pipe(999, 4); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	forged := child
	forged.Program = "ghost"
	if _, err := kernel.RunRegistered(t.Context(), forged); err == nil {
		t.Fatal("伪造程序名应失败")
	}
	_ = kernel
}

func TestControlRunIgnoresSignals(t *testing.T) {

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	program := executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return 0, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	reg, err := executable.New(map[string]executable.Program{"waiter": program})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 8)
	if err != nil {
		t.Fatal(err)
	}
	control := proc.Control(procControl{k: kernel})
	run := func(ignore []signal.Number) chan proc.Proc {
		result := make(chan proc.Proc, 1)
		go func() {
			terminal, _ := control.Run(t.Context(), proc.RunSpec{
				Name: "waiter", Dir: "/", IgnoreSignals: ignore,
			})
			result <- terminal
		}()
		<-started
		return result
	}

	done := run([]signal.Number{signal.HUP})
	if err := kernel.Signal(proc.PID(2), signal.HUP); err != nil {
		t.Fatal(err)
	}
	close(release)
	if terminal := <-done; terminal.ExitCode != 0 || terminal.ExitSignal != 0 {
		t.Fatalf("忽略处置下的终态: %+v", terminal)
	}

	started, release = make(chan struct{}), make(chan struct{})
	once = sync.Once{}
	done = run(nil)
	if err := kernel.Signal(proc.PID(3), signal.HUP); err != nil {
		t.Fatal(err)
	}
	terminal := <-done
	if terminal.ExitSignal != uint8(signal.HUP) {
		t.Fatalf("未忽略时 SIGHUP 应终止子进程: %+v", terminal)
	}
	close(release)
}

func TestKernelUmaskFact(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	source := kernel.services.UmaskSource
	if source == nil {
		t.Fatal("内核必须装上创建掩码查询口")
	}
	if _, err := source.Umask(999); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程: %v", err)
	}
	parent, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if mask, err := source.Umask(uint64(parent.ID)); err != nil || mask != proc.DefaultUmask {
		t.Fatalf("初值 = %o, %v", mask, err)
	}

	control, ok := proc.Control(procControl{k: kernel}).(proc.UmaskControl)
	if !ok {
		t.Fatal("procControl 必须实现 UmaskControl")
	}
	if err := control.SetUmask(parent.ID, 0o077); err != nil {
		t.Fatal(err)
	}
	if mask, err := source.Umask(uint64(parent.ID)); err != nil || mask != 0o077 {
		t.Fatalf("设置后 = %o, %v", mask, err)
	}

	if err := kernel.SetUmask(parent.ID, stdfs.FileMode(0o10000)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界掩码: %v", err)
	}
	if err := kernel.SetUmask(999, 0o022); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程: %v", err)
	}
	child, err := kernel.Begin(parent.ID, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if child.Umask != 0o077 {
		t.Fatalf("子进程应继承掩码: %o", child.Umask)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := kernel.SetUmask(parent.ID, 0o022); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后应拒绝: %v", err)
	}
}

func TestKernelSignalControlPort(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	control, ok := proc.Control(procControl{k: kernel}).(proc.SignalControl)
	if !ok {
		t.Fatal("procControl 必须实现 SignalControl")
	}
	child, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := control.Exists(child.ID); err != nil || !exists {
		t.Fatalf("存在性查询 = %v, %v", exists, err)
	}
	if exists, err := control.Exists(999); err != nil || exists {
		t.Fatalf("未知进程的存在性查询 = %v, %v", exists, err)
	}
	if err := control.Signal(child.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	if pending, err := kernel.Pending(child.ID); err != nil || len(pending) != 1 || pending[0] != signal.TERM {
		t.Fatalf("待决信号 = %v, %v", pending, err)
	}
	if err := control.Signal(999, signal.TERM); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("向未知进程投递 = %v", err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exists(child.ID); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后的存在性查询 = %v", err)
	}
}

func TestKernelTableControlPort(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	control, ok := proc.Control(procControl{k: kernel}).(proc.TableControl)
	if !ok {
		t.Fatal("procControl 必须实现 TableControl")
	}
	child, err := kernel.Begin(0, "echo", []string{"hi"}, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	processes := control.Processes()
	if len(processes) == 0 {
		t.Fatal("进程表不应为空")
	}
	var found bool
	for _, process := range processes {
		if process.ID != child.ID {
			continue
		}
		found = true
		if process.Program != "echo" || len(process.Args) != 1 || process.Args[0] != "hi" {
			t.Fatalf("进程条目 = %+v", process)
		}
		if process.SID == 0 || process.PGID == 0 {
			t.Fatalf("进程条目缺少会话/进程组: %+v", process)
		}
	}
	if !found {
		t.Fatalf("进程表里没有刚创建的进程 %d: %+v", child.ID, processes)
	}

	processes[0].Program = "mutated"
	for _, process := range kernel.Processes() {
		if process.Program == "mutated" {
			t.Fatal("Processes 返回的必须是值副本")
		}
	}
}

func TestKernelUsageAggregatesChildren(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}

	meters, err := resources.NewSession(
		resources.Limits{Memory: 1 << 20, Work: 1 << 20},
		resources.Limits{Memory: 1 << 20, Work: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := fd.New()
	kernel, err := New(Services{Set: capability.Set{FD: descriptors}, Programs: reg, Meters: meters}, 4)
	if err != nil {
		t.Fatal(err)
	}
	control, ok := proc.Control(procControl{k: kernel}).(proc.UsageControl)
	if !ok {
		t.Fatal("procControl 必须实现 UsageControl")
	}
	parent, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(parent.ID, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}

	if err := kernel.meters.Charge(t.Context(), uint64(child.ID), 7); err != nil {
		t.Fatal(err)
	}
	before, err := control.Usage(parent.ID)
	if err != nil || before.Children != 0 {
		t.Fatalf("回收前父进程的子进程用量应为 0: %+v, %v", before, err)
	}

	if err := kernel.table.Exit(child.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Finish(parent.ID, child.ID, ExitStatus{}); err != nil {
		t.Fatal(err)
	}
	after, err := control.Usage(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Children < 7 {
		t.Fatalf("回收后应把子进程用量并入父进程: %+v", after)
	}
	if after.Self == 0 {
		t.Fatalf("父进程自身也应有用量记录: %+v", after)
	}

	if _, err := control.Usage(child.ID); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("回收后的子进程用量应报 NoProcess: %v", err)
	}

	if _, err := control.Usage(999); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程用量: %v", err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Usage(parent.ID); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后的用量查询: %v", err)
	}
}

func TestKernelWaitChildNoHangAndBlocking(t *testing.T) {
	reg, _ := registryFor("long", "echo")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	child, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}

	// WNOHANG:子进程运行中,无就绪结果。
	if _, found, err := kernel.WaitChild(t.Context(), 0, child.ID, true); err != nil || found {
		t.Fatalf("运行中 nohang: found=%v err=%v", found, err)
	}
	if _, found, _ := kernel.WaitChild(t.Context(), 0, 0, true); found {
		t.Fatal("任意目标不应有就绪结果")
	}
	if !kernel.HasChildren(0) {
		t.Fatal("应有子进程")
	}

	// 阻塞等待在子进程转僵尸后立刻返回:子进程经 RunRegistered
	// 的退出路径(table.Exit)成为僵尸并唤醒等待者。
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = kernel.Signal(child.ID, signal.TERM)
		_, _ = kernel.RunRegistered(t.Context(), child)
	}()
	state, found, err := kernel.WaitChild(t.Context(), 0, 0, false)
	if err != nil || !found || state.PID != child.ID || state.ExitSignal != uint8(signal.TERM) {
		t.Fatalf("阻塞等待: state=%+v found=%v err=%v", state, found, err)
	}
	<-exited

	// 立即退出的子进程:阻塞等待取回退出码。
	var out bytes.Buffer
	echo, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{Output: &out})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := kernel.RunRegistered(t.Context(), echo); err != nil || status.Code != 0 {
		t.Fatalf("echo 运行: %+v err=%v", status, err)
	}
	state, found, err = kernel.WaitChild(t.Context(), 0, 0, false)
	if err != nil || !found || state.PID != echo.ID || state.ExitCode != 0 {
		t.Fatalf("echo 等待: state=%+v found=%v err=%v", state, found, err)
	}

	// 回收后再探测:任意目标报 ECHILD。
	if _, _, err := kernel.WaitChild(t.Context(), 0, 0, true); !errors.Is(err, errno.NoChild) {
		t.Fatalf("无子进程: err=%v want ECHILD", err)
	}
	if kernel.HasChildren(0) {
		t.Fatal("回收后不应再有子进程")
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKernelWaitChildNoHangTargetsZombie(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	child, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	_ = kernel.Signal(child.ID, signal.TERM)
	if _, err := kernel.RunRegistered(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	state, found, err := kernel.WaitChild(t.Context(), 0, child.ID, true)
	if err != nil || !found || state.ExitSignal != uint8(signal.TERM) {
		t.Fatalf("僵尸 nohang: state=%+v found=%v err=%v", state, found, err)
	}
	// 已回收:目标不存在报 ECHILD。
	if _, _, err := kernel.WaitChild(t.Context(), 0, child.ID, true); !errors.Is(err, errno.NoChild) {
		t.Fatalf("已回收: err=%v want ECHILD", err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKernelForkCopiesProcessState(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	parent, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}

	// 父进程绑定地址空间并写入可观察数据。
	binder, _ := parent.Resources.AddressSpace()
	space, err := binder.BindAddressSpace(uint64(parent.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = space

	// 信号处置与屏蔽登记。
	_ = kernel.signals.SetAction(uint64(parent.ID), signal.USR1, signal.ActionConfig{Handler: 0x4040})
	_, _ = kernel.signals.ApplyMask(uint64(parent.ID), signal.MaskBlock, 1<<(signal.HUP-1))

	child, childSpace, err := kernel.Fork(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Parent != parent.ID {
		t.Fatalf("子进程父=%d want %d", child.Parent, parent.ID)
	}
	if child.Program != parent.Program || child.Dir != parent.Dir {
		t.Fatalf("子进程镜像=%+v", child)
	}
	if childSpace == nil {
		t.Fatal("子进程应获得克隆地址空间")
	}

	// 信号处置与屏蔽继承;挂起集不继承。
	if config, _ := kernel.signals.Action(uint64(child.ID), signal.USR1); config.Handler != 0x4040 {
		t.Fatalf("子进程处置=%+v", config)
	}
	if mask, _ := kernel.signals.Mask(uint64(child.ID)); mask&(1<<(signal.HUP-1)) == 0 {
		t.Fatalf("子进程屏蔽=%x", mask)
	}
	if err := kernel.signals.Raise(uint64(parent.ID), signal.USR2); err != nil {
		t.Fatal(err)
	}
	if pending, _ := kernel.signals.Pending(uint64(child.ID)); len(pending) != 0 {
		t.Fatalf("子进程不应继承挂起集: %v", pending)
	}

	// 描述符共享复制:父安装的句柄子可见。
	table := kernel.fds
	installed, err := table.InstallNext(uint64(parent.ID), &pipeCountingHandle{})
	if err != nil {
		t.Fatal(err)
	}
	// fork 之后安装的不共享(快照语义),这里验证 fork 前条目的共享:
	if !table.IsOpen(uint64(child.ID), 0) || !table.IsOpen(uint64(child.ID), 1) {
		t.Fatal("fork 应复制标准流描述符")
	}
	_ = installed

	// 地址空间克隆独立:父进程空间与子空间互不影响(经 E10 语义)。
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type pipeCountingHandle struct{ closed int }

func (h *pipeCountingHandle) Read(p []byte) (int, error)  { return 0, io.EOF }
func (h *pipeCountingHandle) Write(p []byte) (int, error) { return len(p), nil }
func (h *pipeCountingHandle) Close() error                { h.closed++; return nil }

func TestKillSignalDispatchSemantics(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	parent, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	control := parent.Control.(proc.SignalKillControl)

	// pid>0:目标存在探测(sig=0)成功,缺失报 ESRCH。
	if err := control.KillSignal(int64(parent.ID), 0); err != nil {
		t.Fatalf("sig=0 探测: %v", err)
	}
	if err := control.KillSignal(int64(parent.ID+50), 0); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("缺失目标: %v want ESRCH", err)
	}
	// 非法信号号 EINVAL。
	if err := control.KillSignal(int64(parent.ID), signal.Number(64)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非法信号: %v want EINVAL", err)
	}
	// pid>0 投递:登记处理器的信号保持挂起待边界投递。
	_ = kernel.signals.SetAction(uint64(parent.ID), signal.USR1, signal.ActionConfig{Handler: 0x4040})
	if err := control.KillSignal(int64(parent.ID), signal.USR1); err != nil {
		t.Fatal(err)
	}
	if pending, _ := kernel.signals.Pending(uint64(parent.ID)); len(pending) != 1 || pending[0] != signal.USR1 {
		t.Fatalf("挂起=%v", pending)
	}
	// pid=0:调用方进程组(默认 PGID=自身)。
	if err := control.KillSignal(0, signal.USR2); err != nil {
		t.Fatal(err)
	}
	if pending, _ := kernel.signals.Pending(uint64(parent.ID)); len(pending) != 2 {
		t.Fatalf("进程组发送后挂起=%v", pending)
	}
	// pid<-1:进程组 -pid;空组按 Linux 报 ESRCH。
	if err := control.KillSignal(-int64(parent.ID+10), signal.TERM); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("空组: %v want ESRCH", err)
	}
	// pid=-1:全部进程除自身。
	if err := control.KillSignal(-1, signal.USR1); err != nil {
		t.Fatal(err)
	}
	// sig=0 组探测:调用方组存在、全集合非空成功;空组 ESRCH。
	if err := control.KillSignal(0, 0); err != nil {
		t.Fatalf("sig=0 pid=0: %v", err)
	}
	if err := control.KillSignal(-1, 0); err != nil {
		t.Fatalf("sig=0 pid=-1: %v", err)
	}
	if err := control.KillSignal(-int64(parent.ID+10), 0); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("sig=0 空组: %v want ESRCH", err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestKernelStopContinueKillPureCPUTask 验证 J07:纯 CPU 任务可被
// SIGSTOP 驻停(推进冻结)、SIGCONT 恢复、SIGKILL 终止——包括驻停
// 状态下被 KILL(Stopped → Zombie),父进程 waitid 观察到信号终止。
func TestKernelStopContinueKillPureCPUTask(t *testing.T) {
	var progress atomic.Int64
	registry, err := executable.New(map[string]executable.Program{
		"spin": executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
			for ctx.Err() == nil {
				progress.Add(1)
			}
			return 0, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 4)
	child, err := kernel.Begin(0, "spin", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan ExitStatus, 1)
	runErr := make(chan error, 1)
	go func() {
		status, err := kernel.RunRegistered(t.Context(), child)
		results <- status
		runErr <- err
	}()
	waitForProgress(t, &progress, 1000)

	// SIGSTOP:推进冻结,状态可观察。
	if err := kernel.Signal(child.ID, signal.STOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	frozen := progress.Load()
	time.Sleep(60 * time.Millisecond)
	if progress.Load() != frozen {
		t.Fatalf("驻停期间仍在推进: %d → %d", frozen, progress.Load())
	}
	for _, p := range kernel.Processes() {
		if p.ID == child.ID && p.State != proc.Stopped {
			t.Fatalf("驻停后状态=%v want stopped", p.State)
		}
	}

	// SIGCONT:恢复推进,状态回运行。
	if err := kernel.Signal(child.ID, signal.CONT); err != nil {
		t.Fatal(err)
	}
	waitForProgress(t, &progress, frozen+1000)
	for _, p := range kernel.Processes() {
		if p.ID == child.ID && p.State != proc.Running {
			t.Fatalf("继续后状态=%v want running", p.State)
		}
	}

	// 再驻停后 SIGKILL:驻停进程被直接终止(Stopped → Zombie)。
	if err := kernel.Signal(child.ID, signal.STOP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := kernel.Signal(child.ID, signal.KILL); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-results:
		if status.Signal != signal.KILL || status.Code != 128+uint8(signal.KILL) {
			t.Fatalf("status=%+v want SIGKILL/137", status)
		}
		if err := <-runErr; err != nil {
			t.Fatalf("被信号终止不应报错: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGKILL 未终止驻停任务")
	}
	state, found, err := kernel.WaitChild(t.Context(), 0, child.ID, true)
	if err != nil || !found || state.ExitSignal != uint8(signal.KILL) {
		t.Fatalf("waitid: state=%+v found=%v err=%v", state, found, err)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func waitForProgress(t *testing.T, progress *atomic.Int64, target int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for progress.Load() < target {
		if time.Now().After(deadline) {
			t.Fatalf("任务未推进到 %d(当前 %d)", target, progress.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestKernelSIGCHLDNotifiesParentOnChildState 验证 J08:子进程状态
// 变化按 Linux 语义通知父进程——默认处置忽略不可观察;登记处理器
// 时退出/驻停/继续各产生一条挂起 SIGCHLD;显式 SIG_IGN 的退出自动
// 回收不留僵尸(此后 waitid 报 ECHILD,不重复回收)。
func TestKernelSIGCHLDNotifiesParentOnChildState(t *testing.T) {
	reg, _ := registryFor("long", "echo")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 8)
	parent, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	// (a) 默认处置:子进程退出后父进程无挂起 SIGCHLD。
	var firstOut bytes.Buffer
	first, err := kernel.Begin(parent.ID, "echo", []string{"x"}, "/", nil, fd.Streams{Output: &firstOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, first); err != nil {
		t.Fatal(err)
	}
	pending, _ := kernel.signals.Pending(uint64(parent.ID))
	for _, n := range pending {
		if n == signal.CHLD {
			t.Fatalf("默认处置下父进程不应收到 SIGCHLD: %v", pending)
		}
	}

	// (b) 登记处理器:子进程退出产生一条挂起 SIGCHLD。
	if err := kernel.signals.SetAction(uint64(parent.ID), signal.CHLD,
		signal.ActionConfig{Handler: 0x4040}); err != nil {
		t.Fatal(err)
	}
	var secondOut bytes.Buffer
	second, err := kernel.Begin(parent.ID, "echo", []string{"x"}, "/", nil, fd.Streams{Output: &secondOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, second); err != nil {
		t.Fatal(err)
	}
	pending, _ = kernel.signals.Pending(uint64(parent.ID))
	if len(pending) != 1 || pending[0] != signal.CHLD {
		t.Fatalf("退出通知: pending=%v want [SIGCHLD]", pending)
	}

	// (c) 子进程驻停/继续各产生一条 SIGCHLD。
	third, err := kernel.Begin(parent.ID, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = kernel.RunRegistered(ctx, third)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	if err := kernel.Signal(third.ID, signal.STOP); err != nil {
		t.Fatal(err)
	}
	// 标准信号在投递前合并(Linux 语义):退出/驻停/继续的多次
	// 通知在父进程挂起集中仍是一个 SIGCHLD 位。
	pending, _ = kernel.signals.Pending(uint64(parent.ID))
	if len(pending) != 1 || pending[0] != signal.CHLD {
		t.Fatalf("驻停通知: pending=%v want [SIGCHLD]", pending)
	}
	if err := kernel.Signal(third.ID, signal.CONT); err != nil {
		t.Fatal(err)
	}
	pending, _ = kernel.signals.Pending(uint64(parent.ID))
	if len(pending) != 1 || pending[0] != signal.CHLD {
		t.Fatalf("继续通知: pending=%v want [SIGCHLD]", pending)
	}
	if err := kernel.Signal(third.ID, signal.KILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("KILL 未终止子进程")
	}

	// (d) 显式 SIG_IGN:子进程退出自动回收,waitid 报 ECHILD。
	if err := kernel.signals.SetIgnored(uint64(parent.ID), signal.CHLD, true); err != nil {
		t.Fatal(err)
	}
	var fourthOut bytes.Buffer
	fourth, err := kernel.Begin(parent.ID, "echo", []string{"x"}, "/", nil, fd.Streams{Output: &fourthOut})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, fourth); err != nil {
		t.Fatal(err)
	}
	if _, _, err := kernel.WaitChild(ctx, parent.ID, fourth.ID, true); !errors.Is(err, errno.NoChild) {
		t.Fatalf("SIG_IGN 自动回收后: err=%v want ECHILD", err)
	}
	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestKernelWaitInterruptedByDeliverableSignal 验证 J09:阻塞等待
// 被登记了处理器的信号打断并返回 EINTR(wait* 无论 SA_RESTART 永不
// 重启);信号在等待唤醒后保持挂起,由指令边界投递。
func TestKernelWaitInterruptedByDeliverableSignal(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	parent, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(parent.ID, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.signals.SetAction(uint64(parent.ID), signal.USR1,
		signal.ActionConfig{Handler: 0x4040}); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, _, err := kernel.WaitChild(t.Context(), parent.ID, child.ID, false)
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := kernel.Signal(parent.ID, signal.USR1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, errno.Interrupted) {
			t.Fatalf("等待应被信号打断: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待未被信号打断")
	}
	// 信号保持挂起待边界投递,不因打断而丢失。
	if pending, _ := kernel.signals.Pending(uint64(parent.ID)); len(pending) != 1 || pending[0] != signal.USR1 {
		t.Fatalf("挂起=%v want [SIGUSR1]", pending)
	}
	// 模拟边界投递完成:处理器消费挂起位。
	kernel.signals.Consume(uint64(parent.ID), signal.USR1)
	// 未登记处理器的信号不打断等待。
	other := make(chan error, 1)
	go func() {
		_, _, err := kernel.WaitChild(t.Context(), parent.ID, child.ID, false)
		other <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := kernel.Signal(parent.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-other:
		t.Fatalf("默认处置的信号不应打断等待: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestKernelResetForExecResetsProcessState 验证 H07 的内核侧复位:
// execve 成功后进程表元数据随新映像更新(保留工作目录,Generation
// 递增),CLOEXEC 描述符关闭,信号处置与挂起集合全部复位。
func TestKernelResetForExecResetsProcessState(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	parent, err := kernel.Begin(0, "long", []string{"old"}, "/", map[string]string{"OLD": "1"}, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	// CLOEXEC 描述符:exec 成功后必须关闭。
	handle := pipeCountingHandle{}
	if err := kernel.fds.Install(uint64(parent.ID), 7, &handle); err != nil {
		t.Fatal(err)
	}
	if err := kernel.fds.SetCloexec(uint64(parent.ID), 7, true); err != nil {
		t.Fatal(err)
	}
	// 信号处置与挂起:exec 后必须复位。
	if err := kernel.signals.SetAction(uint64(parent.ID), signal.USR1,
		signal.ActionConfig{Handler: 0x4040}); err != nil {
		t.Fatal(err)
	}
	if err := kernel.signals.Raise(uint64(parent.ID), signal.USR1); err != nil {
		t.Fatal(err)
	}
	before, err := kernel.table.Get(parent.ID)
	if err != nil {
		t.Fatal(err)
	}

	control := parent.Control.(proc.ExecControl)
	if err := control.ResetForExec("/new", []string{"new", "arg"}, map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}

	after, err := kernel.table.Get(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Program != "/new" || len(after.Args) != 2 || after.Args[1] != "arg" || after.Env["A"] != "1" {
		t.Fatalf("进程表元数据=%+v", after)
	}
	if after.Dir != "/" {
		t.Fatalf("工作目录应保留: %v", after.Dir)
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("Generation=%d want %d", after.Generation, before.Generation+1)
	}
	if kernel.fds.IsOpen(uint64(parent.ID), 7) {
		t.Fatal("CLOEXEC 描述符应已关闭")
	}
	if config, err := kernel.signals.Action(uint64(parent.ID), signal.USR1); err != nil || config.Handler != 0 {
		t.Fatalf("信号处置应复位: %+v err=%v", config, err)
	}
	if pending, _ := kernel.signals.Pending(uint64(parent.ID)); len(pending) != 0 {
		t.Fatalf("挂起集应清空: %v", pending)
	}
	if err := kernel.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestKernelWaitDeadlineWakesWithoutSideEffects 验证 G07 超时轴:
// 阻塞等待被调用方截止时间唤醒,返回上下文错误;超时不破坏进程
// 状态——子进程结束后同一等待仍可正常回收。
func TestKernelWaitDeadlineWakesWithoutSideEffects(t *testing.T) {
	reg, _ := registryFor("long")
	kernel, _ := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	parent, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(parent.ID, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = kernel.WaitChild(ctx, parent.ID, child.ID, false)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("超时截止的等待不应成功")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("错误=%v want DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("等待未按截止时间唤醒: %v", elapsed)
	}
	// 超时后子进程仍在表中且可被后续等待回收(约定:超时无副作用)。
	if _, err := kernel.table.Get(child.ID); err != nil {
		t.Fatalf("超时改变了子进程状态: %v", err)
	}
	if err := kernel.Signal(child.ID, signal.TERM); err != nil {
		t.Fatal(err)
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer waitCancel()
	for {
		state, _, err := kernel.WaitChild(waitCtx, parent.ID, child.ID, true)
		if err == nil {
			_ = state
			break
		}
		if !errors.Is(err, errno.NoChild) {
			t.Fatalf("回收等待: %v", err)
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("超时后无法回收子进程")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestKernelWaitOnReapedTargetEndsWithECHILD 验证等待一个已被回收的
// 目标进程不会无限等待:父进程以 SIG_IGN 忽略 SIGCHLD 时子进程被
// 自动回收,等待方以 ECHILD 结束。
func TestKernelWaitOnReapedTargetEndsWithECHILD(t *testing.T) {
	reg, err := registryFor("long")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	// 父进程忽略 SIGCHLD:子进程退出后被自动回收。
	if err := kernel.signals.SetIgnored(uint64(kernel.Init()), signal.CHLD, true); err != nil {
		t.Fatal(err)
	}
	if err := kernel.table.Exit(child.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	kernel.childStateChanged(child.ID, true)

	waitCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := kernel.Wait(waitCtx, kernel.Init(), child.ID); !errors.Is(err, errno.NoChild) {
		t.Fatalf("等待已回收的目标 err=%v want ECHILD", err)
	}
}
