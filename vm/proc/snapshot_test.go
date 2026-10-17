package proc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func populatedTable(t *testing.T) *Table {
	t.Helper()
	table, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	root, err := table.Create(0, "init", []string{"init"}, "/", map[string]string{"HOME": "/"})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(root.ID); err != nil {
		t.Fatal(err)
	}
	child, err := table.Create(root.ID, "worker", []string{"a", "b"}, "/work", map[string]string{"K": "V"})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(child.ID); err != nil {
		t.Fatal(err)
	}
	grandchild, err := table.Create(child.ID, "leaf", nil, "/leaf", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Start(grandchild.ID); err != nil {
		t.Fatal(err)
	}

	if err := table.Exit(grandchild.ID, 7, 0); err != nil {
		t.Fatal(err)
	}
	return table
}

func TestSnapshotRoundTripPreservesTableState(t *testing.T) {
	ctx := context.Background()
	table := populatedTable(t)
	data, err := table.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(ctx, data); err != nil {
		t.Fatal(err)
	}

	before, after := table.List(), restored.List()
	if len(before) != len(after) {
		t.Fatalf("进程数不一致: %d vs %d", len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Parent != after[i].Parent ||
			before[i].Generation != after[i].Generation || before[i].Program != after[i].Program ||
			before[i].Dir != after[i].Dir || before[i].State != after[i].State ||
			before[i].ExitCode != after[i].ExitCode || before[i].ExitSignal != after[i].ExitSignal ||
			before[i].PGID != after[i].PGID || before[i].SID != after[i].SID {
			t.Fatalf("第 %d 个进程不一致:\n%+v\n%+v", i, before[i], after[i])
		}
		if strings.Join(before[i].Args, ",") != strings.Join(after[i].Args, ",") {
			t.Fatalf("参数不一致: %v vs %v", before[i].Args, after[i].Args)
		}
	}
	if got := restored.Init(); got != table.Init() {
		t.Fatalf("Init=%d want %d", got, table.Init())
	}
}

func TestSnapshotIsDeterministicAndIsolated(t *testing.T) {
	ctx := context.Background()
	table := populatedTable(t)
	first, err := table.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := table.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("相同表状态必须产生相同字节")
	}

	tampered := append([]byte(nil), first...)
	tampered[0] ^= 0xff
	if restored, err := table.Snapshot(ctx); err != nil || string(restored) != string(first) {
		t.Fatalf("篡改编码结果不应影响后续快照: %v", err)
	}
}

func TestRestoreAdvancesNextPID(t *testing.T) {
	ctx := context.Background()
	table := populatedTable(t)
	data, err := table.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(ctx, data); err != nil {
		t.Fatal(err)
	}
	newProcess, err := restored.Create(restored.Init(), "after", nil, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range table.List() {
		if newProcess.ID == p.ID {
			t.Fatalf("恢复后复用了 PID %d", newProcess.ID)
		}
	}

	before, err := table.Get(table.Init())
	if err != nil {
		t.Fatal(err)
	}
	after, err := restored.GetLive(table.Init(), before.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if after.Program != before.Program {
		t.Fatalf("代数语义未保持: %+v", after)
	}
}

func TestRestoreReparentsMissingParents(t *testing.T) {
	ctx := context.Background()
	table := populatedTable(t)
	data, err := table.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = data

	snapshot := tableSnapshot{
		Format: TableFormat,
		Next:   4,
		Init:   1,
		Entries: []processRecord{
			{ID: 1, Parent: 0, Generation: 1, Program: "init", Dir: "/", State: Running, PGID: 1, SID: 1},
			{ID: 3, Parent: 2, Generation: 1, Program: "leaf", Dir: "/", State: Running, PGID: 1, SID: 1},
		},
	}
	raw := mustMarshal(t, snapshot)
	restored, err := New(8)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Restore(ctx, raw); err != nil {
		t.Fatal(err)
	}
	leaf, err := restored.Get(3)
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Parent != 1 {
		t.Fatalf("缺失父进程必须挂靠 init: parent=%d", leaf.Parent)
	}
}

func TestRestoreRejectsInvalidSnapshots(t *testing.T) {
	ctx := context.Background()
	table := populatedTable(t)
	before, err := table.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"空数据", nil, errno.Invalid},
		{"非法 JSON", []byte("{"), errno.Invalid},
		{"未知字段", []byte(`{"format":"` + TableFormat + `","entries":[],"future":1}`), errno.Invalid},
		{"版本不符", []byte(`{"format":"assistant-sandbox-proctable-0","next":1,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Version},
		{"空条目", []byte(`{"format":"` + TableFormat + `","next":1,"init":1,"entries":[]}`), errno.Invalid},
		{"零号进程", []byte(`{"format":"` + TableFormat + `","next":1,"init":1,"entries":[{"id":0,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"重复 PID", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1},{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"启动进程缺失", []byte(`{"format":"` + TableFormat + `","next":2,"init":9,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"启动进程有父进程", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":1,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"多个根进程", []byte(`{"format":"` + TableFormat + `","next":3,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":1,"sid":1},{"id":2,"parent":0,"generation":1,"program":"q","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"缺失程序名", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"相对目录", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"rel","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"代数非法", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":0,"generation":0,"program":"p","dir":"/","state":1,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"状态非法", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":9,"pgid":1,"sid":1}]}`), errno.Invalid},
		{"缺少进程组", []byte(`{"format":"` + TableFormat + `","next":2,"init":1,"entries":[{"id":1,"parent":0,"generation":1,"program":"p","dir":"/","state":1,"pgid":0,"sid":1}]}`), errno.Invalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, err := New(8)
			if err != nil {
				t.Fatal(err)
			}
			err = target.Restore(ctx, tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}

			if len(target.List()) != 0 {
				t.Fatalf("拒绝后表被改动: %+v", target.List())
			}
		})
	}

	after, err := table.Snapshot(ctx)
	if err != nil || string(after) != string(before) {
		t.Fatalf("被拒绝的恢复不应影响原表: %v", err)
	}
}

func TestSnapshotRequiresInitProcess(t *testing.T) {
	table, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Snapshot(context.Background()); !errors.Is(err, errno.Busy) {
		t.Fatalf("未初始化应报 EBUSY: %v", err)
	}
}

func TestSnapshotRestoreContextCancellation(t *testing.T) {
	table := populatedTable(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := table.Snapshot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应中止快照: %v", err)
	}
	target, err := New(4)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Restore(ctx, []byte(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应中止恢复: %v", err)
	}
}

func mustMarshal(t *testing.T, snapshot tableSnapshot) []byte {
	t.Helper()
	data, err := encodeSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func encodeSnapshot(snapshot tableSnapshot) ([]byte, error) {
	return marshalTableSnapshot(snapshot)
}
