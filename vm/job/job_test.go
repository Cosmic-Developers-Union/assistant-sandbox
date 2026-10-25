package job

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

func setup(t *testing.T) (*proc.Table, proc.PID) {
	t.Helper()
	table, err := proc.New(8)
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
	return table, root.ID
}

func TestMembersAndGroups(t *testing.T) {
	table, init := setup(t)
	worker, _ := table.Create(init, "worker", nil, "/", nil)
	if err := table.Start(worker.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetPGID(init, worker.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	member, _ := table.Create(init, "member", nil, "/", nil)
	if err := table.Start(member.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetPGID(init, member.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	members := Members(table, worker.ID)
	if len(members) != 2 || members[0] != worker.ID || members[1] != member.ID {
		t.Fatalf("members=%v", members)
	}
	leader, ok := Leader(table, worker.ID)
	if !ok || leader != worker.ID {
		t.Fatalf("leader=%v ok=%v", leader, ok)
	}

	if leader, ok := Leader(table, init); !ok || leader != init {
		t.Fatalf("leader=%v ok=%v", leader, ok)
	}
	if !GroupExists(table, worker.ID) || GroupExists(table, 99) {
		t.Fatal("组存在性错误")
	}
}

func TestSessionMembers(t *testing.T) {
	table, init := setup(t)
	worker, _ := table.Create(init, "worker", nil, "/", nil)
	if err := table.Start(worker.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetSID(worker.ID); err != nil {
		t.Fatal(err)
	}
	peer, _ := table.Create(init, "peer", nil, "/", nil)
	if err := table.Start(peer.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetSID(peer.ID); err != nil {
		t.Fatal(err)
	}
	if got := SessionMembers(table, worker.ID); len(got) != 1 || got[0] != worker.ID {
		t.Fatalf("session=%v", got)
	}
	if got := SessionMembers(table, peer.ID); len(got) != 1 || got[0] != peer.ID {
		t.Fatalf("session=%v", got)
	}
	if got := SessionMembers(table, init); len(got) != 1 || got[0] != init {
		t.Fatalf("session=%v", got)
	}

	third, _ := table.Create(init, "third", nil, "/", nil)
	if err := table.Start(third.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetPGID(init, third.ID, third.ID); err != nil {
		t.Fatal(err)
	}
	if err := table.SetSID(third.ID); !errors.Is(err, errno.NotPermitted) {
		t.Fatalf("err=%v", err)
	}
}

func TestSignalGroupDispatch(t *testing.T) {
	table, init := setup(t)
	worker, _ := table.Create(init, "worker", nil, "/", nil)
	if err := table.Start(worker.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetPGID(init, worker.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	member, _ := table.Create(init, "member", nil, "/", nil)
	if err := table.Start(member.ID); err != nil {
		t.Error(err)
	}
	if err := table.SetPGID(init, member.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	var got []proc.PID
	dispatch := func(pid proc.PID, n signal.Number) error {
		got = append(got, pid)
		return nil
	}
	if err := SignalGroup(table, worker.ID, signal.TERM, dispatch); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != worker.ID || got[1] != member.ID {
		t.Fatalf("dispatch=%v", got)
	}

	if err := SignalGroup(table, 99, signal.TERM, dispatch); !errors.Is(err, errno.NoProcess) {
		t.Fatal(err)
	}

	if err := SignalGroup(table, worker.ID, 0, dispatch); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}

	fail := func(pid proc.PID, n signal.Number) error {
		if pid == member.ID {
			return errno.Permission
		}
		return nil
	}
	if err := SignalGroup(table, worker.ID, signal.TERM, fail); !errors.Is(err, errno.Permission) {
		t.Fatal(err)
	}
}
