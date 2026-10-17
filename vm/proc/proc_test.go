package proc

import (
	"errors"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestLifecycleIsolationAndReap(t *testing.T) {
	table, err := New(3)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"original"}
	env := map[string]string{"K": "original"}
	root, err := table.Create(0, "init", args, "/", env)
	if err != nil {
		t.Fatal(err)
	}
	args[0] = "changed"
	env["K"] = "changed"
	if err := table.Start(root.ID); err != nil {
		t.Fatal(err)
	}
	child, err := table.Create(root.ID, "child", nil, "/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	fetched, _ := table.Get(root.ID)
	if fetched.Args[0] != "original" || fetched.Env["K"] != "original" {
		t.Fatal(fetched)
	}
	fetched.Env["K"] = "mutated"
	again, _ := table.Get(root.ID)
	if again.Env["K"] != "original" {
		t.Fatal("共享可变内存")
	}
	if root.Generation != 1 || child.Generation != 1 {
		t.Fatal("代数未分配")
	}
	if err := table.Start(child.ID); err != nil {
		t.Fatal(err)
	}

	if child.PGID != root.PGID || child.SID != root.SID {
		t.Fatal(child)
	}

	if err := table.Exit(child.ID, 7, 0); err != nil {
		t.Fatal(err)
	}
	got, ok := table.WaitAny(root.ID)
	if !ok || got.ExitCode != 7 || got.State != Zombie || got.ExitSignal != 0 {
		t.Fatalf("got=%v ok=%v", got, ok)
	}
	reaped, err := table.Reap(root.ID, child.ID)
	if err != nil || reaped.ExitCode != 7 {
		t.Fatal(reaped, err)
	}

	if _, ok := table.WaitAny(root.ID); ok {
		t.Fatal("僵尸未移除")
	}
	if _, err := table.Reap(root.ID, child.ID); !errors.Is(err, errno.NoChild) {
		t.Fatal(err)
	}

	if _, err := table.Reap(root.ID, root.ID); !errors.Is(err, errno.NoChild) {
		t.Fatal(err)
	}
	if table.Init() != root.ID {
		t.Fatalf("init=%d", table.Init())
	}
}

func TestReparentToInitOnParentExit(t *testing.T) {
	table, _ := New(4)
	root, _ := table.Create(0, "init", nil, "/", nil)
	if err := table.Start(root.ID); err != nil {
		t.Error(err)
	}
	mid, _ := table.Create(root.ID, "mid", nil, "/", nil)
	if err := table.Start(mid.ID); err != nil {
		t.Error(err)
	}
	grandchild, _ := table.Create(mid.ID, "grand", nil, "/", nil)
	if err := table.Start(grandchild.ID); err != nil {
		t.Error(err)
	}
	zombieChild, _ := table.Create(mid.ID, "zombie", nil, "/", nil)
	if err := table.Start(zombieChild.ID); err != nil {
		t.Error(err)
	}
	if err := table.Exit(zombieChild.ID, 3, 0); err != nil {
		t.Error(err)
	}

	if err := table.Exit(mid.ID, 9, 2); err != nil {
		t.Fatal(err)
	}

	for _, id := range []PID{grandchild.ID, zombieChild.ID} {
		p, err := table.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Parent != root.ID {
			t.Fatalf("%d parent=%d", id, p.Parent)
		}
	}

	reaped, err := table.Reap(root.ID, zombieChild.ID)
	if err != nil || reaped.ExitCode != 3 {
		t.Fatal(reaped, err)
	}

	if children := table.Children(root.ID); len(children) != 2 {
		t.Fatalf("children=%v", children)
	}
	if _, err := table.Reap(root.ID, mid.ID); err != nil {
		t.Fatal(err)
	}
	if children := table.Children(root.ID); len(children) != 1 || children[0].ID != grandchild.ID {
		t.Fatalf("children=%v", children)
	}

	if err := table.Exit(grandchild.ID, 0, 15); err != nil {
		t.Error(err)
	}
	reaped, err = table.Reap(root.ID, grandchild.ID)
	if err != nil || reaped.ExitSignal != 15 {
		t.Fatal(reaped, err)
	}
}

func TestOnlyOneRootAllowed(t *testing.T) {
	table, _ := New(3)
	if _, err := table.Create(0, "init", nil, "/", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Create(0, "second", nil, "/", nil); !errors.Is(err, errno.Busy) {
		t.Fatalf("err=%v", err)
	}
}

func TestGenerationValidation(t *testing.T) {
	table, _ := New(3)
	root, _ := table.Create(0, "init", nil, "/", nil)
	if err := table.Start(root.ID); err != nil {
		t.Error(err)
	}
	if _, err := table.GetLive(root.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := table.GetLive(root.ID, 2); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("err=%v", err)
	}
	if _, err := table.GetLive(99, 1); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
}

func TestInvalidStateBudgetAndPIDExhaustion(t *testing.T) {
	if _, err := New(0); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	table, _ := New(2)
	for _, bad := range []struct{ program, dir string }{{"", "/"}, {"p", ""}, {"p", "relative"}} {
		if _, err := table.Create(0, bad.program, nil, bad.dir, nil); !errors.Is(err, errno.Invalid) {
			t.Fatal(err)
		}
	}
	if _, err := table.Create(99, "p", nil, "/", nil); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	root, _ := table.Create(0, "init", nil, "/", nil)
	if _, err := table.Create(root.ID, "p", nil, "/", nil); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}
	if err := table.Exit(root.ID, 0, 0); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}
	if _, err := table.Reap(0, root.ID); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}
	if err := table.Start(root.ID); err != nil {
		t.Error(err)
	}
	if err := table.Start(root.ID); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}
	child, _ := table.Create(root.ID, "p", nil, "/", nil)
	if _, err := table.Create(root.ID, "p", nil, "/", nil); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
	if _, err := table.Reap(99, child.ID); !errors.Is(err, errno.NoChild) {
		t.Fatal(err)
	}
	for _, err := range []error{table.Start(99), table.Exit(99, 0, 0)} {
		if !errors.Is(err, errno.NoProcess) {
			t.Fatal(err)
		}
	}
	if _, err := table.Get(99); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}
	if err := table.Exit(root.ID, 0, 0); err != nil {
		t.Error(err)
	}
	if err := table.Exit(root.ID, 0, 0); !errors.Is(err, errno.Busy) {
		t.Fatal(err)
	}

	ids := table.List()
	if len(ids) != 2 || ids[0].ID != 1 || ids[1].ID != 2 {
		t.Fatal(ids)
	}
	overflow, _ := New(2)
	overflowRoot, _ := overflow.Create(0, "init", nil, "/", nil)
	if err := overflow.Start(overflowRoot.ID); err != nil {
		t.Error(err)
	}
	overflow.next = ^PID(0)
	last, _ := overflow.Create(overflowRoot.ID, "last", nil, "/", nil)
	if err := overflow.Start(last.ID); err != nil {
		t.Error(err)
	}
	if err := overflow.Exit(last.ID, 0, 0); err != nil {
		t.Error(err)
	}
	if _, err := overflow.Reap(overflowRoot.ID, last.ID); err != nil {
		t.Error(err)
	}

	if _, err := overflow.Create(overflowRoot.ID, "wrap", nil, "/", nil); !errors.Is(err, errno.Limit) {
		t.Fatal("PID溢出", err)
	}
	var zero Table
	if _, err := zero.Create(0, "p", nil, "/", nil); !errors.Is(err, errno.Limit) {
		t.Fatal(err)
	}
}

