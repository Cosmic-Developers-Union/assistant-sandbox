package proc

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestProcessThreadIdentityIsSeparated 验证 H01:进程号与线程号是两个
// 独立标识——单线程模型下二者相等,线程组标识指向组长;快照往返保留
// 三个标识。
func TestProcessThreadIdentityIsSeparated(t *testing.T) {
	table, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	root, err := table.Create(0, "init", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(root.ID); err != nil {
		t.Fatal(err)
	}
	child, err := table.Create(root.ID, "child", nil, "/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Proc{root, child} {
		if p.TID != p.ID {
			t.Fatalf("单线程模型的线程号应等于进程号: %+v", p)
		}
		if p.TGID != p.ID {
			t.Fatalf("单线程模型的线程组号应等于进程号: %+v", p)
		}
	}
	// 父子关系与线程组相互独立:子进程的父进程是根,组标识是自己的
	// 组长。
	if child.Parent != root.ID {
		t.Fatalf("父子关系错误: %+v", child)
	}
	if child.TGID != child.ID || child.TID != child.ID {
		t.Fatalf("子进程标识错误: %+v", child)
	}
	data, err := table.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(t.Context(), data); err != nil {
		t.Fatal(err)
	}
	got, err := restored.Get(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.TID != child.TID || got.TGID != child.TGID {
		t.Fatalf("快照未保留线程标识: %+v", got)
	}
}

// TestStateTransitionsAreConsistent 验证 H01:进程状态迁移集合明确——
// 就绪只能进入运行;运行可进入等待、驻停与僵尸;等待可被唤醒回运行;
// 僵尸是终态。
func TestStateTransitionsAreConsistent(t *testing.T) {
	table, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	root, err := table.Create(0, "init", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(root.ID); err != nil {
		t.Fatal(err)
	}
	child, err := table.Create(root.ID, "child", nil, "/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 就绪状态不能直接进入等待:必须先开始运行。
	if err := table.Transition(child.ID, Waiting); !errors.Is(err, errno.Busy) {
		t.Fatalf("就绪→等待: err=%v want EBUSY", err)
	}
	if err := table.Transition(child.ID, Running); err != nil {
		t.Fatal(err)
	}
	// 运行→等待→运行。
	if err := table.Transition(child.ID, Waiting); err != nil {
		t.Fatal(err)
	}
	if got, _ := table.Get(child.ID); got.State != Waiting {
		t.Fatalf("状态=%v want waiting", got.State)
	}
	if err := table.Transition(child.ID, Running); err != nil {
		t.Fatal(err)
	}
	// 运行→驻停→运行(驻停不能直接进入等待)。
	if err := table.Transition(child.ID, Stopped); err != nil {
		t.Fatal(err)
	}
	if err := table.Transition(child.ID, Waiting); !errors.Is(err, errno.Busy) {
		t.Fatalf("驻停→等待: err=%v want EBUSY", err)
	}
	if err := table.Transition(child.ID, Running); err != nil {
		t.Fatal(err)
	}
	// 阻塞中的进程可以被信号终止。
	if err := table.Transition(child.ID, Waiting); err != nil {
		t.Fatal(err)
	}
	if err := table.Exit(child.ID, 0, 9); err != nil {
		t.Fatal(err)
	}
	if got, _ := table.Get(child.ID); got.State != Zombie || got.ExitSignal != 9 {
		t.Fatalf("状态=%v 信号=%d want zombie/9", got.State, got.ExitSignal)
	}
	// 僵尸是终态:不能再迁移,只能被回收。
	if err := table.Transition(child.ID, Running); !errors.Is(err, errno.Busy) {
		t.Fatalf("僵尸→运行: err=%v want EBUSY", err)
	}
	// 不存在的进程迁移报 ESRCH。
	if err := table.Transition(root.ID+100, Running); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程: err=%v want ESRCH", err)
	}
}
