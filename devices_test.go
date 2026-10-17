package sandbox

import (
	"io/fs"
	"testing"

	abi "github.com/tetratelabs/wazero/experimental/sys"
)

func TestVirtualNullDevice(t *testing.T) {
	d := devices{}
	for _, name := range []string{".", "", "/", "null", "missing", "../null"} {
		_, errno := d.Stat(name)
		_, other := d.Lstat(name)
		if errno != other {
			t.Fatal(name)
		}
		if name == "missing" || name == "../null" {
			if errno != abi.ENOENT {
				t.Fatal(name, errno)
			}
		} else if errno != 0 {
			t.Fatal(name, errno)
		}
	}
	if _, errno := d.OpenFile("missing", abi.O_RDONLY, 0); errno != abi.ENOENT {
		t.Fatal(errno)
	}
	if _, errno := d.OpenFile(".", abi.O_WRONLY, 0); errno != abi.EISDIR {
		t.Fatal(errno)
	}
	dir, _ := d.OpenFile(".", abi.O_RDONLY, 0)
	if yes, errno := dir.IsDir(); !yes || errno != 0 {
		t.Fatal(yes, errno)
	}
	if _, errno := dir.Read(nil); errno != abi.EISDIR {
		t.Fatal(errno)
	}
	if err := dir.Close(); err != 0 {
		t.Error(err)
	}
	f, errno := d.OpenFile("null", abi.O_RDWR|abi.O_TRUNC, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	if stat, errno := f.Stat(); errno != 0 || stat.Mode&fs.ModeCharDevice == 0 {
		t.Fatal(stat, errno)
	}
	if yes, errno := f.IsDir(); yes || errno != 0 {
		t.Fatal(yes, errno)
	}
	if n, errno := f.Write([]byte("discard")); errno != 0 || n != 7 {
		t.Fatal(n, errno)
	}
	if n, errno := f.Pwrite([]byte("discard"), 100); errno != 0 || n != 7 {
		t.Fatal(n, errno)
	}
	if n, errno := f.Read(make([]byte, 10)); errno != 0 || n != 0 {
		t.Fatal(n, errno)
	}
	if n, errno := f.Pread(make([]byte, 10), 100); errno != 0 || n != 0 {
		t.Fatal(n, errno)
	}
	if n, errno := f.Seek(100, 0); errno != abi.ENOSYS || n != 0 {
		t.Fatal(n, errno)
	}
	if errno := f.Truncate(0); errno != 0 {
		t.Fatal(errno)
	}
	if err := f.Close(); err != 0 {
		t.Error(err)
	}
	if err := f.Close(); err != 0 {
		t.Error(err)
	}
	if _, errno := f.Stat(); errno != abi.EBADF {
		t.Fatal(errno)
	}
	if _, errno := f.Read(nil); errno != abi.EBADF {
		t.Fatal(errno)
	}
	if _, errno := f.Write(nil); errno != abi.EBADF {
		t.Fatal(errno)
	}
	if _, errno := f.Seek(0, 0); errno != abi.ENOSYS {
		t.Fatal(errno)
	}
	for _, flags := range []abi.Oflag{abi.O_RDONLY, abi.O_WRONLY} {
		f, _ := d.OpenFile("null", flags, 0)
		if flags == abi.O_RDONLY {
			if _, errno := f.Write(nil); errno != abi.EBADF {
				t.Fatal(errno)
			}
		} else {
			if _, errno := f.Read(nil); errno != abi.EBADF {
				t.Fatal(errno)
			}
		}
		if err := f.Close(); err != 0 {
			t.Error(err)
		}
	}
}
