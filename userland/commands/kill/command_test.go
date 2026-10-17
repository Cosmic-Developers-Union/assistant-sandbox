package killcmd

import (
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
)

type delivery struct {
	pid    proc.PID
	number signal.Number
}

type signalStub struct {
	proc.Control
	sent      []delivery
	missing   map[proc.PID]bool
	failWith  error
	existsErr error
}

func (s *signalStub) Signal(pid proc.PID, number signal.Number) error {
	if s.failWith != nil {
		return s.failWith
	}
	if s.missing[pid] {
		return errno.NoProcess
	}
	s.sent = append(s.sent, delivery{pid: pid, number: number})
	return nil
}

func (s *signalStub) Exists(pid proc.PID) (bool, error) {
	if s.existsErr != nil {
		return false, s.existsErr
	}
	if s.missing[pid] {
		return false, nil
	}
	return true, nil
}

func TestSignalParsingAndDelivery(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name string
		args []string
		want []delivery
	}{
		{"默认 SIGTERM", []string{"7"}, []delivery{{7, signal.TERM}}},
		{"短横线加名字", []string{"-KILL", "9"}, []delivery{{9, signal.KILL}}},
		{"带 SIG 前缀且小写", []string{"-sigint", "3"}, []delivery{{3, signal.INT}}},
		{"-s 形式", []string{"-s", "HUP", "4"}, []delivery{{4, signal.HUP}}},
		{"--signal= 形式", []string{"--signal=USR1", "5"}, []delivery{{5, signal.USR1}}},
		{"数字信号", []string{"-15", "6"}, []delivery{{6, signal.TERM}}},
		{"多个 pid", []string{"-TERM", "1", "2"}, []delivery{{1, signal.TERM}, {2, signal.TERM}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &signalStub{missing: map[proc.PID]bool{}}
			result := commandtest.RunWithControl(t, Run, dir, test.args, "", stub)
			if result.Code != 0 || result.Stderr != "" {
				t.Fatalf("%v 得到 %+v", test.args, result)
			}
			if len(stub.sent) != len(test.want) {
				t.Fatalf("%v 投递了 %v，期望 %v", test.args, stub.sent, test.want)
			}
			for index, want := range test.want {
				if stub.sent[index] != want {
					t.Fatalf("%v 第 %d 次投递 = %v，期望 %v", test.args, index, stub.sent[index], want)
				}
			}
		})
	}
}

func TestSignalZeroChecksExistence(t *testing.T) {
	dir := t.TempDir()
	stub := &signalStub{missing: map[proc.PID]bool{42: true}}
	result := commandtest.RunWithControl(t, Run, dir, []string{"-0", "42"}, "", stub)
	if result.Code != 1 || !strings.Contains(result.Stderr, "42") {
		t.Fatalf("-0 不存在的 pid 得到 %+v", result)
	}
	if len(stub.sent) != 0 {
		t.Fatalf("信号 0 不应投递: %v", stub.sent)
	}
	if result := commandtest.RunWithControl(t, Run, dir, []string{"-0", "7"}, "", stub); result.Code != 0 {
		t.Fatalf("-0 存在的 pid 得到 %+v", result)
	}
}

func TestFailures(t *testing.T) {
	dir := t.TempDir()
	stub := &signalStub{missing: map[proc.PID]bool{9: true}}
	result := commandtest.RunWithControl(t, Run, dir, []string{"-TERM", "9", "10"}, "", stub)
	if result.Code != 1 || result.Stderr == "" {
		t.Fatalf("对不存在的 pid 投递得到 %+v", result)
	}
	if len(stub.sent) != 1 || stub.sent[0].pid != 10 {
		t.Fatalf("应当继续处理其余 pid: %v", stub.sent)
	}
	for _, args := range [][]string{
		{"-TERM"}, {"-BOGUS", "1"}, {"-99", "1"}, {"-s"}, {"-TERM", "0"}, {"-TERM", "abc"},
		{"-TERM", "-s", "HUP", "1"}, {"-l", "1", "2"},
		{"-257", "1"}, {"-s", "-255", "1"}, {"-l", "257"},
	} {
		if result := commandtest.RunWithControl(t, Run, dir, args, "", stub); result.Code == 0 {
			t.Fatalf("%v 应当失败: %+v", args, result)
		}
	}
}

func TestDeliveryAndExistenceErrors(t *testing.T) {
	dir := t.TempDir()
	stub := &signalStub{missing: map[proc.PID]bool{}, failWith: errno.Invalid}
	result := commandtest.RunWithControl(t, Run, dir, []string{"-TERM", "5"}, "", stub)
	if result.Code != 1 || !strings.Contains(result.Stderr, "5") {
		t.Fatalf("投递失败得到 %+v", result)
	}
	stub = &signalStub{missing: map[proc.PID]bool{}, existsErr: errno.Invalid}
	if result := commandtest.RunWithControl(t, Run, dir, []string{"-0", "5"}, "", stub); result.Code != 1 {
		t.Fatalf("-0 查询失败得到 %+v", result)
	}
	stub = &signalStub{missing: map[proc.PID]bool{}}
	if result := commandtest.RunWithControl(t, Run, dir, []string{"-HUP", "--", "11"}, "", stub); result.Code != 0 || len(stub.sent) != 1 || stub.sent[0].pid != 11 {
		t.Fatalf("-- 之后的 pid 得到 %+v %v", result, stub.sent)
	}
}

func TestListForm(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.RunWithControl(t, Run, dir, []string{"-l"}, "", &signalStub{})
	if result.Code != 0 {
		t.Fatalf("-l 得到 %+v", result)
	}
	lines := strings.Fields(result.Stdout)
	if len(lines) < 15 || lines[0] != "SIGHUP" || lines[8] != "SIGKILL" {
		t.Fatalf("-l 列表形状不符: %v", lines[:min(10, len(lines))])
	}

	result = commandtest.RunWithControl(t, Run, dir, []string{"-l", "143"}, "", &signalStub{})
	if result.Code != 0 || strings.TrimSpace(result.Stdout) != "SIGTERM" {
		t.Fatalf("-l 143 得到 %+v", result)
	}
	result = commandtest.RunWithControl(t, Run, dir, []string{"-l", "9"}, "", &signalStub{})
	if result.Code != 0 || strings.TrimSpace(result.Stdout) != "SIGKILL" {
		t.Fatalf("-l 9 得到 %+v", result)
	}
	if result := commandtest.RunWithControl(t, Run, dir, []string{"-l", "0"}, "", &signalStub{}); result.Code == 0 {
		t.Fatalf("-l 0 应失败: %+v", result)
	}
}

func TestListWithoutControlPort(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-l"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "SIGTERM") {
		t.Fatalf("无端口时 -l 应可用: %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"-TERM", "1"}, "")
	if result.Code == 0 || !strings.Contains(result.Stderr, "投递") {
		t.Fatalf("无端口时投递应失败: %+v", result)
	}
}
