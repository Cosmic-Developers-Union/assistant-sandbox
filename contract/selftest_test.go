package contract

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type report struct {
	failed bool
	msg    string
}

func (r *report) record(msg string) {
	r.failed = true
	if r.msg == "" {
		r.msg = msg
	}
}

func (r *report) Helper() {}
func (r *report) Error(args ...any) {
	r.record(join(args...))
}
func (r *report) Errorf(format string, args ...any) {
	r.record(fmt.Sprintf(format, args...))
}
func (r *report) Fatal(args ...any) {
	r.record(join(args...))
	runtime.Goexit()
}
func (r *report) Fatalf(format string, args ...any) {
	r.record(fmt.Sprintf(format, args...))
	runtime.Goexit()
}

func join(args ...any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, fmt.Sprint(arg))
	}
	return strings.Join(parts, " ")
}

func runVector(vector Vector, factory Factory) *report {
	r := &report{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		vector.Run(r, factory)
	}()
	<-done
	return r
}

func TestVectorsCatchViolations(t *testing.T) {
	violations := []struct {
		name    string
		vector  string
		factory Factory
		want    string
	}{
		{
			name:    "错误终态与输出",
			vector:  "ExecReturnsExitStateAndOutput",
			factory: func() (Session, error) { return &brokenSession{}, nil },
			want:    "state=",
		},
		{
			name:    "重复等待结果不一致",
			vector:  "WaitIsRepeatableAndConcurrent",
			factory: func() (Session, error) { return &unstableWaitSession{}, nil },
			want:    "state=",
		},
		{
			name:    "取消等待杀掉进程",
			vector:  "CancelWaitDoesNotKillProcess",
			factory: func() (Session, error) { return &cancelKillsSession{}, nil },
			want:    "state=",
		},
		{
			name:    "观测失败",
			vector:  "UnknownProgramFailsWithoutResidue",
			factory: func() (Session, error) { return &inspectFailSession{}, nil },
			want:    "Inspect 故障",
		},
		{
			name:    "关闭后仍接受任务",
			vector:  "CloseIsIdempotentAndRefusesNewTasks",
			factory: func() (Session, error) { return &ignoresCloseSession{}, nil },
			want:    "关闭后的会话必须拒绝新任务",
		},
		{
			name:    "信号投递失败",
			vector:  "SignalTerminatesProcess",
			factory: func() (Session, error) { return &signalFailSession{}, nil },
			want:    "state=",
		},
		{

			name:    "未知程序未失败",
			vector:  "UnknownProgramFailsWithoutResidue",
			factory: func() (Session, error) { return &acceptsUnknownSession{}, nil },
			want:    "未知程序必须失败",
		},
		{

			name:    "解析失败留下残留",
			vector:  "UnknownProgramFailsWithoutResidue",
			factory: func() (Session, error) { return &residueSession{}, nil },
			want:    "残留进程",
		},
		{
			name:    "执行返回错误",
			vector:  "ExecReturnsExitStateAndOutput",
			factory: func() (Session, error) { return &execFailsSession{}, nil },
			want:    "执行失败",
		},
		{
			name:    "等待返回错误",
			vector:  "ExecReturnsExitStateAndOutput",
			factory: func() (Session, error) { return &waitFailsSession{}, nil },
			want:    "等待失败",
		},
		{

			name:    "Close 不幂等",
			vector:  "CloseIsIdempotentAndRefusesNewTasks",
			factory: func() (Session, error) { return &nonIdempotentCloseSession{}, nil },
			want:    "Close 必须幂等",
		},
		{

			name:    "并发等待返回错误",
			vector:  "WaitIsRepeatableAndConcurrent",
			factory: func() (Session, error) { return &concurrentWaitFailsSession{}, nil },
			want:    "等待失败",
		},
		{

			name:    "输出不符",
			vector:  "ExecReturnsExitStateAndOutput",
			factory: func() (Session, error) { return &silentEchoSession{}, nil },
			want:    "out=",
		},
		{

			name:    "首次等待失败",
			vector:  "WaitIsRepeatableAndConcurrent",
			factory: func() (Session, error) { return &waitFailsSession{}, nil },
			want:    "等待失败",
		},
		{

			name:    "取消等待不报错",
			vector:  "CancelWaitDoesNotKillProcess",
			factory: func() (Session, error) { return &cancelIgnoredSession{}, nil },
			want:    "取消的等待应返回取消错误",
		},
		{

			name:    "终止向量信号失败",
			vector:  "SignalTerminatesProcess",
			factory: func() (Session, error) { return &signalRejectSession{}, nil },
			want:    "信号失败",
		},
		{

			name:    "Close 首次失败",
			vector:  "CloseIsIdempotentAndRefusesNewTasks",
			factory: func() (Session, error) { return &closeFailsSession{}, nil },
			want:    "Close 失败",
		},
		{

			name:    "残留检查观测失败",
			vector:  "UnknownProgramFailsWithoutResidue",
			factory: func() (Session, error) { return &inspectOnceThenFailSession{}, nil },
			want:    "Inspect 故障",
		},
	}
	vectors := make(map[string]Vector)
	for _, vector := range Vectors() {
		vectors[vector.Name] = vector
	}
	for _, violation := range violations {
		t.Run(violation.name, func(t *testing.T) {
			target, ok := vectors[violation.vector]
			if !ok {
				t.Fatalf("向量 %s 未登记", violation.vector)
			}
			got := runVector(target, violation.factory)
			if !got.failed {
				t.Fatalf("向量 %s 未发现违约：%s", violation.vector, violation.name)
			}
			if !strings.Contains(got.msg, violation.want) {
				t.Fatalf("向量 %s 的失败原因=%q 未包含 %q", violation.vector, got.msg, violation.want)
			}

			if violation.name == "装配失败" {
				for _, vector := range Vectors() {
					if !runVector(vector, violation.factory).failed {
						t.Fatalf("装配失败在向量 %s 下未被报告", vector.Name)
					}
				}
			}
		})
	}
}

