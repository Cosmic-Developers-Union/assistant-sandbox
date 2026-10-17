package sandbox

import (
	"io/fs"
	"sync/atomic"

	abi "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)

type devices struct{ abi.UnimplementedFS }

func (devices) Stat(name string) (sys.Stat_t, abi.Errno)  { return deviceStat(name) }
func (devices) Lstat(name string) (sys.Stat_t, abi.Errno) { return deviceStat(name) }
func deviceStat(name string) (sys.Stat_t, abi.Errno) {
	switch name {
	case ".", "", "/":
		return sys.Stat_t{Mode: fs.ModeDir | 0o555, Nlink: 1}, 0
	case "null":
		return sys.Stat_t{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o666, Nlink: 1}, 0
	default:
		return sys.Stat_t{}, abi.ENOENT
	}
}
func (devices) OpenFile(name string, flags abi.Oflag, _ fs.FileMode) (abi.File, abi.Errno) {
	stat, errno := deviceStat(name)
	if errno != 0 {
		return nil, errno
	}
	if stat.Mode.IsDir() && flags&(abi.O_WRONLY|abi.O_RDWR|abi.O_CREAT|abi.O_TRUNC|abi.O_APPEND) != 0 {
		return nil, abi.EISDIR
	}
	return &deviceFile{mode: stat.Mode, flags: flags}, 0
}

type deviceFile struct {
	abi.UnimplementedFile
	mode   fs.FileMode
	flags  abi.Oflag
	closed atomic.Bool
}

func (f *deviceFile) Stat() (sys.Stat_t, abi.Errno) {
	if f.closed.Load() {
		return sys.Stat_t{}, abi.EBADF
	}
	return sys.Stat_t{Mode: f.mode, Nlink: 1}, 0
}
func (f *deviceFile) IsDir() (bool, abi.Errno) { stat, err := f.Stat(); return stat.Mode.IsDir(), err }
func (f *deviceFile) Read(_ []byte) (int, abi.Errno) {
	if f.closed.Load() || f.flags&abi.O_WRONLY != 0 {
		return 0, abi.EBADF
	}
	if f.mode.IsDir() {
		return 0, abi.EISDIR
	}
	return 0, 0
}
func (f *deviceFile) Pread(b []byte, _ int64) (int, abi.Errno) { return f.Read(b) }
func (f *deviceFile) Write(b []byte) (int, abi.Errno) {
	if f.closed.Load() || f.flags&(abi.O_WRONLY|abi.O_RDWR) == 0 {
		return 0, abi.EBADF
	}
	if f.mode.IsDir() {
		return 0, abi.EISDIR
	}
	return len(b), 0
}
func (f *deviceFile) Pwrite(b []byte, _ int64) (int, abi.Errno) { return f.Write(b) }
func (f *deviceFile) Truncate(_ int64) abi.Errno                { _, err := f.Write(nil); return err }
func (f *deviceFile) Close() abi.Errno                          { f.closed.Store(true); return 0 }
