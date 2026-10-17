package proc

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestReplaceKeepsProcessIdentityAndBumpsGeneration(t *testing.T) {
	table, err := New(3)
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
	child, err := table.Create(root.ID, "old", []string{"arg"}, "/old", map[string]string{"OLD": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(child.ID); err != nil {
		t.Fatal(err)
	}

	args := []string{"new-arg", "second"}
	env := map[string]string{"NEW": "2"}
	before, after, err := table.Replace(child.ID, "new", args, "/new", env)
	if err != nil {
		t.Fatal(err)
	}
	if before.Program != "old" || before.Generation != child.Generation {
		t.Fatalf("before=%+v", before)
	}
	if after.ID != child.ID || after.Parent != child.Parent || after.PGID != child.PGID || after.SID != child.SID {
		t.Fatalf("身份字段必须保持: after=%+v child=%+v", after, child)
	}
	if after.Program != "new" || after.Dir != "/new" || after.Generation != child.Generation+1 {
		t.Fatalf("after=%+v", after)
	}

	args[0] = "mutated"
	env["NEW"] = "mutated"
	stored, err := table.Get(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Args[0] != "new-arg" || stored.Env["NEW"] != "2" {
		t.Fatalf("入参必须复制: %+v", stored)
	}
	if stored.Env["OLD"] != "" {
		t.Fatal("旧环境必须整体替换")
	}

	if _, err := table.GetLive(child.ID, child.Generation); !errors.Is(err, errno.NoProcess) {
		t.Fatal("旧代数句柄必须失效", err)
	}
	if _, err := table.GetLive(child.ID, after.Generation); err != nil {
		t.Fatal("新代数句柄必须可用", err)
	}
}

func TestReplaceRejectsInvalidRequests(t *testing.T) {
	table, err := New(3)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ program, dir string }{
		{"", "/"}, {"program", ""}, {"program", "relative"},
	} {
		if _, _, err := table.Replace(1, bad.program, nil, bad.dir, nil); !errors.Is(err, errno.Invalid) {
			t.Fatalf("program=%q dir=%q 应报 EINVAL: %v", bad.program, bad.dir, err)
		}
	}
	root, err := table.Create(0, "init", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := table.Replace(root.ID, "new", nil, "/", nil); !errors.Is(err, errno.Busy) {
		t.Fatalf("Ready 进程应报 EBUSY: %v", err)
	}
	if err := table.Start(root.ID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := table.Replace(999, "new", nil, "/", nil); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}

	if err := table.Exit(root.ID, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := table.Replace(root.ID, "new", nil, "/", nil); !errors.Is(err, errno.Busy) {
		t.Fatalf("Zombie 进程应报 EBUSY: %v", err)
	}

	stored, err := table.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Program != "init" || stored.Generation != 1 {
		t.Fatalf("拒绝后状态必须不变: %+v", stored)
	}
}
