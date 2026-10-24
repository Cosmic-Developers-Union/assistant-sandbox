package vm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/replay"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/scheduler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func TestKernelFailClosedAfterSequencerOverflow(t *testing.T) {
	ctx := context.Background()
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}

	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg, Sequencer: scheduler.New(1)}, 4)
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, child); err != nil {
		t.Fatal(err)
	}

	if _, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Busy) {
		t.Fatalf("fail-closed 后不得创建进程: %v", err)
	}
	if err := kernel.Finish(0, child.ID, ExitStatus{}); !errors.Is(err, errno.Busy) {
		t.Fatalf("fail-closed 后不得回收: %v", err)
	}
	if err := kernel.SetUmask(1, 0o022); !errors.Is(err, errno.Busy) {
		t.Fatalf("fail-closed 后不得改掩码: %v", err)
	}
	control := procControl{k: kernel, self: proc.PID(1)}
	if err := control.SetNice(1, 0); !errors.Is(err, errno.Busy) {
		t.Fatalf("fail-closed 后不得改优先级: %v", err)
	}

	if nice, err := control.Nice(1); err != nil || nice != proc.DefaultNice {
		t.Fatalf("优先级读应仍可用: %d %v", nice, err)
	}
	if _, err := kernel.Pending(child.ID); err != nil {
		t.Fatalf("待决信号读应仍可用: %v", err)
	}
}

func TestKernelCloseWakesWaitersAndInterruptsRunners(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	var once sync.Once
	program := executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return 0, ctx.Err()
	})
	reg, err := executable.New(map[string]executable.Program{"long": program, "echo": echoProgram()})
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
	runResult := make(chan ExitStatus, 1)
	go func() {
		status, _ := kernel.RunRegistered(ctx, child)
		runResult <- status
	}()
	<-started

	waitErr := make(chan error, 1)
	go func() {
		_, err := kernel.Wait(ctx, 0, child.ID)
		waitErr <- err
	}()
	for kernel.children.Waiters() == 0 {
		runtime.Gosched()
	}
	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waitErr:
		if !errors.Is(err, errno.Cancelled) {
			t.Fatalf("关闭应唤醒等待者并报取消: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭未唤醒等待者")
	}
	select {
	case status := <-runResult:
		if status.Signal != signal.TERM {
			t.Fatalf("关闭应以 SIGTERM 中断运行中的程序: %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭未中断运行中的程序")
	}

	if closed, err := kernel.Closed(); err != nil || !closed {
		t.Fatalf("Closed: %v %v", closed, err)
	}
	if kernel.HasProgram("long") {
		t.Fatal("关闭后不得再解析程序")
	}
	if err := kernel.EnsureQuietPoint(); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后没有安全点: %v", err)
	}
	if _, err := kernel.SnapshotTable(ctx); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后不得快照: %v", err)
	}
	if _, err := kernel.Pending(child.ID); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后不得读待决信号: %v", err)
	}
	if err := kernel.Close(ctx); err != nil {
		t.Fatalf("关闭必须幂等: %v", err)
	}
}

func TestKernelQuietPointRejectsBusyAndSnapshotCancellation(t *testing.T) {
	reg, err := registryFor("long")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.EnsureQuietPoint(); err != nil {
		t.Fatalf("只剩 init 时应是安全点: %v", err)
	}
	if _, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{}); err != nil {
		t.Fatal(err)
	}
	if err := kernel.EnsureQuietPoint(); !errors.Is(err, errno.Busy) {
		t.Fatalf("有活动进程时必须拒绝安全点: %v", err)
	}
	if _, err := kernel.SnapshotTable(t.Context()); !errors.Is(err, errno.Busy) {
		t.Fatalf("忙时不得快照: %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := kernel.SnapshotTable(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应先于表访问返回: %v", err)
	}
}

