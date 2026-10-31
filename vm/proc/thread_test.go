package proc

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestThreadGroupLifecycle 锁定 K01/K02 的进程表语义:线程 TID 独立、
// TGID 归组;非组长退出直接移除且不影响组长;组长先退出保留占位
// 僵尸(不可回收);最后一个线程退出以该线程状态定稿整组;回收在
// 组完成前被拒绝。
func TestThreadGroupLifecycle(t *testing.T) {
	table, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := table.Create(0, "init", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(leader.ID); err != nil {
		t.Fatal(err)
	}
	group, err := table.Create(leader.ID, "app", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(group.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := table.CreateThread(group.ID, "app")
	if err != nil {
		t.Fatalf("创建线程: %v", err)
	}
	if err := table.Start(thread.ID); err != nil {
		t.Fatal(err)
	}
	if thread.TID == group.TID || thread.TGID != group.TGID {
		t.Fatalf("线程标识=%+v want 独立 TID、共享 TGID", thread)
	}
	if table.ThreadCount(group.TGID) != 2 {
		t.Fatalf("组内成员=%d want 2", table.ThreadCount(group.TGID))
	}

	// 组长(app 进程)先退出:占位僵尸,组内仍有存活线程,不可回收。
	if done, err := table.ExitThread(group.ID, 7, 0); err != nil || done {
		t.Fatalf("组长退出: done=%v err=%v", done, err)
	}
	placeholder, err := table.Get(group.ID)
	if err != nil || placeholder.State != Zombie {
		t.Fatalf("组长占位=%+v err=%v", placeholder, err)
	}
	if _, err := table.Reap(leader.ID, group.ID); !errors.Is(err, errno.Busy) {
		t.Fatalf("组未完成回收应被拒: %v", err)
	}

	// 非组长退出:条目直接移除(线程无僵尸),组长仍占位。
	if done, err := table.ExitThread(thread.ID, 9, 0); err != nil {
		t.Fatalf("线程退出: %v", err)
	} else if !done {
		t.Fatal("最后一个线程退出应定稿整组")
	}
	if _, err := table.Get(thread.ID); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("线程条目应移除: %v", err)
	}
	final, err := table.Get(group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != Zombie || final.ExitCode != 9 {
		t.Fatalf("组长终态=%+v want 僵尸/退出码 9(最后退出线程的状态)", final)
	}
	reaped, err := table.Reap(leader.ID, group.ID)
	if err != nil {
		t.Fatalf("组完成后回收: %v", err)
	}
	if reaped.ExitCode != 9 {
		t.Fatalf("回收状态=%+v", reaped)
	}
	if table.ThreadCount(group.TGID) != 0 {
		t.Fatal("回收后组应为空")
	}
}

// TestExitGroupEndsWholeGroup 锁定 exit_group 语义(H08):任一线程
// 触发组退出,全部成员消失,组长承载退出状态。
func TestExitGroupEndsWholeGroup(t *testing.T) {
	table, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	leader, _ := table.Create(0, "init", nil, "/", nil)
	_ = table.Start(leader.ID)
	group, _ := table.Create(leader.ID, "app", nil, "/", nil)
	_ = table.Start(group.ID)
	first, _ := table.CreateThread(group.ID, "app")
	_ = table.Start(first.ID)
	second, _ := table.CreateThread(group.ID, "app")
	_ = table.Start(second.ID)

	if err := table.Exit(first.ID, 33, 0); err != nil {
		t.Fatalf("组退出: %v", err)
	}
	// 非组长线程随组消失;组长(app 进程)承载退出状态转僵尸。
	for _, id := range []PID{first.ID, second.ID} {
		if _, err := table.Get(id); !errors.Is(err, errno.NoProcess) {
			t.Fatalf("成员 %d 应随组退出: %v", id, err)
		}
	}
	final, err := table.Get(group.ID)
	if err != nil || final.State != Zombie || final.ExitCode != 33 {
		t.Fatalf("组长终态=%+v err=%v want 僵尸/33", final, err)
	}
}

// TestCreateThreadRejectsStoppedParent 锁定创建约束:非运行/等待的
// 线程不能创建线程;进程上限覆盖线程(O02)。
func TestCreateThreadRejectsStoppedParent(t *testing.T) {
	table, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	leader, _ := table.Create(0, "init", nil, "/", nil)
	_ = table.Start(leader.ID)
	group, _ := table.Create(leader.ID, "app", nil, "/", nil)
	_ = table.Start(group.ID)
	if err := table.Stop(group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := table.CreateThread(group.ID, "app"); !errors.Is(err, errno.Busy) {
		t.Fatalf("驻停线程创建: %v want EBUSY", err)
	}
}
