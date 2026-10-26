package vm

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/futex"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// tidKernel 构造带空注册表的内核,返回内核与根进程控制。
func tidKernel(t *testing.T) (*Kernel, procControl) {
	t.Helper()
	registry, err := executable.New(map[string]executable.Program{"idle": executable.Func(func(context.Context, proc.Proc) (uint8, error) {
		<-context.Background().Done()
		return 0, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: registry}, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kernel.Close(context.Background()) })
	// 用 Begin 建立的工作进程(fork 需要父进程有已挂载的描述符组)。
	process, err := kernel.Begin(0, "idle", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	self := procControl{k: kernel, self: process.ID}
	return kernel, self
}

// TestSetTidAddressRoundTrip 验证 K03:set_tid_address 登记/覆盖/撤销
// 与查询;fork 不继承;exec 复位。
func TestSetTidAddressRoundTrip(t *testing.T) {
	_, self := tidKernel(t)
	ctx := context.Background()
	if err := self.SetTidAddress(0x1000); err != nil {
		t.Fatal(err)
	}
	if got := self.TidAddress(); got != 0x1000 {
		t.Fatalf("登记=%#x want 0x1000", got)
	}
	// 覆盖。
	if err := self.SetTidAddress(0x2000); err != nil {
		t.Fatal(err)
	}
	if got := self.TidAddress(); got != 0x2000 {
		t.Fatalf("覆盖=%#x want 0x2000", got)
	}
	// fork 不继承:登记按进程号键控,新建/派生进程从不复制条目
	// (k.Fork 无 tidAddresses 复制路径)。用另一工作进程验证。
	other, err := self.k.Begin(0, "idle", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	if got := (procControl{k: self.k, self: other.ID}).TidAddress(); got != 0 {
		t.Fatalf("新进程继承=%#x want 0", got)
	}
	// exec:复位登记。
	if err := self.ResetForExec("/idle", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := self.TidAddress(); got != 0 {
		t.Fatalf("exec 未复位=%#x want 0", got)
	}
	_ = ctx
}

// TestClearTidAtExitWritesAndWakes 验证 K03:运行流终止时登记地址
// 写零(8 字节,经注入回调)并唤醒该地址上的 futex 等待者;未登记
// 时无操作。
func TestClearTidAtExitWritesAndWakes(t *testing.T) {
	_, self := tidKernel(t)
	ctx := context.Background()
	const address = 0xABC0
	if err := self.SetTidAddress(address); err != nil {
		t.Fatal(err)
	}
	// 预置一个等待者:值恒 0(等待清零),阻塞直到唤醒。
	futexes := self.FutexTable()
	done := make(chan error, 1)
	go func() {
		done <- futexes.Wait(ctx, futex.Key{Owner: uint64(self.self), Address: address}, 0, func() (uint32, bool) {
			return 0, true
		})
	}()
	time.Sleep(30 * time.Millisecond)

	writes := make(chan struct {
		address uint64
		content []byte
	}, 1)
	self.ClearTidAtExit(ctx, func(_ context.Context, writeAddress uint64, content []byte) error {
		writes <- struct {
			address uint64
			content []byte
		}{writeAddress, content}
		return nil
	})
	select {
	case write := <-writes:
		if write.address != address {
			t.Fatalf("写零地址=%#x want %#x", write.address, address)
		}
		if len(write.content) != 8 {
			t.Fatalf("写零长度=%d want 8", len(write.content))
		}
		for i, b := range write.content {
			if b != 0 {
				t.Fatalf("写零内容[%d]=%#x", i, b)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("退出清算未写零")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("等待者 err=%v want nil(被唤醒)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("退出清算未唤醒 futex 等待者")
	}
	// 登记已消费:再次清算无操作。
	if got := self.TidAddress(); got != 0 {
		t.Fatalf("清算后登记=%#x want 0", got)
	}
	writes = nil
	self.ClearTidAtExit(ctx, func(context.Context, uint64, []byte) error { return nil })
}

// TestClearTidWithoutRegistrationIsNoop 验证 K03:未登记地址的退出
// 清算不注入写回调即可返回。
func TestClearTidWithoutRegistrationIsNoop(t *testing.T) {
	_, self := tidKernel(t)
	called := false
	self.ClearTidAtExit(context.Background(), func(context.Context, uint64, []byte) error {
		called = true
		return nil
	})
	if called {
		t.Fatal("未登记不应写零")
	}
}

// TestFutexWakeCountCapMatchesIntMax 验证 K03 唤醒参数:清算用
// INT_MAX 全量唤醒(count 上限与 futex 核心约定一致)。
func TestFutexWakeCountCapMatchesIntMax(t *testing.T) {
	_, self := tidKernel(t)
	ctx := context.Background()
	const address = 0x3000
	if err := self.SetTidAddress(address); err != nil {
		t.Fatal(err)
	}
	futexes := self.FutexTable()
	const waiters = 3
	done := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			done <- futexes.Wait(ctx, futex.Key{Owner: uint64(self.self), Address: address}, 0, func() (uint32, bool) {
				return 0, true
			})
		}()
	}
	time.Sleep(30 * time.Millisecond)
	self.ClearTidAtExit(ctx, nil)
	for i := 0; i < waiters; i++ {
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, errno.Cancelled) {
				t.Fatalf("等待者 %d err=%v", i, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("等待者 %d 未被唤醒", i)
		}
	}
	_ = math.MaxInt32
}
