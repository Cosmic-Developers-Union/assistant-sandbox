//go:build wasip1

package filesystem

import (
	"os"
	"syscall"
)

//go:wasmimport wasi_snapshot_preview1 fd_filestat_set_times
func setFileTimes(fd uint32, access, modification uint64, flags uint32) uint32

func (f *Filesystem) setTimes(name string, access, modification int64) error {
	file, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if code := setFileTimes(uint32(file.Fd()), uint64(access), uint64(modification), 1|4); code != 0 {
		return syscall.Errno(code)
	}
	return nil
}