func TestKernelDescriptorEntriesRequireTable(t *testing.T) {
	ctx := context.Background()
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(1, 3, &fakeTerminal{}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("InstallFD: %v", err)
	}
	if err := kernel.CloseFD(1, 3); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("CloseFD: %v", err)
	}
	if _, _, err := kernel.Pipe(1, 8); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("Pipe: %v", err)
	}
	if _, err := kernel.ReadFD(ctx, 1, 0, make([]byte, 1)); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("ReadFD: %v", err)
	}
	if _, err := kernel.ReadContextFD(ctx, 1, 0, make([]byte, 1)); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("ReadContextFD: %v", err)
	}

	if err := kernel.notifyTerminal(ctx, "完成通知"); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("notifyTerminal: %v", err)
	}
}

func TestKernelDescriptorEntryFaults(t *testing.T) {
	ctx := context.Background()
	kernel, process, descriptors := fdKernel(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := kernel.ReadContextFD(cancelled, process.ID, 0, make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
	if _, err := kernel.ReadContextFD(ctx, proc.PID(999), 0, make([]byte, 1)); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
	if _, err := kernel.ReadContextFD(ctx, process.ID, 9, make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("未知描述符应报 EBADF: %v", err)
	}

	if err := descriptors.Detach(uint64(process.ID)); err != nil {
		t.Fatal(err)
	}
	if err := kernel.InstallFD(process.ID, 3, &fakeTerminal{}); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("描述符表缺失时安装应失败: %v", err)
	}
}

func TestKernelBeginRejectsWhenDescriptorBudgetCannotCoverStandardStreams(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	meters, err := resources.NewSession(resources.Limits{Memory: 1 << 20, Work: 8, Descriptors: 2}, resources.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg, Meters: meters}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.Limit) {
		t.Fatalf("标准流预算不足应拒绝: %v", err)
	}
	if procs := kernel.Processes(); len(procs) != 1 {
		t.Fatalf("失败的创建不得留下进程: %+v", procs)
	}
}

func TestKernelSignalDispositionEdges(t *testing.T) {
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

	if err := kernel.signals.SetIgnored(uint64(child.ID), signal.WINCH, true); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Signal(child.ID, signal.WINCH); err != nil {
		t.Fatalf("被忽略的信号应静默丢弃: %v", err)
	}
	if pending, err := kernel.Pending(child.ID); err != nil || len(pending) != 0 {
		t.Fatalf("被忽略的信号不应待决: %v %v", pending, err)
	}

	if err := kernel.Signal(child.ID, signal.CHLD); err != nil {
		t.Fatalf("默认忽略的信号应照常投递: %v", err)
	}
	if view := processView(t, kernel, child.ID); view.State != proc.Running {
		t.Fatalf("忽略处置不得改变进程状态: %+v", view)
	}

	if _, err := kernel.Pending(proc.PID(999)); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}
}

func TestKernelPriorityControlEdges(t *testing.T) {
	ctx := context.Background()
	reporting := executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		descriptors, err := p.Resources.Descriptors()
		if err != nil {
			return 0, err
		}
		out, err := descriptors.Get(ctx, uint64(p.ID), 1)
		if err != nil {
			return 0, err
		}
		_, err = fmt.Fprintf(out, "%d", p.Nice)
		return 0, err
	})
	reg, err := executable.New(map[string]executable.Program{"nice": reporting})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	control := procControl{k: kernel, self: proc.PID(1)}
	if _, err := control.Nice(proc.PID(999)); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程的优先级读应报 ESRCH: %v", err)
	}
	if err := control.SetNice(proc.PID(999), 0); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程的优先级写应报 ESRCH: %v", err)
	}
	runNice := func(adjust int) string {
		var out bytes.Buffer
		if _, err := control.Run(ctx, proc.RunSpec{Name: "nice", Dir: "/", Streams: fd.Streams{Output: &out}, NiceAdjust: &adjust}); err != nil {
			t.Fatalf("Run(adjust=%d): %v", adjust, err)
		}
		return out.String()
	}

	if got := runNice(proc.NiceHigh + 50); got != fmt.Sprint(proc.NiceHigh) {
		t.Fatalf("越界增量应夹到 NiceHigh: %q", got)
	}

	if got := runNice(1); got != "1" {
		t.Fatalf("界内增量应原样生效: %q", got)
	}

	if err := kernel.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Nice(1); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后的优先级读应报取消: %v", err)
	}
	if err := control.SetNice(1, 0); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后的优先级写应报取消: %v", err)
	}
}

