package devfs

import (
	"context"
	"io"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type file struct {
	fs     *FS
	name   string
	flags  uint32
	closed bool
}

func (f *file) Read(buffer []byte) (int, error) {
	if f.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if len(buffer) == 0 {
		return 0, nil
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
	case ttyName:
		if f.fs.terminal == nil {
			return 0, errno.Wrap(errno.NoDevice, "没有控制终端")
		}
		return f.fs.terminal.Read(buffer)
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
	switch f.name {
	case nullName:

		return len(buffer), nil
	case zeroName:

		return len(buffer), nil
	case randomName, urandomName:

		return 0, errno.Wrap(errno.Permission, "随机设备不接受写入: "+f.name)
	case ttyName:
		if f.fs.terminal == nil {
			return 0, errno.Wrap(errno.NoDevice, "没有控制终端")
		}
		return f.fs.terminal.Write(buffer)
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

func (f *file) Stat() (stdfs.FileInfo, error) {
	if f.closed {
		return nil, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return info{name: f.name, mode: f.fs.deviceMode(f.name)}, nil
}

func (f *file) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return nil
}
