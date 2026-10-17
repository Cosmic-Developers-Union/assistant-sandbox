package wasm

import (
	"context"
	"errors"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type HostHandler func(ctx context.Context, memory GuestMemory, args []uint64) error

type HostBinding struct {
	Module  string
	Entry   string
	Params  []ValueType
	Results []ValueType
	Handler HostHandler
}

type ValueType uint8

const (
	TypeI32 ValueType = iota
	TypeI64
	TypeF32
	TypeF64
)

func (v ValueType) String() string {
	switch v {
	case TypeI32:
		return "i32"
	case TypeI64:
		return "i64"
	case TypeF32:
		return "f32"
	case TypeF64:
		return "f64"
	default:
		return "type(" + strconv.FormatUint(uint64(v), 10) + ")"
	}
}

func (b HostBinding) Validate() error {
	if b.Module == "" || b.Entry == "" {
		return errno.Wrap(errno.Invalid, "宿主函数绑定的模块名与条目名不能为空")
	}
	if b.Handler == nil {
		return errno.Wrap(errno.Invalid, "宿主函数绑定缺少实现")
	}
	return nil
}

var ErrHostCallCancelled = errno.Wrap(errno.Cancelled, "宿主调用已取消")

func checkCancelled(ctx context.Context) error {
	if ctx == nil {
		return errno.Wrap(errno.Invalid, "宿主调用缺少上下文")
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrHostCallCancelled, err)
	}
	return nil
}
