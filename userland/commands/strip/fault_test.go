package strip

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"testing"

	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var errFaultWrite = errors.New("注入的写入失败")

type faultyFS struct {
	vmfs.System
	writeErr error
}

func (f *faultyFS) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	if f.writeErr != nil && flags&vmfs.WriteOnly != 0 {
		return faultyFile{File: file, err: f.writeErr}, nil
	}
	return file, nil
}

type faultyFile struct {
	vmfs.File
	err error
}

func (f faultyFile) Write(p []byte) (int, error) { return 0, f.err }

func TestWriteFailureInjected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "t.o")
	if err := os.WriteFile(target, []byte("original"), 0o644); err != nil {
		t.Error(err)
	}
	system := &faultyFS{System: platformfs.NewFilesystem(dir), writeErr: errFaultWrite}
	if err := writeViaTemp(t.Context(), system, target, []byte("new")); err == nil {
		t.Fatal("写失败应报错")
	}
	if got := mustRead(t, target); string(got) != "original" {
		t.Fatalf("原文件应保持不变: %q", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) > 0 && entry.Name()[0] == '.' && entry.Name() != ".keep" {
			t.Fatalf("临时文件残留: %s", entry.Name())
		}
	}
}

func TestStickyDiagFailure(t *testing.T) {
	w := &stickyWriter{writer: failWriter{}}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("写失败应返回错误")
	}
	if _, err := w.Write([]byte("y")); err == nil {
		t.Fatal("失败应粘连")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
