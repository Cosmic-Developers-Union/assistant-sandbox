package sandbox

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	abi "github.com/tetratelabs/wazero/experimental/sys"
)

func TestFilesystemCapabilitiesAndQuotas(t *testing.T) {
	dir := t.TempDir()
	f, err := OpenFilesystem(dir, FilesystemConfig{ReadOnly: false, MaxFileBytes: 16, WriteBytes: 24, Operations: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{"", "../escape", "/absolute", "a/../b", "a\\b", "C:drive", "a\x00b"} {
		if _, e := f.OpenFile(name, abi.O_CREAT|abi.O_WRONLY, 0o600); e == 0 {
			t.Fatal(name)
		}
		if _, e := f.Stat(name); e == 0 {
			t.Fatal(name)
		}
		if _, e := f.Lstat(name); e == 0 {
			t.Fatal(name)
		}
		if e := f.Mkdir(name, 0o700); e == 0 {
			t.Fatal(name)
		}
		if e := f.Rename(name, "valid"); e == 0 {
			t.Fatal(name)
		}
		if e := f.Unlink(name); e == 0 {
			t.Fatal(name)
		}
		if e := f.Rmdir(name); e == 0 {
			t.Fatal(name)
		}
	}
	file, e := f.OpenFile("value", abi.O_CREAT|abi.O_RDWR, 0o600)
	if e != 0 {
		t.Fatal(e)
	}
	if n, e := file.Write([]byte("0123456789")); e != 0 || n != 10 {
		t.Fatal(n, e)
	}
	if _, e := file.Write([]byte("overflow")); e != abi.ERANGE {
		t.Fatal(e)
	}
	if _, e := file.Pwrite([]byte("ok"), 0); e != 0 {
		t.Fatal(e)
	}
	if _, e := file.Pwrite([]byte("overflow"), 15); e != abi.ERANGE {
		t.Fatal(e)
	}
	if _, e := file.Pwrite([]byte("x"), -1); e != abi.ERANGE {
		t.Fatal(e)
	}
	if e := file.Truncate(17); e != abi.ERANGE {
		t.Fatal(e)
	}
	if e := file.Truncate(-1); e != abi.EINVAL {
		t.Fatal(e)
	}
	if e := file.Truncate(16); e != 0 {
		t.Fatal(e)
	}
	if _, e := file.Write([]byte("more")); e != 0 {
		t.Fatal(e)
	}
	if _, e := file.Pwrite([]byte("quota"), 0); e != abi.ERANGE {
		t.Fatal(e)
	}
	if st, e := file.Stat(); e != 0 || st.Size != 16 {
		t.Fatal(st, e)
	}
	if _, e := file.Dev(); e != 0 {
		t.Fatal(e)
	}
	if _, e := file.Ino(); e != 0 {
		t.Fatal(e)
	}
	if directory, e := file.IsDir(); e != 0 || directory {
		t.Fatal(directory, e)
	}
	if file.IsAppend() {
		t.Fatal("错误的 append 模式")
	}
	if e := file.Sync(); e != 0 {
		t.Fatal(e)
	}
	if e := file.Datasync(); e != 0 {
		t.Fatal(e)
	}
	if _, e := file.Seek(0, io.SeekStart); e != 0 {
		t.Fatal(e)
	}
	data := make([]byte, 2)
	if n, e := file.Read(data); e != 0 || n != 2 || string(data) != "ok" {
		t.Fatal(string(data), n, e)
	}
	if n, e := file.Pread(data, 0); e != 0 || n != 2 {
		t.Fatal(n, e)
	}
	if e := file.Close(); e != 0 {
		t.Fatal(e)
	}
	if e := file.Close(); e != 0 {
		t.Fatal(e)
	}
	if _, e := file.Stat(); e != abi.EBADF {
		t.Fatal(e)
	}
	if e := f.Mkdir("sub", 0o700); e != 0 {
		t.Fatal(e)
	}
	if e := f.Rmdir("value"); e != abi.ENOTDIR {
		t.Fatal(e)
	}
	if e := f.Unlink("sub"); e != abi.EISDIR {
		t.Fatal(e)
	}
	if _, e := f.OpenFile("value", abi.O_DIRECTORY, 0); e != abi.ENOTDIR {
		t.Fatal(e)
	}
	directory, e := f.OpenFile(".", abi.O_DIRECTORY, 0)
	if e != 0 {
		t.Fatal(e)
	}
	if entries, e := directory.Readdir(10); e != 0 || len(entries) != 2 {
		t.Fatal(entries, e)
	}
	if err := directory.Close(); err != 0 {
		t.Error(err)
	}
	if e := f.Rename("value", "renamed"); e != 0 {
		t.Fatal(e)
	}
	if _, e := f.Stat("renamed"); e != 0 {
		t.Fatal(e)
	}
	if _, e := f.Lstat("renamed"); e != 0 {
		t.Fatal(e)
	}
	readonly, e := f.OpenFile("renamed", abi.O_RDONLY, 0)
	if e != 0 {
		t.Fatal(e)
	}
	if _, e := readonly.Write([]byte("x")); e != abi.EBADF {
		t.Fatal(e)
	}
	if _, e := readonly.Pwrite([]byte("x"), 0); e != abi.EBADF {
		t.Fatal(e)
	}
	if e := readonly.Truncate(0); e != abi.EBADF {
		t.Fatal(e)
	}
	if err := readonly.Close(); err != 0 {
		t.Error(err)
	}
	if e := f.Unlink("renamed"); e != 0 {
		t.Fatal(e)
	}
	if e := f.Rmdir("sub"); e != 0 {
		t.Fatal(e)
	}
	for _, name := range []string{"missing"} {
		if _, e := f.Stat(name); e != abi.ENOENT {
			t.Fatal(e)
		}
		if _, e := f.Lstat(name); e != abi.ENOENT {
			t.Fatal(e)
		}
		if e := f.Unlink(name); e != abi.ENOENT {
			t.Fatal(e)
		}
		if e := f.Rmdir(name); e != abi.ENOENT {
			t.Fatal(e)
		}
	}
}
func TestFilesystemReadOnlyAndHandleBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("content"), 0o600); err != nil {
		t.Error(err)
	}
	ro, err := OpenFilesystem(dir, FilesystemConfig{ReadOnly: true, MaxFileBytes: 16, WriteBytes: 32, Operations: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ro.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, e := ro.OpenFile("file", abi.O_WRONLY, 0); e != abi.EROFS {
		t.Fatal(e)
	}
	for _, e := range []abi.Errno{ro.Mkdir("sub", 0o700), ro.Rename("file", "other"), ro.Unlink("file"), ro.Rmdir("sub")} {
		if e != abi.EROFS {
			t.Fatal(e)
		}
	}
	handles := []abi.File{}
	for range 256 {
		file, e := ro.OpenFile("file", abi.O_RDONLY, 0)
		if e != 0 {
			t.Fatal(e)
		}
		handles = append(handles, file)
	}
	if _, e := ro.OpenFile("file", abi.O_RDONLY, 0); e != abi.EAGAIN {
		t.Fatal(e)
	}
	for _, file := range handles {
		if err := file.Close(); err != 0 {
			t.Error(err)
		}
	}
	if _, err := OpenFilesystem(filepath.Join(dir, "missing"), FilesystemConfig{MaxFileBytes: 1, WriteBytes: 1, Operations: 1}); err == nil {
		t.Fatal("不存在的挂载")
	}
	if errno(fs.ErrPermission) != abi.EACCES || errno(fs.ErrExist) != abi.EEXIST || errno(io.ErrUnexpectedEOF) != abi.EIO {
		t.Fatal("错误映射")
	}
}
