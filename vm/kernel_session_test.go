package vm

import (
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

// TestKernelProcessGroupsAndSessions 验证 H11:进程组与会话的建立与
// 查询按 Linux 语义工作——子进程继承父进程的进程组与会话;setpgid
// 把子进程移入新组;getsid/setpgid 的权限规则(跨会话、非子进程、
// 会话组长)按 EPERM 拒绝。
func TestKernelProcessGroupsAndSessions(t *testing.T) {
	reg, err := registryFor("echo")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 8)
	if err != nil {
		t.Fatal(err)
	}
	init := kernel.Init()

	child, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	sessions, ok := child.Control.(proc.SessionControl)
	if !ok {
		t.Fatal("进程控制端口未实现会话能力")
	}

	// 子进程默认继承父进程的进程组与会话。
	pgid, err := sessions.GetPGID(0)
	if err != nil {
		t.Fatal(err)
	}
	if pgid != uint64(init) {
		t.Fatalf("继承的进程组=%d want %d", pgid, init)
	}
	sid, err := sessions.GetSID(0)
	if err != nil {
		t.Fatal(err)
	}
	if sid != uint64(init) {
		t.Fatalf("继承的会话=%d want %d", sid, init)
	}

	// 查询他人进程组与不存在的进程。
	if pgid, err := sessions.GetPGID(int64(init)); err != nil || pgid != uint64(init) {
		t.Fatalf("pgid=%d err=%v", pgid, err)
	}
	if _, err := sessions.GetPGID(9999); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("不存在的进程 err=%v want ESRCH", err)
	}
	if _, err := sessions.GetSID(9999); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("不存在的进程 err=%v want ESRCH", err)
	}
	// 负 pid 与不存在的进程一样按 ESRCH 应答(Linux 内核语义)。
	if _, err := sessions.GetPGID(-1); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("负 pid err=%v want ESRCH", err)
	}
	if _, err := sessions.GetSID(-1); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("负 pid err=%v want ESRCH", err)
	}

	// setpgid(0,0):调用方自成一个进程组组长。
	if err := sessions.SetPGID(0, 0); err != nil {
		t.Fatal(err)
	}
	if pgid, err := sessions.GetPGID(0); err != nil || pgid != uint64(child.ID) {
		t.Fatalf("进程组=%d err=%v want %d", pgid, err, child.ID)
	}
	// 已是组长时不能建立新会话(Linux:EPERM)。
	if err := sessions.SetSID(); !errors.Is(err, errno.NotPermitted) {
		t.Fatalf("setsid err=%v want EPERM", err)
	}
	// 组长的会话仍是原会话。
	if sid, err := sessions.GetSID(0); err != nil || sid != uint64(init) {
		t.Fatalf("会话=%d err=%v want %d", sid, err, init)
	}

	// 非子进程不可被改组(EPERM)。
	if err := sessions.SetPGID(int64(init), 0); !errors.Is(err, errno.NotPermitted) {
		t.Fatalf("改组非子进程 err=%v want EPERM", err)
	}

	// 会话隔离:另一个子进程建立新会话后,跨会话改组被拒。
	peer, err := kernel.Begin(0, "echo", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	peerSessions, ok := peer.Control.(proc.SessionControl)
	if !ok {
		t.Fatal("进程控制端口未实现会话能力")
	}
	if err := peerSessions.SetSID(); err != nil {
		t.Fatal(err)
	}
	if sid, err := peerSessions.GetSID(0); err != nil || sid != uint64(peer.ID) {
		t.Fatalf("新会话=%d err=%v want %d", sid, err, peer.ID)
	}
	// 新会话的组长不能改自己的组(会话组长:EPERM)。
	if err := peerSessions.SetPGID(0, 0); !errors.Is(err, errno.NotPermitted) {
		t.Fatalf("会话组长改组 err=%v want EPERM", err)
	}
	// 跨会话改组被拒。
	if err := peerSessions.SetPGID(int64(child.ID), 0); !errors.Is(err, errno.NotPermitted) {
		t.Fatalf("跨会话改组 err=%v want EPERM", err)
	}
}

// TestKernelSessionGroupSignalDelivery 验证 H11:组信号送达进程组内
// 全部成员,空组报 ESRCH;会话建立后组信号不再影响原会话成员。
func TestKernelSessionGroupSignalDelivery(t *testing.T) {
	reg, err := registryFor("long")
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{Set: capability.Set{FD: fd.New()}, Programs: reg}, 8)
	if err != nil {
		t.Fatal(err)
	}
	init := kernel.Init()
	leader, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	member, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := kernel.Begin(0, "long", nil, "/", nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	leaderSessions, ok := leader.Control.(proc.SessionControl)
	if !ok {
		t.Fatal("进程控制端口未实现会话能力")
	}
	// 组长自成一个组,成员随后加入该组。
	if err := leaderSessions.SetPGID(0, 0); err != nil {
		t.Fatal(err)
	}
	memberSessions, ok := member.Control.(proc.SessionControl)
	if !ok {
		t.Fatal("进程控制端口未实现会话能力")
	}
	if err := memberSessions.SetPGID(0, int64(leader.ID)); err != nil {
		t.Fatal(err)
	}
	if pgid, err := memberSessions.GetPGID(0); err != nil || pgid != uint64(leader.ID) {
		t.Fatalf("成员进程组=%d err=%v want %d", pgid, err, leader.ID)
	}

	// 组信号送达组内两名成员,组外进程不受影响。
	if err := kernel.SignalGroup(leader.ID, signal.INT); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []proc.PID{leader.ID, member.ID} {
		pending, err := kernel.signals.Pending(uint64(pid))
		if err != nil || len(pending) != 1 || pending[0] != signal.INT {
			t.Fatalf("pid=%d pending=%v err=%v", pid, pending, err)
		}
	}
	pending, err := kernel.signals.Pending(uint64(outsider.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("组外进程收到信号:%v", pending)
	}
	// 空组报 ESRCH。
	if err := kernel.SignalGroup(9999, signal.INT); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("空组 err=%v want ESRCH", err)
	}
	// init 未加入任何新组,仍是独立进程组。
	if pgid, err := kernel.GetPGID(init, init); err != nil || pgid != init {
		t.Fatalf("init 进程组=%d err=%v want %d", pgid, err, init)
	}
}
