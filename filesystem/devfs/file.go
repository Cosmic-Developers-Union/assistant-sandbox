package devfs

import (
	"context"
	"io"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

type file struct {
	fs     *FS
	name   string
	flags  uint32
	closed bool
	// stream 是 PTY 主端等动态设备流(P08);为 nil 时走静态设备分支。
	stream io.ReadWriteCloser
}

func (f *file) Read(buffer []byte) (int, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if f.stream != nil {
		return f.stream.Read(buffer)
	}
	if isTerminalName(f.name) {
		if f.fs.terminal == nil {
			return 0, errno.Wrap(errno.NoDevice, "没有控制终端")
		}
		return f.fs.terminal.Read(buffer)
	}
	switch f.name {
	case nullName:

		return 0, io.EOF
	case zeroName:

		for index := range buffer {
			buffer[index] = 0
		}
		return len(buffer), nil
	case randomName, urandomName:

		return f.fs.entropy.Read(context.Background(), buffer)
	default:
		return 0, errno.Wrap(errno.NotFound, "没有该设备节点: "+f.name)
	}
}

func (f *file) Write(buffer []byte) (int, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}

	if f.flags&(1|2) == 0 {
		return 0, errno.Wrap(errno.Permission, "句柄以只读方式打开: "+f.name)
	}
	if f.stream != nil {
		return f.stream.Write(buffer)
	}
	if isTerminalName(f.name) {
		if f.fs.terminal == nil {
			return 0, errno.Wrap(errno.NoDevice, "没有控制终端")
		}
		return f.fs.terminal.Write(buffer)
	}
	switch f.name {
	case nullName:

		return len(buffer), nil
	case zeroName:

		return len(buffer), nil
	case randomName, urandomName:

		return 0, errno.Wrap(errno.Permission, "随机设备不接受写入: "+f.name)
	default:
		return 0, errno.Wrap(errno.NotFound, "没有该设备节点: "+f.name)
	}
}

func (f *file) Seek(int64, int) (int64, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return 0, errno.Wrap(errno.NotSupported, "设备节点不可定位: "+f.name)
}

func (f *file) Dimensions() (uint16, uint16) {
	if terminal, ok := f.stream.(interface{ Dimensions() (uint16, uint16) }); ok {
		return terminal.Dimensions()
	}
	if terminal, ok := f.fs.terminal.(interface{ Dimensions() (uint16, uint16) }); ok {
		return terminal.Dimensions()
	}
	return 24, 80
}

func (f *file) State() fd.TerminalState {
	if terminal, ok := f.stream.(fd.StatefulTerminal); ok {
		return terminal.State()
	}
	if terminal, ok := f.fs.terminal.(fd.StatefulTerminal); ok {
		return terminal.State()
	}
	return fd.TerminalState{}
}

func (f *file) PTYNumber() uint32 {
	if numbered, ok := f.stream.(interface{ PTYNumber() uint32 }); ok {
		return numbered.PTYNumber()
	}
	return 0
}

func (f *file) SetState(state fd.TerminalState) error {
	if terminal, ok := f.stream.(fd.StatefulTerminal); ok {
		return terminal.SetState(state)
	}
	if terminal, ok := f.fs.terminal.(fd.StatefulTerminal); ok {
		return terminal.SetState(state)
	}
	return errno.Wrap(errno.NotSupported, "设备不支持终端状态")
}

func (f *file) Stat() (stdfs.FileInfo, error) {
	if f.closed {
		return nil, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return info{name: f.name, mode: f.fs.deviceMode(f.name)}, nil
}

// StatusFlags/SetStatusFlags 转发给动态设备(PTY 主端)的打开状态;
// signal.set_wakeup_fd 等路径会对设备 fd 做 fcntl 校验。
func (f *file) StatusFlags() (uint32, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if flagger, ok := f.stream.(interface {
		StatusFlags() (uint32, error)
	}); ok {
		return flagger.StatusFlags()
	}
	return f.flags & (0o3 | 0o2000 | 0o4000), nil
}

func (f *file) SetStatusFlags(flags uint32) error {
	if f.closed {
		return errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if setter, ok := f.stream.(interface{ SetStatusFlags(uint32) error }); ok {
		return setter.SetStatusFlags(flags)
	}
	f.flags = f.flags&^(0o2000|0o4000) | flags&(0o2000|0o4000)
	return nil
}

// Ready/OnChange 转发动态设备的就绪图;静态设备恒就绪。
func (f *file) Ready() poll.Readiness {
	if source, ok := f.stream.(poll.Source); ok {
		return source.Ready()
	}
	return poll.Readable | poll.Writable
}

func (f *file) OnChange(fn func()) (cancel func()) {
	if source, ok := f.stream.(poll.Source); ok {
		return source.OnChange(fn)
	}
	return func() {}
}

func (f *file) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.stream != nil {
		return f.stream.Close()
	}
	return nil
}
