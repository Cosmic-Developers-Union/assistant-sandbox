package fd

import (
	"context"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/epoll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/eventfd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signalfd"
)

type Stream interface {
	io.Reader
	io.Writer
	io.Closer
}

// Creator 是描述符表创建内核对象描述符的能力(管道、eventfd、
// signalfd、按最低空闲编号复制)。作用域包装器实现本接口时以自己
// 绑定的 pid 强制调用方身份,忽略传入的 pid。
type Creator interface {
	Pipe(pid uint64) (read, write uint32, err error)

	EventFD(pid uint64, initialValue uint64, options eventfd.Options) (uint32, error)

	SignalFD(pid uint64, mask uint64, options signalfd.Options) (uint32, error)

	// SignalFDAt 取已存在描述符上的 signalfd(更新掩码用)。
	SignalFDAt(pid uint64, id uint32) (*signalfd.FD, error)

	// Epoll 创建并安装一个 epoll 实例;EpollAt 取已存在实例
	// (epoll_ctl/epoll_pwait 用,L07)。
	Epoll(pid uint64) (uint32, error)

	EpollAt(pid uint64, id uint32) (*epoll.FD, error)

	DupAt(ctx context.Context, pid, from, min uint64) (uint32, error)
}

type Terminal struct {
	TTY           bool
	Rows, Columns uint16

	State TerminalState
}

type TerminalState struct {
	Canonical bool

	Echo bool

	ISIG bool

	Mesg bool

	Rows, Columns uint16
}

type StatefulTerminal interface {
	State() TerminalState
	SetState(TerminalState) error
}

type TabStopSetter interface {
	TabStops() []uint16
	SetTabStops(stops []uint16) error
}

type Descriptors interface {
	Get(context.Context, uint64, uint32) (Stream, error)
	Dup(context.Context, uint64, uint32, uint32) error
	Close(context.Context, uint64, uint32) error
	Terminal(context.Context, uint64, uint32) (Terminal, error)
}

type TerminalSetter interface {
	SetTerminalState(context.Context, uint64, uint32, TerminalState) error
}

type Installer interface {
	Descriptors
	InstallNext(uint64, Stream) (uint32, error)
	SetCloexec(uint64, uint32, bool) error

	// Cloexec 查询描述符的 FD_CLOEXEC 标志(F_GETFD)。
	Cloexec(pid uint64, id uint32) (bool, error)
}

type Bindings interface {
	Attach(uint64, Streams)
	Detach(uint64)
}
