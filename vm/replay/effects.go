package replay

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Effect uint8

const (
	Unclassified Effect = iota

	SideEffect

	Pure
)

func (e Effect) String() string {
	switch e {
	case SideEffect:
		return "side-effect"
	case Pure:
		return "pure"
	default:
		return "unclassified"
	}
}

type operation struct {
	effect Effect

	note string
}

var registry = map[string]operation{
	OpEntropy:    {SideEffect, "读取客体随机源：重放会得到不同字节"},
	OpBridgeCall: {SideEffect, "宿主桥调用：重放会真的再调一次宿主"},
	OpStdin:      {SideEffect, "读取客体输入：重放会真的再读一次"},
	OpClockRead:  {Pure, "读取客体时钟：可复现是确定性执行的断言，必须重算比对"},
	OpClockSleep: {Pure, "等待：记录前后时刻，回放校验起点并把时钟推进到记录值"},
	OpSchedule:   {SideEffect, "控制点（信号/回收/快照）：重放会改变进程状态序列"},
}

func Classify(name string) (Effect, string) {
	entry, ok := registry[name]
	if !ok {
		return Unclassified, ""
	}
	return entry.effect, entry.note
}

func Register(name string, effect Effect) error {
	if name == "" {
		return errno.Wrap(errno.Invalid, "操作名不能为空")
	}
	if effect != SideEffect && effect != Pure {
		return errno.Wrap(errno.Invalid, "必须显式声明 SideEffect 或 Pure")
	}
	registry[name] = operation{effect, "由扩展适配器登记"}
	return nil
}

func RunTyped(ctx context.Context, journal Journal, pid uint64, operation string, effect Effect, input []byte, run func(context.Context) ([]byte, string, error)) ([]byte, error) {
	if _, err := errnoRequire(journal); err != nil {
		return nil, err
	}
	if role, ok := journal.(interface{ Recording() bool }); ok && role.Recording() {
		return record(ctx, journal, pid, operation, input, run)
	}
	switch effect {
	case SideEffect:
		return reuse(ctx, journal, pid, operation, input)
	case Pure:
		return recompute(ctx, journal, pid, operation, input, run)
	default:
		return nil, errno.Wrap(errno.Invalid, fmt.Sprintf("操作%s未分类，回放拒绝执行", operation))
	}
}

func Run(ctx context.Context, journal Journal, pid uint64, operation string, input []byte, run func(context.Context) ([]byte, string, error)) ([]byte, error) {
	effect, _ := Classify(operation)
	return RunTyped(ctx, journal, pid, operation, effect, input, run)
}

func record(ctx context.Context, journal Journal, pid uint64, operation string, input []byte, run func(context.Context) ([]byte, string, error)) ([]byte, error) {
	output, code, err := run(ctx)
	var codeStr string
	if err != nil {
		if code == "" {
			code = "error"
		}
		codeStr = code
	}
	fact := Fact{PID: pid, Operation: operation, Input: input, Output: output, ErrorCode: codeStr}
	if recordErr := journal.Record(ctx, fact); recordErr != nil {
		return nil, errors.Join(err, recordErr)
	}
	return output, err
}

func reuse(ctx context.Context, journal Journal, pid uint64, operation string, input []byte) ([]byte, error) {
	fact, err := journal.Next(ctx, Fact{PID: pid, Operation: operation, Input: input})
	if err != nil {
		return nil, err
	}
	if fact.ErrorCode != "" {
		return fact.Output, fmt.Errorf("%s: %w", fact.ErrorCode, errno.Invalid)
	}
	return fact.Output, nil
}

func recompute(ctx context.Context, journal Journal, pid uint64, operation string, input []byte, run func(context.Context) ([]byte, string, error)) ([]byte, error) {
	fact, err := journal.Next(ctx, Fact{PID: pid, Operation: operation, Input: input})
	if err != nil {
		return nil, err
	}
	output, code, runErr := run(ctx)
	if runErr != nil || code != "" {
		return nil, fmt.Errorf("纯操作%s在回放时失败: %w", operation, errors.Join(runErr, errno.Invalid))
	}
	if !bytes.Equal(output, fact.Output) {
		position := 0
		if locator, ok := journal.(positioned); ok {
			position = locator.Position()
		}
		return nil, &MismatchError{
			Position: position,
			Expected: fact,
			Actual:   Fact{PID: pid, Operation: operation, Input: input, Output: output},
			Reason:   "纯计算结果与记录不一致",
		}
	}
	return output, nil
}