func TestControlRunRejectsInvalidSignalDisposition(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	control := procControl{k: kernel, self: proc.PID(1)}
	terminal, err := control.Run(t.Context(), proc.RunSpec{
		Name: "echo", Dir: "/", IgnoreSignals: []signal.Number{0},
	})
	if !errors.Is(err, errno.Invalid) {
		t.Fatalf("无效信号编号应拒绝: %v", err)
	}
	if terminal.ID != 0 {
		t.Fatalf("被拒绝的启动不得报告进程: %+v", terminal)
	}
	if procs := kernel.Processes(); len(procs) != 1 {
		t.Fatalf("被拒绝的启动不得留下进程: %+v", procs)
	}
}

func TestKernelRunRegisteredRejectsCancelledContexts(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	status, err := kernel.RunRegistered(cancelled, child)
	if !errors.Is(err, context.Canceled) || status.Code != 125 {
		t.Fatalf("已取消的上下文不得启动程序: %+v %v", status, err)
	}

	ctx, cancelLater := context.WithCancel(t.Context())
	lateKernel, err := New(Services{
		Set:      capability.Set{FD: fd.New()},
		Programs: &cancelOnLookup{registry: reg, cancel: cancelLater, cancelAfter: 2},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	lateChild, err := lateKernel.Begin(0, "echo", nil, "/", nil, fd.Streams{Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lateKernel.RunRegistered(ctx, lateChild); err == nil {
		t.Fatal("解析期间被取消必须拒绝启动")
	}
}

type cancelOnLookup struct {
	registry    executable.Registry
	cancel      context.CancelFunc
	cancelAfter int
	mu          sync.Mutex
	calls       int
}

func (c *cancelOnLookup) Lookup(name string) (executable.Program, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call >= c.cancelAfter {
		c.cancel()
	}
	return c.registry.Lookup(name)
}

func TestKernelSelfReapedProcessIsNotReportedAsRunning(t *testing.T) {
	ctx := context.Background()

	selfReap := func(cancel context.CancelFunc) executable.Program {
		return executable.Func(func(_ context.Context, p proc.Proc) (uint8, error) {
			if cancel != nil {
				cancel()
			}
			return 0, p.Control.Finish(p.Parent, p.ID, 0)
		})
	}
	reg, err := executable.New(map[string]executable.Program{"self": selfReap(nil)})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	child, err := kernel.Begin(0, "self", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.RunRegistered(ctx, child); err == nil {
		t.Fatal("终态写入失败必须上报")
	}
	if procs := kernel.Processes(); len(procs) != 1 {
		t.Fatalf("自回收的进程不得留下残留: %+v", procs)
	}

	execKernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	exited, err := execKernel.Exec(ctx, "self", nil)
	if exited.State != proc.Zombie || err == nil {
		t.Fatalf("已回收的进程应按终态快照报告: %+v %v", exited, err)
	}

	ctx2, cancel2 := context.WithCancel(ctx)
	reg2, err := executable.New(map[string]executable.Program{"self": selfReap(cancel2)})
	if err != nil {
		t.Fatal(err)
	}
	cancelKernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg2}, 4)
	if err != nil {
		t.Fatal(err)
	}
	cancelChild, err := cancelKernel.Begin(0, "self", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cancelKernel.RunRegistered(ctx2, cancelChild); err == nil {
		t.Fatal("取消路径上的终态写入失败必须上报")
	}

	residual, getErr := cancelKernel.table.Get(cancelChild.ID)
	switch {
	case getErr != nil:

	case residual.State == proc.Zombie:
		if _, reapErr := cancelKernel.Reap(cancelChild.ID); reapErr != nil {
			t.Fatalf("残留僵尸必须能由 init 回收: %v", reapErr)
		}
	default:
		t.Fatalf("取消后进程不得停留在运行态: %+v", residual)
	}
}

func TestKernelReplaceRollsBackWhenDescriptorCleanupFails(t *testing.T) {
	kernel, descriptors := execKernel(t)
	pid := startGuest(t, kernel, descriptors)
	before := processView(t, kernel, pid)

	if err := descriptors.Detach(uint64(pid)); err != nil {
		t.Fatal(err)
	}
	if _, err := kernel.Replace(t.Context(), pid, "new", []string{"x"}, "/new", nil); err == nil {
		t.Fatal("描述符清理失败必须拒绝替换")
	}
	after := processView(t, kernel, pid)
	if after.Program != before.Program || after.Dir != before.Dir || strings.Join(after.Args, " ") != strings.Join(before.Args, " ") {
		t.Fatalf("必须回滚到替换前的身份: %+v → %+v", before, after)
	}

	second, descriptors := execKernel(t)
	pid2 := startGuest(t, second, descriptors)
	second.signals.Discard(uint64(pid2))
	if _, err := second.Replace(t.Context(), pid2, "new", nil, "/new", nil); err == nil {
		t.Fatal("信号表不一致必须拒绝替换")
	}
	if err := second.Finish(0, pid2, ExitStatus{}); !errors.Is(err, errno.Busy) {
		t.Fatalf("状态不一致后必须 fail-closed: %v", err)
	}
}

func TestKernelUtilityContracts(t *testing.T) {
	for _, value := range []uint64{0, 9, 10, math.MaxUint64} {
		if got, want := utoa(value), fmt.Sprint(value); got != want {
			t.Fatalf("utoa(%d) = %q, want %q", value, got, want)
		}
	}

	detail := memoryDetail(memory.Violation{
		Fault: memory.FaultUnmapped, Start: 0, End: 4096, Requested: memory.ProtRead | memory.ProtWrite,
	})
	if !strings.Contains(detail, "start=0") || !strings.Contains(detail, "end=4096") || !strings.Contains(detail, "rw-") {
		t.Fatalf("违规详情不符: %q", detail)
	}
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

	space, err := kernelSpaces{kernel: kernel}.BindAddressSpace(uint64(child.ID), memory.New(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if space == nil {
		t.Fatal("端口必须返回受管地址空间")
	}
	if _, err := kernel.AddressSpace(child.ID); err != nil {
		t.Fatalf("端口必须登记到真实 PID: %v", err)
	}
	if _, err := kernel.AddressSpace(child.ID + 99); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未绑定的进程应报 ENOENT: %v", err)
	}
}

func TestKernelBeginResolvesShebangAndBinaryEntry(t *testing.T) {
	ctx := context.Background()
	filesystem, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := executable.New(map[string]executable.Program{"echo": echoProgram()})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set:      capability.Set{FS: filesystem, FD: fd.New()},
		Programs: reg,
		Binaries: stubBinaries{program: echoProgram()},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}

	run := func(name string, args []string) string {
		var out bytes.Buffer
		child, err := kernel.Begin(0, name, args, "/", nil, fd.Streams{Output: &out})
		if err != nil {
			t.Fatalf("Begin(%s): %v", name, err)
		}
		if _, err := kernel.RunRegistered(ctx, child); err != nil {
			t.Fatalf("RunRegistered(%s): %v", name, err)
		}
		if _, err := kernel.Reap(child.ID); err != nil {
			t.Fatalf("Reap(%s): %v", name, err)
		}
		return out.String()
	}
	guestWrite(t, filesystem, "/work/tool", "#!/bin/echo -e\n正文\n")

	if got := run("/work/tool", []string{"arg"}); !strings.HasPrefix(got, "-e /work/tool arg") {
		t.Fatalf("shebang 参数必须进入参数表: %q", got)
	}

	guestWrite(t, filesystem, "/work/image", "\x7fELF\x02\x01\x01\x00rest")
	if got := run("/work/image", []string{"iarg"}); !strings.Contains(got, "image-argv0 iarg") {
		t.Fatalf("二进制映像入口必须被执行: %q", got)
	}

	plain, err := New(Services{Set: capability.Set{FS: filesystem, FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Begin(0, "/work/image", nil, "/", nil, fd.Streams{}); !errors.Is(err, errno.NoExec) {
		t.Fatalf("没有映像入口时 ELF 应被拒绝: %v", err)
	}
}

type stubBinaries struct{ program executable.Program }

func (s stubBinaries) New(string) (executable.Program, []string, error) {
	return s.program, []string{"image-argv0"}, nil
}

func TestKernelReadJournalEmptyAndUnknownOutcomes(t *testing.T) {
	ctx := context.Background()
	journal := replay.Record()
	kernel, process := stdinKernel(t, journal, emptyReader{})
	if _, err := kernel.ReadFD(ctx, process.ID, 0, make([]byte, 4)); !errors.Is(err, errno.NotFound) {
		t.Fatalf("上游结束应报 ENOENT: %v", err)
	}
	facts := journal.Facts()
	if len(facts) != 1 || len(facts[0].Output) != 1 || facts[0].Output[0] != readStatusEOF {
		t.Fatalf("EOF 必须记录为独立状态字节: %+v", facts)
	}
	replayKernel, replayProcess := stdinKernel(t, replay.Replay(facts), emptyReader{})
	if _, err := replayKernel.ReadFD(ctx, replayProcess.ID, 0, make([]byte, 4)); !errors.Is(err, errno.NotFound) {
		t.Fatalf("回放应返回同样的 ENOENT: %v", err)
	}
	broken := replay.Replay([]replay.Fact{{
		PID: uint64(replayProcess.ID), Operation: replay.OpStdin,
		Input: encodeReadRequest(0, 4), Output: []byte{9},
	}})
	brokenKernel, brokenProcess := stdinKernel(t, broken, emptyReader{})
	if _, err := brokenKernel.ReadFD(ctx, brokenProcess.ID, 0, make([]byte, 4)); !errors.Is(err, errno.Invalid) {
		t.Fatalf("未知状态字节应报 EINVAL: %v", err)
	}
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

func TestKernelQuietPointRejectsExecutingProgram(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	reg, err := executable.New(map[string]executable.Program{
		"hold": executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
			close(started)
			select {
			case <-release:
				return 0, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 4)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan proc.Proc, 1)
	go func() {
		exited, _ := kernel.Exec(context.Background(), "hold", nil)
		done <- exited
	}()
	<-started
	if err := kernel.EnsureQuietPoint(); !errors.Is(err, errno.Busy) {
		t.Fatalf("执行中的程序没有安全点: %v", err)
	}
	if _, err := kernel.SnapshotTable(t.Context()); !errors.Is(err, errno.Busy) {
		t.Fatalf("执行中不得快照: %v", err)
	}
	close(release)

	exited := <-done
	if _, err := kernel.Reap(exited.ID); err != nil {
		t.Fatal(err)
	}
	if err := kernel.EnsureQuietPoint(); err != nil {
		t.Fatalf("回收后应恢复安全点: %v", err)
	}
}

type panickingRegistry struct{}

func (panickingRegistry) Lookup(string) (executable.Program, error) { panic("装配层解析故障") }

func TestKernelExecPanicIsFailClosed(t *testing.T) {
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: panickingRegistry{}}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("panic 必须原样抛出")
		}
		if err := kernel.SetUmask(1, 0o022); !errors.Is(err, errno.Busy) {
			t.Fatalf("panic 后必须 fail-closed: %v", err)
		}
		if err := kernel.Finish(0, 1, ExitStatus{}); !errors.Is(err, errno.Busy) {
			t.Fatalf("panic 后必须 fail-closed: %v", err)
		}
	}()
	_, _ = kernel.Exec(context.Background(), "echo", nil)
}
