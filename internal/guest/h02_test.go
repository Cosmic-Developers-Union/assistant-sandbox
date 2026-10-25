package guest_test

import (
	"context"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// TestSchedProbesAreDeterministic 锁定三个调度探针的产物摘要。
func TestSchedProbesAreDeterministic(t *testing.T) {
	if guest.ImageDigestOf(guest.SpinForeverProbe()) != guest.SpinForeverImageSHA256 {
		t.Fatal("spin-forever 探针摘要与登记值不符")
	}
	if guest.ImageDigestOf(guest.SpinExitProbe(5)) != guest.SpinExitImageSHA256 {
		t.Fatal("spin-exit 探针摘要与登记值不符")
	}
	if guest.ImageDigestOf(guest.BlockedReadProbe()) != guest.BlockedReadImageSHA256 {
		t.Fatal("blocked-read 探针摘要与登记值不符")
	}
	if guest.ImageDigestOf(guest.SleepProbe()) != guest.SleepProbeImageSHA256 {
		t.Fatal("sleep 探针摘要与登记值不符")
	}
}

// TestKernelTimeSliceLetsCPUBoundProgramsShareCore 验证 H02:纯 CPU
// 死循环与有界 CPU 密集程序共享单核——死循环在时间片边界让出执行权,
// 有界程序得以推进并完成;时间片机制失效时后者将永远等不到执行权。
func TestKernelTimeSliceLetsCPUBoundProgramsShareCore(t *testing.T) {
	kernel, system := guestKernel(t, nil)
	defer func() { _ = kernel.Close(context.Background()) }()
	write := func(t *testing.T, path string, image []byte) {
		t.Helper()
		file, err := system.Open(context.Background(), path, fs.Create|fs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(image); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	write(t, "/spin", guest.SpinForeverProbe())
	write(t, "/bound", guest.SpinExitProbe(5))

	spin, err := kernel.Begin(0, "/spin", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	spinDone := make(chan error, 1)
	go func() {
		_, err := kernel.RunRegistered(context.Background(), spin)
		spinDone <- err
	}()
	bound, err := kernel.Begin(0, "/bound", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	boundDone := make(chan struct{}, 1)
	go func() {
		defer close(boundDone)
		if _, err := kernel.RunRegistered(context.Background(), bound); err != nil {
			t.Errorf("有界程序运行: %v", err)
		}
	}()
	select {
	case <-boundDone:
	case <-time.After(30 * time.Second):
		t.Fatal("有界程序未完成:死循环未在时间片边界让出执行权")
	}
	// 有界程序完成时死循环仍在运行(被时间片轮转而非饿死)。
	found := false
	for _, p := range kernel.Processes() {
		if p.ID == spin.ID && (p.State == proc.Running || p.State == proc.Waiting) {
			found = true
		}
	}
	if !found {
		t.Fatalf("死循环进程应仍在运行: %+v", kernel.Processes())
	}
	// SIGKILL 结束死循环并回收。
	if err := kernel.Signal(spin.ID, signal.KILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-spinDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL 未终止死循环")
	}
	if _, _, err := kernel.WaitChild(context.Background(), 0, spin.ID, true); err != nil {
		t.Fatalf("回收死循环: %v", err)
	}
}

// TestKernelBlockedPipeReadDoesNotHoldExecution 验证 H03:阻塞在空管道
// 读上的进程不持有执行权——其他 CPU 密集进程照常推进完成;内核关闭
// 唤醒阻塞者,不留宿主 goroutine。
func TestKernelBlockedPipeReadDoesNotHoldExecution(t *testing.T) {
	kernel, system := guestKernel(t, nil)
	defer func() { _ = kernel.Close(context.Background()) }()
	file, err := system.Open(context.Background(), "/blocked", fs.Create|fs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(guest.BlockedReadProbe()); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	file, err = system.Open(context.Background(), "/bound", fs.Create|fs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(guest.SpinExitProbe(9)); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	blocked, err := kernel.Begin(0, "/blocked", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = kernel.RunRegistered(context.Background(), blocked) }()
	time.Sleep(50 * time.Millisecond) // 等阻塞进程进入管道读
	bound, err := kernel.Begin(0, "/bound", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	boundDone := make(chan struct{}, 1)
	go func() {
		defer close(boundDone)
		if _, err := kernel.RunRegistered(context.Background(), bound); err != nil {
			t.Errorf("有界程序运行: %v", err)
		}
	}()
	select {
	case <-boundDone:
	case <-time.After(30 * time.Second):
		t.Fatal("阻塞进程占用了执行权,其他进程无法推进")
	}
}

// TestKernelStoppedProcessDoesNotHoldExecution 验证 H02:被 SIGSTOP
// 驻停的进程归还执行权,其他进程照常完成;恢复后继续执行。
func TestKernelStoppedProcessDoesNotHoldExecution(t *testing.T) {
	kernel, system := guestKernel(t, nil)
	defer func() { _ = kernel.Close(context.Background()) }()
	for name, image := range map[string][]byte{
		"/spin":  guest.SpinForeverProbe(),
		"/bound": guest.SpinExitProbe(6),
	} {
		file, err := system.Open(context.Background(), name, fs.Create|fs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(image); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	spin, err := kernel.Begin(0, "/spin", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = kernel.RunRegistered(context.Background(), spin) }()
	time.Sleep(50 * time.Millisecond)
	if err := kernel.Signal(spin.ID, signal.STOP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	stopped := false
	for !stopped {
		for _, p := range kernel.Processes() {
			if p.ID == spin.ID && p.State == proc.Stopped {
				stopped = true
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("进程未进入驻停状态")
		}
		time.Sleep(time.Millisecond)
	}
	bound, err := kernel.Begin(0, "/bound", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	boundDone := make(chan struct{}, 1)
	go func() {
		defer close(boundDone)
		if _, err := kernel.RunRegistered(context.Background(), bound); err != nil {
			t.Errorf("有界程序运行: %v", err)
		}
	}()
	select {
	case <-boundDone:
	case <-time.After(30 * time.Second):
		t.Fatal("驻停进程占用了执行权,其他进程无法推进")
	}
	if err := kernel.Signal(spin.ID, signal.KILL); err != nil {
		t.Fatal(err)
	}
}

// TestKernelSignalTerminatesBlockedPipeRead 验证 H03:阻塞在空管道读
// 上的进程被信号按默认处置终止(阻塞读可中断,无宿主 goroutine
// 遗漏),父进程经 waitid 观察到信号终止。
func TestKernelSignalTerminatesBlockedPipeRead(t *testing.T) {
	kernel, system := guestKernel(t, nil)
	defer func() { _ = kernel.Close(context.Background()) }()
	file, err := system.Open(context.Background(), "/blocked", fs.Create|fs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(guest.BlockedReadProbe()); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	blocked, err := kernel.Begin(0, "/blocked", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{}, 1)
	go func() {
		defer close(done)
		_, _ = kernel.RunRegistered(context.Background(), blocked)
	}()
	time.Sleep(100 * time.Millisecond)
	if err := kernel.Signal(blocked.ID, signal.USR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("阻塞读未被信号终止(读取不可中断或令牌未释放)")
	}
	state, found, err := kernel.WaitChild(context.Background(), 0, blocked.ID, true)
	if err != nil || !found {
		t.Fatalf("回收: state=%+v found=%v err=%v", state, found, err)
	}
	if state.ExitSignal != uint8(signal.USR1) {
		t.Fatalf("终止信号=%d want SIGUSR1", state.ExitSignal)
	}
}

// TestKernelSleepDoesNotHoldExecution 验证 H03:clock_nanosleep 睡眠
// 期间不持有执行权——其他 CPU 密集进程照常完成;睡眠进程按期醒来。
func TestKernelSleepDoesNotHoldExecution(t *testing.T) {
	// 用生产时钟(而非虚拟时钟):睡眠真实阻塞,验证睡眠期间
	// 执行权已释放、其他进程可以推进。
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set: capability.Set{
			FS:       system,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    clock.NewReal(),
			Entropy:  entropy.NewSeeded(1),
		},
		Programs: registry,
		Binaries: elf.Factory{},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = kernel.Close(context.Background()) }()
	for name, image := range map[string][]byte{
		"/sleeper": guest.SleepProbe(),
		"/bound":   guest.SpinExitProbe(8),
	} {
		file, err := system.Open(context.Background(), name, fs.Create|fs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(image); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sleeper, err := kernel.Begin(0, "/sleeper", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	sleepDone := make(chan error, 1)
	go func() {
		_, err := kernel.RunRegistered(context.Background(), sleeper)
		sleepDone <- err
	}()
	bound, err := kernel.Begin(0, "/bound", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	boundDone := make(chan struct{}, 1)
	go func() {
		defer close(boundDone)
		_, _ = kernel.RunRegistered(context.Background(), bound)
	}()
	// 有界程序(约百万指令,微秒级)必须在睡眠者(200ms)之前完成:
	// 睡眠发生在系统调用内,执行权已让出;若睡眠持有执行权,有界
	// 程序要等睡眠结束才能推进。
	select {
	case <-boundDone:
	case <-sleepDone:
		t.Fatal("睡眠者先于有界程序完成:睡眠期间未释放执行权")
	case <-time.After(30 * time.Second):
		t.Fatal("睡眠进程占用了执行权,其他进程无法推进")
	}
	// 睡眠者按期醒来并回收。
	select {
	case err := <-sleepDone:
		if err != nil {
			t.Fatalf("睡眠进程运行: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("睡眠进程未按期醒来")
	}
	if _, _, err := kernel.WaitChild(context.Background(), 0, sleeper.ID, true); err != nil {
		t.Fatalf("回收睡眠进程: %v", err)
	}
}
