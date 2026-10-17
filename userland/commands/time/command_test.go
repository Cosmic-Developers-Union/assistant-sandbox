package time

import (
	"context"
	"fmt"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

const sysLine = "sys 不可测（客体没有用户态/内核态 CPU 计时）\n"

type controlStub struct {
	specs []proc.RunSpec
	usage proc.Usage

	runErr error
	status proc.Proc

	advance uint64
	clock   *clock.Virtual

	selfWork, childrenWork uint64
}

func (c *controlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotSupported
}

func (c *controlStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (c *controlStub) Run(ctx context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.specs = append(c.specs, spec)
	if c.runErr != nil {
		return proc.Proc{}, c.runErr
	}

	if spec.Name == "echo" {
		if _, err := fmt.Fprintln(spec.Streams.Output, strings.Join(spec.Args, " ")); err != nil {
			return proc.Proc{}, err
		}
	}
	if c.clock != nil && c.advance > 0 {
		if err := c.clock.Sleep(ctx, c.advance); err != nil {
			return proc.Proc{}, err
		}
	}
	c.usage.Self += c.selfWork
	c.usage.Children += c.childrenWork
	return c.status, nil
}

type measuring struct{ *controlStub }

func (m measuring) Usage(proc.PID) (proc.Usage, error) { return m.usage, nil }

func TestReportsRealWorkAndUnmeasurableSys(t *testing.T) {
	virtual := &clock.Virtual{}
	stub := &controlStub{clock: virtual, advance: 1_500_000_000, selfWork: 1, childrenWork: 7}
	dir := t.TempDir()
	result := commandtest.RunWith(t, Run, dir, []string{"echo", "hi"}, "", commandtest.Options{
		Control: measuring{stub}, Clock: virtual, Env: map[string]string{"KEY": "value"},
	})
	want := "real 1.50\nuser 8（确定性工作量，不是秒）\n" + sysLine
	if result.Code != 0 || result.Stdout != "hi\n" || result.Stderr != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
	if len(stub.specs) != 1 {
		t.Fatalf("执行次数: %d", len(stub.specs))
	}
	spec := stub.specs[0]
	if spec.Name != "echo" || strings.Join(spec.Args, " ") != "hi" || spec.Dir != dir {
		t.Fatalf("执行请求: %+v", spec)
	}
	if spec.Env["KEY"] != "value" {
		t.Fatalf("环境应随子进程走: %v", spec.Env)
	}
	if spec.Parent != 1 {
		t.Fatalf("父进程应是当前进程: %d", spec.Parent)
	}
}

func TestPortabilityFlagKeepsHonestOutput(t *testing.T) {
	stub := &controlStub{childrenWork: 5}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"-p", "true"}, "", commandtest.Options{
		Control: measuring{stub},
	})
	want := "real 0.00\nuser 5（确定性工作量，不是秒）\n" + sysLine
	if result.Code != 0 || result.Stderr != want {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestOptionsAfterUtilityBelongToUtility(t *testing.T) {
	stub := &controlStub{}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"echo", "-p", "hi"}, "", commandtest.Options{
		Control: measuring{stub},
	})
	if result.Code != 0 || result.Stdout != "-p hi\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
	if len(stub.specs) != 1 || strings.Join(stub.specs[0].Args, " ") != "-p hi" {
		t.Fatalf("utility 实参: %+v", stub.specs)
	}
}

func TestWithoutUsagePortSaysUnmeasurable(t *testing.T) {
	stub := &controlStub{}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"true"}, "", commandtest.Options{Control: stub})
	want := "real 0.00\nuser 不可测（客体读不到确定性用量）\n" + sysLine
	if result.Code != 0 || result.Stderr != want {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestWithoutClockFailsHonestly(t *testing.T) {
	stub := &controlStub{}
	run := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}

		set.Clock = nil
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	result := commandtest.RunWith(t, run, t.TempDir(), []string{"echo"}, "", commandtest.Options{Control: stub})
	if result.Code != 2 || !strings.Contains(result.Stderr, "没有时钟能力") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if len(stub.specs) != 0 {
		t.Fatal("没有时钟时不应执行 utility")
	}
}

func TestMissingUtilityIsNotInvoked(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		reason error
		code   int
		text   string
	}{
		{"找不到", errno.NotFound, 127, "未注册沙箱命令"},
		{"不能执行", errno.NoExec, 126, "无法执行"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &controlStub{runErr: testCase.reason}
			result := commandtest.RunWith(t, Run, t.TempDir(), []string{"echo"}, "", commandtest.Options{Control: stub})
			if result.Code != testCase.code || !strings.Contains(result.Stderr, testCase.text) {
				t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
			}
			if strings.Contains(result.Stderr, "real ") {
				t.Fatalf("utility 没被调用时不应输出统计: %q", result.Stderr)
			}
		})
	}
}

func TestPropagatesUtilityStatus(t *testing.T) {
	stub := &controlStub{status: proc.Proc{ExitCode: 3}}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"false"}, "", commandtest.Options{Control: stub})
	if result.Code != 3 {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if !strings.HasPrefix(result.Stderr, "real 0.00\n") || !strings.HasSuffix(result.Stderr, sysLine) {
		t.Fatalf("非零退出时统计仍应输出: %q", result.Stderr)
	}
}

func TestArgumentErrors(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{"缺 utility", nil},
		{"未知选项", []string{"-z", "echo"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWith(t, Run, t.TempDir(), testCase.args, "", commandtest.Options{Control: stub})
			if result.Code != 2 || result.Stderr == "" {
				t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
			}
			if len(stub.specs) != 0 {
				t.Fatalf("参数错误时不应执行 utility: %+v", stub.specs)
			}
		})
	}
}