type baseSession struct {
	closed bool
}

func (s *baseSession) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed {
		return nil, errors.New("会话已关闭")
	}
	if spec.Program == "definitely-missing" {
		return nil, errors.New("程序不存在")
	}
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}
func (s *baseSession) Inspect(context.Context) (Snapshot, error) { return Snapshot{}, nil }
func (s *baseSession) Close(context.Context) error               { s.closed = true; return nil }

type fixedProcess struct{ state ExitState }

func (p *fixedProcess) ID() uint64                              { return 1 }
func (p *fixedProcess) Wait(context.Context) (ExitState, error) { return p.state, nil }
func (p *fixedProcess) Signal(context.Context, string) error    { return nil }

type brokenSession struct{ baseSession }

func (s *brokenSession) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if spec.Program == "definitely-missing" {
		return nil, errors.New("程序不存在")
	}
	return &fixedProcess{state: ExitState{Code: 9, Reason: ReasonExit}}, nil
}

type unstableWaitSession struct{ baseSession }

func (s *unstableWaitSession) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if spec.Program == "definitely-missing" {
		return nil, errors.New("程序不存在")
	}
	return &unstableProcess{}, nil
}

type unstableProcess struct{ waits int }

func (p *unstableProcess) ID() uint64 { return 1 }
func (p *unstableProcess) Wait(context.Context) (ExitState, error) {
	p.waits++
	if p.waits == 1 {
		return ExitState{Code: 0, Reason: ReasonExit}, nil
	}
	return ExitState{Code: 1, Reason: ReasonExit}, nil
}
func (p *unstableProcess) Signal(context.Context, string) error { return nil }

type cancelKillsSession struct{ baseSession }

func (s *cancelKillsSession) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if spec.Program == "definitely-missing" {
		return nil, errors.New("程序不存在")
	}
	return &killOnCancelProcess{}, nil
}

type killOnCancelProcess struct{ killed bool }

func (p *killOnCancelProcess) ID() uint64 { return 1 }
func (p *killOnCancelProcess) Wait(ctx context.Context) (ExitState, error) {
	if err := ctx.Err(); err != nil {

		p.killed = true
		return ExitState{}, err
	}
	if p.killed {
		return ExitState{Code: 0, Reason: ReasonExit}, nil
	}
	return ExitState{Code: 143, Reason: ReasonSignaled, Signal: "SIGTERM"}, nil
}
func (p *killOnCancelProcess) Signal(context.Context, string) error { p.killed = true; return nil }

type inspectFailSession struct{ baseSession }

func (s *inspectFailSession) Inspect(context.Context) (Snapshot, error) {
	return Snapshot{}, errors.New("Inspect 故障")
}

type ignoresCloseSession struct{ baseSession }

