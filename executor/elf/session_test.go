package elf

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// sessionControlStub 模拟内核的会话与进程组语义(H11):进程默认继承
// 父进程的进程组与会话;setpgid(0,0) 使调用方自成组长;已是组长时
// setsid 按 Linux 报 EPERM。
type sessionControlStub struct {
	pid  proc.PID
	pgid proc.PID
	sid  proc.PID
}

// proc.Control 的最小实现:本用例只关心会话端口。
func (s *sessionControlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.Invalid
}

func (s *sessionControlStub) Finish(proc.PID, proc.PID, uint8) error { return errno.Invalid }

func (s *sessionControlStub) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.Invalid
}

func (s *sessionControlStub) SetPGID(pid int64, pgid int64) error {
	if pid != 0 && proc.PID(pid) != s.pid {
		return errno.NotPermitted
	}
	if pgid == 0 {
		s.pgid = s.pid
		return nil
	}
	s.pgid = proc.PID(pgid)
	return nil
}

func (s *sessionControlStub) SetSID() error {
	if s.pgid == s.pid {
		return errno.NotPermitted
	}
	s.sid = s.pid
	s.pgid = s.pid
	return nil
}

func (s *sessionControlStub) GetPGID(int64) (uint64, error) { return uint64(s.pgid), nil }

func (s *sessionControlStub) GetSID(int64) (uint64, error) { return uint64(s.sid), nil }

// sessionProgram 依次执行 getpgid(0)、getsid(0)、setpgid(0,0)、
// getpgid(0)、setsid(),把五个结果存入数据段 0x12000 起的五个 64 位
// 字,再经 write(1, …) 输出,供宿主逐项核对。
var sessionProgram = []uint32{
	0x000125B7, // lui a1, 0x12        → a1 = 0x12000(结果缓冲区)
	0x09B00893, // li a7, 155 (getpgid)
	0x00000513, // li a0, 0
	0x00000073, // ecall → a0 = pgid₁
	0x00A5B023, // sd a0, 0(a1)
	0x09C00893, // li a7, 156 (getsid)
	0x00000513, // li a0, 0
	0x00000073, // ecall → a0 = sid₁
	0x00A5B423, // sd a0, 8(a1)
	0x09A00893, // li a7, 154 (setpgid)
	0x00000513, // li a0, 0
	0x00000593, // li a1, 0
	0x00000073, // ecall → a0 = errno
	0x000125B7, // lui a1, 0x12
	0x00A5B823, // sd a0, 16(a1)
	0x09B00893, // li a7, 155 (getpgid)
	0x00000513, // li a0, 0
	0x00000073, // ecall → a0 = pgid₂
	0x000125B7, // lui a1, 0x12
	0x00A5BC23, // sd a0, 24(a1)
	0x09D00893, // li a7, 157 (setsid)
	0x00000073, // ecall → a0 = errno
	0x000125B7, // lui a1, 0x12
	0x02A5B023, // sd a0, 32(a1)
	0x04000893, // li a7, 64 (write)
	0x00100513, // li a0, 1        (stdout)
	0x000125B7, // lui a1, 0x12    (缓冲区)
	0x02800613, // li a2, 40       (长度)
	0x00000073, // ecall
	0x05D00893, // li a7, 93 (exit)
	0x00000513, // li a0, 0
	0x00000073, // ecall
}

// TestRealELFProcessGroupAndSessionCalls 用真实 RISC-V ELF 验证 H11 的
// 端到端链路:客体经 riscv64 ABI 编号调用 getpgid/getsid/setpgid/
// setsid,内核按 Linux 语义应答(继承的组、errno 与改组后的组号)。
func TestRealELFProcessGroupAndSessionCalls(t *testing.T) {
	harness := newHarness(t, buildImageWith(t, sessionProgram, ""), nil)
	harness.process.Control = &sessionControlStub{pid: harness.process.ID, pgid: 1, sid: 1}

	guest, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := guest.Run(t.Context(), harness.process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if code != 0 {
		t.Fatalf("退出码=%d want 0", code)
	}
	raw := harness.stdout.output.Bytes()
	if len(raw) != 40 {
		t.Fatalf("输出长度=%d want 40(%x)", len(raw), raw)
	}
	values := make([]uint64, 5)
	for index := range values {
		values[index] = binary.LittleEndian.Uint64(raw[index*8:])
	}
	pgid1, sid1, setpgidErrno, pgid2, setsidErrno := values[0], values[1], values[2], values[3], values[4]
	if pgid1 != 1 {
		t.Fatalf("继承的进程组=%d want 1", pgid1)
	}
	if sid1 != 1 {
		t.Fatalf("继承的会话=%d want 1", sid1)
	}
	if setpgidErrno != 0 {
		t.Fatalf("setpgid errno=%d want 0", setpgidErrno)
	}
	if pgid2 != uint64(harness.process.ID) {
		t.Fatalf("改组后的进程组=%d want %d", pgid2, harness.process.ID)
	}
	// setsid:调用方已是组长,Linux 报 EPERM(1),ABI 以 -errno 返回。
	if int64(setsidErrno) != -1 {
		t.Fatalf("setsid errno=%d want -1(EPERM)", int64(setsidErrno))
	}
}