func TestMissingControlPortFails(t *testing.T) {
	result := commandtest.Run(t, Run, t.TempDir(), []string{"echo"}, "")
	if result.Code != 2 || !strings.Contains(result.Stderr, "缺少进程控制端口") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

type clockStub struct {
	values []uint64

	failFrom int
	failErr  error
	reads    int
}

func (c *clockStub) Wall(context.Context) (int64, error) { return 0, c.failErr }

func (c *clockStub) Sleep(context.Context, uint64) error { return nil }

func (c *clockStub) Monotonic(context.Context) (uint64, error) {
	index := c.reads
	c.reads++
	if c.failFrom >= 0 && index >= c.failFrom {
		return 0, c.failErr
	}
	if len(c.values) == 0 {
		return 0, nil
	}
	if index >= len(c.values) {
		index = len(c.values) - 1
	}
	return c.values[index], nil
}

func TestClockReadFailureFails(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		clock *clockStub
		ran   int
	}{
		{"开始时刻读不到", &clockStub{failFrom: 0, failErr: errno.NotSupported}, 0},
		{"结束时刻读不到", &clockStub{values: []uint64{0}, failFrom: 1, failErr: errno.NotSupported}, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &controlStub{}
			result := commandtest.RunWith(t, Run, t.TempDir(), []string{"echo"}, "", commandtest.Options{
				Control: stub, Clock: testCase.clock,
			})
			if result.Code != 2 || result.Stderr == "" {
				t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
			}
			if len(stub.specs) != testCase.ran {
				t.Fatalf("执行次数 %d，期望 %d", len(stub.specs), testCase.ran)
			}
		})
	}
}

func TestBackwardsClockReportsZeroReal(t *testing.T) {
	stub := &controlStub{childrenWork: 5}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"true"}, "", commandtest.Options{
		Control: measuring{stub}, Clock: &clockStub{values: []uint64{10, 3}, failFrom: -1},
	})
	want := "real 0.00\nuser 5（确定性工作量，不是秒）\n" + sysLine
	if result.Code != 0 || result.Stderr != want {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

type brokenUsage struct{ *controlStub }

func (b brokenUsage) Usage(proc.PID) (proc.Usage, error) { return proc.Usage{}, errno.NotSupported }

func TestUsageReadFailureSaysUnmeasurable(t *testing.T) {
	stub := &controlStub{childrenWork: 5}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"true"}, "", commandtest.Options{
		Control: brokenUsage{stub},
	})
	want := "real 0.00\nuser 不可测（客体读不到确定性用量）\n" + sysLine
	if result.Code != 0 || result.Stderr != want {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestUnexpectedRunFailureIsReported(t *testing.T) {
	stub := &controlStub{runErr: errno.Invalid}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"echo"}, "", commandtest.Options{Control: stub})
	if result.Code != 2 || !strings.Contains(result.Stderr, "执行 echo") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestHelpExitsCleanly(t *testing.T) {
	stub := &controlStub{}
	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"--help"}, "", commandtest.Options{Control: stub})
	if result.Code != 0 {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if len(stub.specs) != 0 {
		t.Fatal("--help 不应执行 utility")
	}
}

func TestReportWriteFailureIsNotSwallowed(t *testing.T) {
	if err := report(commandtest.FailWriter{}, 0, 0, true); err == nil {
		t.Fatal("写失败必须上报")
	}
	if err := report(commandtest.FailWriter{}, 0, 0, false); err == nil {
		t.Fatal("写失败必须上报（不可测分支）")
	}
}

func TestReportWriteFailurePropagates(t *testing.T) {
	stub := &controlStub{}
	run := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		descriptors, err := p.Resources.Descriptors()
		if err != nil {
			return err
		}
		bindings, ok := descriptors.(interface{ Attach(uint64, fd.Streams) })
		if !ok {
			t.Fatalf("描述符后端类型: %T", descriptors)
		}
		h := commandio.Context(ctx)
		bindings.Attach(uint64(p.ID), fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: commandtest.FailWriter{}})
		return Run(proc.WithContext(ctx, p), args)
	}
	result := commandtest.RunWith(t, run, t.TempDir(), []string{"true"}, "", commandtest.Options{Control: stub})
	if result.Code != 2 {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestElapsedNeverNegative(t *testing.T) {
	if got := elapsed(5, 3); got != 0 {
		t.Fatalf("倒退应折成 0: %d", got)
	}
	if got := elapsed(7, 9); got != 2 {
		t.Fatalf("差值: %d", got)
	}
}

func TestWorkDeltaNeedsBothReadings(t *testing.T) {
	cases := []struct {
		name     string
		before   proc.Usage
		after    proc.Usage
		measured bool
		want     uint64
		ok       bool
	}{
		{"缺失读数", proc.Usage{}, proc.Usage{Self: 4}, false, 0, false},
		{"自身倒退", proc.Usage{Self: 9}, proc.Usage{Self: 4}, true, 0, false},
		{"子进程倒退", proc.Usage{Children: 9}, proc.Usage{Children: 4}, true, 0, false},
		{"自身加子进程", proc.Usage{Self: 2, Children: 3}, proc.Usage{Self: 5, Children: 11}, true, 11, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := workDelta(testCase.before, testCase.after, testCase.measured)
			if got != testCase.want || ok != testCase.ok {
				t.Fatalf("workDelta = (%d, %v)，期望 (%d, %v)", got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

func TestDetachedProcessViewFails(t *testing.T) {
	run := func(ctx context.Context, args []string) error {

		return Run(proc.WithContext(ctx, proc.Proc{}), args)
	}
	result := commandtest.RunWith(t, run, t.TempDir(), []string{"echo"}, "", commandtest.Options{})
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}
