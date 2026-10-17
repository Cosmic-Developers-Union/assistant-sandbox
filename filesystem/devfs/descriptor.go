package devfs

import (
	"io"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type descriptorFile struct {
	stream io.ReadWriteCloser
	id     uint32
	flags  uint32
	closed bool
}

func (d *descriptorFile) Read(buffer []byte) (int, error) {
	if d.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if !d.readable() {
		return 0, errno.Wrap(errno.Permission, "描述符以只写方式打开")
	}
	return d.stream.Read(buffer)
}

func (d *descriptorFile) Write(buffer []byte) (int, error) {
	if d.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	if !d.writable() {
		return 0, errno.Wrap(errno.Permission, "描述符以只读方式打开")
	}
	return d.stream.Write(buffer)
}

func (d *descriptorFile) Seek(int64, int) (int64, error) {
	if d.closed {
		return 0, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return 0, errno.Wrap(errno.NotSupported, "描述符句柄不可定位")
}

func (d *descriptorFile) Stat() (stdfs.FileInfo, error) {
	if d.closed {
		return nil, errno.Wrap(errno.Cancelled, "句柄已关闭")
	}
	return info{name: itoa(d.id), mode: 0o666}, nil
}

func (d *descriptorFile) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	return nil
}

func (d *descriptorFile) readable() bool {
	return d.flags&writeOnlyFlag == 0 || d.flags&readWriteFlag != 0
}

func (d *descriptorFile) writable() bool {
	return d.flags&(writeOnlyFlag|readWriteFlag) != 0
}

const (
	writeOnlyFlag uint32 = 1
	readWriteFlag uint32 = 2
)

var _ vmfs.File = (*descriptorFile)(nil)
