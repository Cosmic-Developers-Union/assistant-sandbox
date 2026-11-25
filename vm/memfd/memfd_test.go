package memfd

import (
	"io"
	"testing"
)

func TestFileReadWriteSeekAndTruncate(t *testing.T) {
	file := New("Hi")
	if _, err := file.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	var buffer [3]byte
	if n, err := io.ReadFull(file, buffer[:]); err != nil || n != 3 || string(buffer[:]) != "abc" {
		t.Fatalf("read=%q n=%d err=%v", buffer, n, err)
	}
	if _, err := file.WriteAt([]byte("Z"), 1); err != nil {
		t.Fatal(err)
	}
	if n, err := file.ReadAt(buffer[:1], 1); err != nil || n != 1 || buffer[0] != 'Z' {
		t.Fatalf("ReadAt=%q n=%d err=%v", buffer[:1], n, err)
	}
	if err := file.Truncate(5); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 5 {
		t.Fatalf("Stat=%+v err=%v", info, err)
	}
}

func TestStatusFlagsAndAppend(t *testing.T) {
	file := New("Hi")
	if _, err := file.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := file.SetStatusFlags(0o2000); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	var content [2]byte
	if n, err := file.ReadAt(content[:], 0); err != nil || n != len(content) {
		t.Fatalf("ReadAt n=%d err=%v", n, err)
	}
	if string(content[:]) != "ab" {
		t.Fatalf("append content=%q", content)
	}
	if flags, err := file.StatusFlags(); err != nil || flags&0o2000 == 0 {
		t.Fatalf("StatusFlags=%#x err=%v", flags, err)
	}
}
