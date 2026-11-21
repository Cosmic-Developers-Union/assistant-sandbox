package common

import (
	"context"
	"encoding/binary"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// P08 终端 ioctl:TCGETS/TCSETS 与窗口大小、PTY 编号。
const (
	tcgets     = 0x5401
	tcsets     = 0x5402
	tcsetsw    = 0x5403
	tcsetsf    = 0x5404
	tiocgwinsz = 0x5413
	tiocswinsz = 0x5414
	tiocgptn   = 0x80045430
	tiocsptlck = 0x40045431
	fionbio    = 0x5421

	termiosSize   = 60
	termiosLFlag  = 12
	termiosCCOff  = 16
	termiosICANON = 0x0002
	termiosECHO   = 0x0008
	termiosISIG   = 0x0001
)

func (s Syscalls) ioctl(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	request := uint32(call.Args[1])
	arg := call.Args[2]
	// FIONBIO 与终端无关,必须在 TTY 判定之前处理:musl/Python
	// 的 socket.setblocking 用它切换 O_NONBLOCK。
	if request == fionbio {
		return s.setNonBlocking(ctx, call)
	}
	if s.Identity == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	terminalSource, ok := s.Descriptors.(interface {
		Terminal(context.Context, uint64) (fd.Terminal, error)
	})
	if !ok {
		return Reply{Errno: errno.NotATerminal}
	}
	terminal, err := terminalSource.Terminal(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if !terminal.TTY {
		return Reply{Errno: errno.NotATerminal}
	}
	switch request {
	case tcgets:
		return s.getTermios(ctx, arg, terminal)
	case tcsets, tcsetsw, tcsetsf:
		return s.setTermios(ctx, call, terminal)
	case tiocgwinsz:
		var raw [8]byte
		binary.LittleEndian.PutUint16(raw[0:], terminal.Rows)
		binary.LittleEndian.PutUint16(raw[2:], terminal.Columns)
		if err := s.Memory.Write(ctx, arg, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		return Reply{}
	case tiocswinsz:
		var raw [8]byte
		if err := s.Memory.Read(ctx, arg, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		state := terminal.State
		state.Rows = binary.LittleEndian.Uint16(raw[0:])
		state.Columns = binary.LittleEndian.Uint16(raw[2:])
		return s.setTerminalState(ctx, call, state)
	case tiocgptn:
		handle, err := s.Descriptors.Get(ctx, call.Args[0])
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		numbered, ok := handle.(interface{ PTYNumber() uint32 })
		if !ok {
			return Reply{Errno: errno.NotATerminal}
		}
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], numbered.PTYNumber())
		if err := s.Memory.Write(ctx, arg, raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
		return Reply{}
	case tiocsptlck:
		var raw [4]byte
		if arg != 0 {
			if err := s.Memory.Read(ctx, arg, raw[:]); err != nil {
				return Reply{Errno: errno.Fault}
			}
		}
		return Reply{}
	default:
		return Reply{Errno: errno.NotATerminal}
	}
}

// setNonBlocking 实现 FIONBIO:arg 指向 4 字节整数,非零表示非阻塞。
func (s Syscalls) setNonBlocking(ctx context.Context, call Call) Reply {
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	flags, ok := handle.(interface {
		StatusFlags() (uint32, error)
	})
	if !ok {
		return Reply{Errno: errno.NotATerminal}
	}
	setter, ok := handle.(interface {
		SetStatusFlags(uint32) error
	})
	if !ok {
		return Reply{Errno: errno.NotATerminal}
	}
	if call.Args[2] == 0 {
		return Reply{Errno: errno.Fault}
	}
	var raw [4]byte
	if err := s.Memory.Read(ctx, call.Args[2], raw[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	current, err := flags.StatusFlags()
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if binary.LittleEndian.Uint32(raw[:]) != 0 {
		current |= oNonBlock
	} else {
		current &^= oNonBlock
	}
	if err := setter.SetStatusFlags(current); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) getTermios(ctx context.Context, address uint64, terminal fd.Terminal) Reply {
	var raw [termiosSize]byte
	var flags uint32
	if terminal.State.Canonical {
		flags |= termiosICANON
	}
	if terminal.State.Echo {
		flags |= termiosECHO
	}
	if terminal.State.ISIG {
		flags |= termiosISIG
	}
	binary.LittleEndian.PutUint32(raw[termiosLFlag:], flags)
	raw[termiosCCOff+0] = 0x03 // VINTR
	raw[termiosCCOff+1] = 0x1c // VQUIT
	raw[termiosCCOff+2] = 0x7f // VERASE
	raw[termiosCCOff+3] = 0x15 // VKILL
	raw[termiosCCOff+4] = 0x04 // VEOF
	raw[termiosCCOff+5] = 0x00 // VTIME
	raw[termiosCCOff+6] = 0x01 // VMIN
	binary.LittleEndian.PutUint32(raw[48:], 38400)
	binary.LittleEndian.PutUint32(raw[52:], 38400)
	if err := s.Memory.Write(ctx, address, raw[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) setTermios(ctx context.Context, call Call, terminal fd.Terminal) Reply {
	var raw [termiosSize]byte
	if err := s.Memory.Read(ctx, call.Args[2], raw[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	flags := binary.LittleEndian.Uint32(raw[termiosLFlag:])
	state := terminal.State
	state.Canonical = flags&termiosICANON != 0
	state.Echo = flags&termiosECHO != 0
	state.ISIG = flags&termiosISIG != 0
	return s.setTerminalState(ctx, call, state)
}

func (s Syscalls) setTerminalState(ctx context.Context, call Call, state fd.TerminalState) Reply {
	setter, ok := s.Descriptors.(interface {
		SetTerminalState(context.Context, uint64, fd.TerminalState) error
	})
	if !ok {
		return Reply{Errno: errno.NotATerminal}
	}
	if err := setter.SetTerminalState(ctx, call.Args[0], state); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}
