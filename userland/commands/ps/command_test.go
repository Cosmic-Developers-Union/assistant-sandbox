package ps

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type tableStub struct {
	proc.Control
	self      proc.PID
	processes []proc.Proc
}

func (s *tableStub) Processes() []proc.Proc { return s.processes }

func fixture() *tableStub {
	return &tableStub{
		self: 1,
		processes: []proc.Proc{
			{ID: 1, Parent: 0, Program: "bash", State: proc.Running, PGID: 1, SID: 1, Dir: "/work", Umask: 0o022},
			{ID: 2, Parent: 1, Program: "/bin/cat", Args: []string{"a.txt"}, State: proc.Running, PGID: 1, SID: 1, Umask: 0o077},
			{ID: 3, Parent: 1, Program: "sleep", Args: []string{"5"}, State: proc.Zombie, ExitCode: 0, PGID: 1, SID: 1},
			{ID: 4, Parent: 0, Program: "other", State: proc.Ready, PGID: 4, SID: 4},
		},
	}
}

func runPs(t *testing.T, args []string, stub *tableStub) commandtest.Result {
	t.Helper()
	return commandtest.RunWith(t, Run, t.TempDir(), args, "", commandtest.Options{Control: stub, SID: stub.self})
}

func TestDefaultListsCurrentSession(t *testing.T) {
	result := runPs(t, nil, fixture())
	if result.Code != 0 {
		t.Fatalf("ps 得到 %+v", result)
	}
	lines := strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n")
	if lines[0] != "PID STATE COMM" {
		t.Fatalf("表头 = %q", lines[0])
	}
	want := []string{"1 running bash", "2 running cat", "3 zombie sleep"}
	if len(lines) != len(want)+1 {
		t.Fatalf("默认应只列当前会话的三个进程: %q", result.Stdout)
	}
	for index, expected := range want {
		if lines[index+1] != expected {
			t.Fatalf("第 %d 行 = %q，期望 %q", index+1, lines[index+1], expected)
		}
	}
}

func TestAllAndPIDSelection(t *testing.T) {
	for _, flag := range []string{"-e", "-A"} {
		result := runPs(t, []string{flag}, fixture())
		if result.Code != 0 || !strings.Contains(result.Stdout, "other") {
			t.Fatalf("%s 应列出全部进程: %+v", flag, result)
		}
	}
	result := runPs(t, []string{"-p", "3"}, fixture())
	if result.Code != 0 || !strings.Contains(result.Stdout, "sleep") || strings.Contains(result.Stdout, "bash") {
		t.Fatalf("-p 3 得到 %q", result.Stdout)
	}

	result = runPs(t, []string{"-p", "2,4", "-p", "3"}, fixture())
	for _, name := range []string{"cat", "other", "sleep"} {
		if !strings.Contains(result.Stdout, name) {
			t.Fatalf("-p 多值应包含 %s: %q", name, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "bash") {
		t.Fatalf("-p 多值不应包含未选中的进程: %q", result.Stdout)
	}
}

func TestColumns(t *testing.T) {
	result := runPs(t, []string{"-o", "pid,ppid,pgid,sid,comm,args,dir,umask", "-p", "2"}, fixture())
	if result.Code != 0 {
		t.Fatalf("-o 得到 %+v", result)
	}
	lines := strings.Split(strings.TrimRight(result.Stdout, "\n"), "\n")
	if lines[0] != "PID PPID PGID SID COMM ARGS DIR UMASK" {
		t.Fatalf("表头 = %q", lines[0])
	}
	if lines[1] != "2 1 1 1 cat /bin/cat a.txt  0077" {
		t.Fatalf("数据行 = %q", lines[1])
	}
	full := runPs(t, []string{"-f", "-p", "1"}, fixture())
	if !strings.HasPrefix(full.Stdout, "PID PPID PGID SID STATE COMM\n") {
		t.Fatalf("-f 表头 = %q", full.Stdout)
	}
	for _, bad := range [][]string{{"-o", "pid,uid"}, {"-o", ""}, {"-o", "pid,"}, {"-p", "0"}, {"-p", "abc"}} {
		result := runPs(t, bad, fixture())
		if bad[1] == "pid," {

			if result.Code != 0 {
				t.Fatalf("%v 应当被接受: %+v", bad, result)
			}
			continue
		}
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("%v 应当报错: %+v", bad, result)
		}
	}
}

func TestWithoutTablePort(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), nil, "")
	if result.Code == 0 || !strings.Contains(result.Stderr, "进程表") {
		t.Fatalf("缺端口应如实报错: %+v", result)
	}
}