func (s *ignoresCloseSession) Close(context.Context) error { return nil }
func (s *ignoresCloseSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}

type signalFailSession struct{ baseSession }

func (s *signalFailSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}

type acceptsUnknownSession struct{ baseSession }

func (s *acceptsUnknownSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}

type residueSession struct {
	baseSession
	residue bool
}

func (s *residueSession) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if spec.Program == "definitely-missing" {
		s.residue = true
		return nil, errors.New("程序不存在")
	}
	return s.baseSession.Exec(ctx, spec)
}
func (s *residueSession) Inspect(context.Context) (Snapshot, error) {
	snapshot := Snapshot{}
	if s.residue {
		snapshot.Processes = append(snapshot.Processes, ProcessView{ID: 7, Program: "definitely-missing"})
	}
	return snapshot, nil
}

type execFailsSession struct{ baseSession }

func (s *execFailsSession) Exec(context.Context, ExecSpec) (Process, error) {
	return nil, errors.New("执行失败")
}

type waitFailsSession struct{ baseSession }

func (s *waitFailsSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &faultyProcess{}, nil
}

type faultyProcess struct{}

func (p *faultyProcess) ID() uint64 { return 1 }
func (p *faultyProcess) Wait(context.Context) (ExitState, error) {
	return ExitState{}, errors.New("等待失败")
}
func (p *faultyProcess) Signal(context.Context, string) error {
	return errors.New("信号失败")
}

type nonIdempotentCloseSession struct {
	baseSession
	closes int
}

func (s *nonIdempotentCloseSession) Close(ctx context.Context) error {
	s.closes++
	if s.closes > 1 {
		return errors.New("Close 不幂等")
	}
	return nil
}
func (s *nonIdempotentCloseSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}

type concurrentWaitFailsSession struct{ baseSession }

func (s *concurrentWaitFailsSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &firstThenFailingProcess{}, nil
}

type firstThenFailingProcess struct{ waits atomic.Int32 }

func (p *firstThenFailingProcess) ID() uint64 { return 1 }
func (p *firstThenFailingProcess) Wait(context.Context) (ExitState, error) {
	if p.waits.Add(1) == 1 {
		return ExitState{Code: 0, Reason: ReasonExit}, nil
	}
	return ExitState{}, errors.New("等待失败")
}
func (p *firstThenFailingProcess) Signal(context.Context, string) error { return nil }

type silentEchoSession struct{ baseSession }

func (s *silentEchoSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}

type cancelIgnoredSession struct{ baseSession }

func (s *cancelIgnoredSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &ignoresCancelProcess{}, nil
}

type ignoresCancelProcess struct{}

func (p *ignoresCancelProcess) ID() uint64 { return 1 }
func (p *ignoresCancelProcess) Wait(context.Context) (ExitState, error) {
	return ExitState{Code: 0, Reason: ReasonExit}, nil
}
func (p *ignoresCancelProcess) Signal(context.Context, string) error { return nil }

type signalRejectSession struct{ baseSession }

func (s *signalRejectSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &rejectsSignalProcess{}, nil
}

type rejectsSignalProcess struct{}

func (p *rejectsSignalProcess) ID() uint64 { return 1 }
func (p *rejectsSignalProcess) Wait(context.Context) (ExitState, error) {
	return ExitState{Code: 0, Reason: ReasonExit}, nil
}
func (p *rejectsSignalProcess) Signal(context.Context, string) error {
	return errors.New("信号失败")
}

type closeFailsSession struct{ baseSession }

func (s *closeFailsSession) Close(context.Context) error { return errors.New("Close 失败") }
func (s *closeFailsSession) Exec(context.Context, ExecSpec) (Process, error) {
	return &fixedProcess{state: ExitState{Code: 0, Reason: ReasonExit}}, nil
}

type inspectOnceThenFailSession struct {
	baseSession
	inspections int
}

func (s *inspectOnceThenFailSession) Inspect(context.Context) (Snapshot, error) {
	s.inspections++
	if s.inspections > 1 {
		return Snapshot{}, errors.New("Inspect 故障")
	}
	return Snapshot{}, nil
}
func (s *inspectOnceThenFailSession) Exec(ctx context.Context, spec ExecSpec) (Process, error) {
	if spec.Program == "definitely-missing" {
		return nil, errors.New("程序不存在")
	}
	return s.baseSession.Exec(ctx, spec)
}