func TestProcessGroupAndSession(t *testing.T) {
	table, _ := New(4)
	init, _ := table.Create(0, "init", nil, "/", nil)
	if err := table.Start(init.ID); err != nil {
		t.Error(err)
	}

	if err := table.SetPGID(init.ID, init.ID, 0); !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}
	if err := table.SetSID(init.ID); !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}
	worker, _ := table.Create(init.ID, "worker", nil, "/", nil)
	if err := table.Start(worker.ID); err != nil {
		t.Error(err)
	}

	if err := table.SetPGID(init.ID, worker.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := table.Get(worker.ID)
	if got.PGID != worker.ID || got.SID != init.SID {
		t.Fatal(got)
	}
	member, _ := table.Create(init.ID, "member", nil, "/", nil)
	if err := table.Start(member.ID); err != nil {
		t.Error(err)
	}

	if err := table.SetPGID(init.ID, member.ID, worker.ID); err != nil {
		t.Fatal(err)
	}

	other, _ := table.Create(init.ID, "other", nil, "/", nil)
	if err := table.Start(other.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetSID(other.ID); err != nil {
		t.Fatal(err)
	}
	if err := table.SetPGID(other.ID, member.ID, member.ID); !errors.Is(err, errno.Permission) {
		t.Fatalf("err=%v", err)
	}

	if err := table.SetPGID(member.ID, worker.ID, worker.ID); !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}

	if err := table.SetPGID(init.ID, member.ID, 99); !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}

	if err := table.SetPGID(init.ID, member.ID, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := table.Get(member.ID); got.PGID != member.ID {
		t.Fatal(got)
	}

	if err := table.SetPGID(init.ID, 99, 0); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	if err := table.SetPGID(member.ID, member.ID, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := table.Get(member.ID); got.SID != member.SID && got.PGID != member.ID {
		t.Fatal(got)
	}
}

func TestWaitAnyPrefersOldest(t *testing.T) {
	table, _ := New(4)
	root, _ := table.Create(0, "init", nil, "/", nil)
	if err := table.Start(root.ID); err != nil {
		t.Error(err)
	}
	first, _ := table.Create(root.ID, "first", nil, "/", nil)
	if err := table.Start(first.ID); err != nil {
		t.Error(err)
	}
	second, _ := table.Create(root.ID, "second", nil, "/", nil)
	if err := table.Start(second.ID); err != nil {
		t.Error(err)
	}
	if err := table.Exit(second.ID, 2, 0); err != nil {
		t.Error(err)
	}
	if err := table.Exit(first.ID, 1, 0); err != nil {
		t.Error(err)
	}
	got, ok := table.WaitAny(root.ID)
	if !ok || got.ID != first.ID || got.ExitCode != 1 {
		t.Fatalf("got=%v ok=%v", got, ok)
	}
	if _, ok := table.WaitAny(root.ID); !ok {
		t.Fatal("第二个僵尸应可见")
	}

	if _, ok := table.WaitAny(root.ID); !ok {
		t.Fatal("init 的子进程仍是僵尸")
	}
	if _, err := table.Reap(root.ID, first.ID); err != nil {
		t.Error(err)
	}
	if _, err := table.Reap(root.ID, second.ID); err != nil {
		t.Error(err)
	}
	if _, ok := table.WaitAny(root.ID); ok {
		t.Fatal("无僵尸时不应返回")
	}
	if table.HasChildren(root.ID) {
		t.Fatal("不应再有子进程")
	}
}

func TestUmaskIsProcessFact(t *testing.T) {
	table, err := New(8)
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
	if root.Umask != DefaultUmask {
		t.Fatalf("启动进程掩码 = %o", root.Umask)
	}
	if err := table.SetUmask(root.ID, 0o077); err != nil {
		t.Fatal(err)
	}
	child, err := table.Create(root.ID, "sh", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.Umask != 0o077 {
		t.Fatalf("子进程应继承掩码: %o", child.Umask)
	}

	if err := table.SetUmask(child.ID, 0o022); err != nil {
		t.Fatal(err)
	}
	if parent, err := table.Get(root.ID); err != nil || parent.Umask != 0o077 {
		t.Fatalf("父进程掩码被改动: %o %v", parent.Umask, err)
	}

	for _, mask := range []stdfs.FileMode{0o10000, stdfs.ModeDir | 0o755} {
		if err := table.SetUmask(root.ID, mask); !errors.Is(err, errno.Invalid) {
			t.Fatalf("掩码 %v 应被拒绝: %v", mask, err)
		}
	}
	if err := table.SetUmask(999, 0o022); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程: %v", err)
	}

	if err := table.Start(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := table.Replace(child.ID, "other", nil, "/", nil); err != nil {
		t.Fatal(err)
	}
	if after, err := table.Get(child.ID); err != nil || after.Umask != 0o022 {
		t.Fatalf("exec 后掩码被改动: %o %v", after.Umask, err)
	}
}
