package common

import (
	"bytes"
	"context"
	"errors"
	stdfs "io/fs"
	"math"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func TestProcessFilesAgainstRealFileSystem(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/data", 0o755); err != nil {
		t.Fatal(err)
	}
	create, err := system.Open(ctx, "/data/note.txt", vmfs.Create|1, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create.Write([]byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if err := create.Close(); err != nil {
		t.Fatal(err)
	}
	const pid = 9
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	files := ProcessFiles{System: system, Store: table, PID: pid, Dir: "/data"}
	descriptors := ProcessDescriptors{Table: table, PID: pid}

	id, err := files.Open(ctx, OpenRequest{Path: "note.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	if id < 3 {
		t.Fatalf("描述符编号=%d：不应占用标准流编号", id)
	}
	buffer := make([]byte, 8)
	if loaded, err := descriptors.Read(ctx, id, buffer); err != nil || loaded != 8 {
		t.Fatalf("读=%d err=%v", loaded, err)
	}
	if string(buffer) != "12345678" {
		t.Fatalf("内容=%q", buffer)
	}
	if err := descriptors.Close(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Read(ctx, id, buffer); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后读取=%v want EBADF", err)
	}

	if _, err := files.Open(ctx, OpenRequest{Path: "/data/note.txt", Flags: OpenFlags{Mode: OpenRead}}); err != nil {
		t.Fatal(err)
	}

	if _, err := files.Open(ctx, OpenRequest{Path: "missing", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失路径=%v want ENOENT", err)
	}

	created, err := files.Open(ctx, OpenRequest{Path: "new.txt", Flags: OpenFlags{Mode: OpenReadWrite, Create: true}, Perm: 0o640})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := descriptors.Write(ctx, created, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := descriptors.Close(ctx, created); err != nil {
		t.Fatal(err)
	}
	reopened, err := files.Open(ctx, OpenRequest{Path: "new.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := descriptors.Read(ctx, reopened, buffer[:2])
	if err != nil || loaded != 2 || string(buffer[:2]) != "hi" {
		t.Fatalf("重开读=%d %q err=%v", loaded, buffer[:2], err)
	}

	if _, err := files.Open(ctx, OpenRequest{Path: "new.txt", Flags: OpenFlags{Mode: OpenWrite, Create: true, Exclusive: true}}); !errors.Is(err, errno.Exist) {
		t.Fatalf("O_EXCL 已存在=%v want EEXIST", err)
	}

	flagged, err := files.Open(ctx, OpenRequest{Path: "note.txt", Flags: OpenFlags{Mode: OpenRead, CloseOnExec: true}})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := table.CloseCloexec(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || uint64(closed[0]) != flagged {
		t.Fatalf("cloexec 回收=%v flagged=%d", closed, flagged)
	}
}

func TestProcessFilesRejectsUnwired(t *testing.T) {
	ctx := context.Background()
	if _, err := (ProcessFiles{}).Open(ctx, OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("err=%v want ENOSYS", err)
	}
	if _, err := (ProcessFiles{Store: fd.New()}).Open(ctx, OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("err=%v want ENOSYS", err)
	}

	table := fd.New()
	files := ProcessFiles{System: &openingSystem{file: &recordingFile{}}, Store: table, PID: 404}
	if _, err := files.Open(ctx, OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("err=%v want ESRCH", err)
	}
}

func TestProcessFilesClosesFileWhenInstallFails(t *testing.T) {
	file := &recordingFile{}
	files := ProcessFiles{System: &openingSystem{file: file}, Store: failingStore{err: errno.Limit}, PID: 1}
	if _, err := files.Open(context.Background(), OpenRequest{Path: "/x", Flags: OpenFlags{Mode: OpenRead}}); !errors.Is(err, errno.Limit) {
		t.Fatalf("err=%v want EAGAIN", err)
	}
	if !file.closed {
		t.Fatal("安装失败后文件没有被关闭")
	}
}

func TestProcessFilesClosesFileWhenCloexecFails(t *testing.T) {
	file := &recordingFile{}
	store := &cloexecFailStore{err: errno.Range}
	files := ProcessFiles{System: &openingSystem{file: file}, Store: store, PID: 1}
	_, err := files.Open(context.Background(), OpenRequest{
		Path: "/x", Flags: OpenFlags{Mode: OpenRead, CloseOnExec: true},
	})
	if !errors.Is(err, errno.Range) {
		t.Fatalf("err=%v want ERANGE", err)
	}

	if !store.closed || store.id != 3 {
		t.Fatalf("回滚=%v id=%d", store.closed, store.id)
	}
}

type openingSystem struct {
	file vmfs.File
	err  error
}

func (s *openingSystem) Open(context.Context, string, uint32, stdfs.FileMode) (vmfs.File, error) {
	return s.file, s.err
}

func (s *openingSystem) Stat(context.Context, string) (stdfs.FileInfo, error) {
	return nil, errno.NotImplemented
}

func (s *openingSystem) Lstat(context.Context, string) (stdfs.FileInfo, error) {
	return nil, errno.NotImplemented
}

func (s *openingSystem) ReadDir(context.Context, string) ([]stdfs.DirEntry, error) {
	return nil, errno.NotImplemented
}

func (s *openingSystem) Mkdir(context.Context, string, stdfs.FileMode) error {
	return errno.NotImplemented
}

func (s *openingSystem) Remove(context.Context, string) error { return errno.NotImplemented }

func (s *openingSystem) Rename(context.Context, string, string) error { return errno.NotImplemented }

func (s *openingSystem) Link(context.Context, string, string) error { return errno.NotImplemented }

func (s *openingSystem) Symlink(context.Context, string, string) error { return errno.NotImplemented }

func (s *openingSystem) Readlink(context.Context, string) (string, error) {
	return "", errno.NotImplemented
}

type recordingFile struct {
	buffer bytes.Buffer
	closed bool
}

func (f *recordingFile) Read(buffer []byte) (int, error)  { return f.buffer.Read(buffer) }
func (f *recordingFile) Write(buffer []byte) (int, error) { return f.buffer.Write(buffer) }
func (f *recordingFile) Seek(int64, int) (int64, error)   { return 0, errno.Invalid }
func (f *recordingFile) Stat() (stdfs.FileInfo, error)    { return nil, errno.Invalid }
func (f *recordingFile) Close() error {
	f.closed = true
	return nil
}

type failingStore struct{ err error }

func (s failingStore) InstallNext(uint64, fd.Stream) (uint32, error) { return 0, s.err }
func (s failingStore) SetCloexec(uint64, uint32, bool) error         { return nil }
func (s failingStore) Close(context.Context, uint64, uint32) error   { return nil }

type cloexecFailStore struct {
	err    error
	closed bool
	id     uint32
}

func (s *cloexecFailStore) InstallNext(uint64, fd.Stream) (uint32, error) { return 3, nil }

func (s *cloexecFailStore) SetCloexec(uint64, uint32, bool) error { return s.err }

func (s *cloexecFailStore) Close(_ context.Context, _ uint64, id uint32) error {
	s.closed = true
	s.id = id
	return nil
}

func TestProcessDescriptorsSeek(t *testing.T) {
	ctx := context.Background()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(ctx, "/seek.txt", vmfs.Create|1, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const pid = 5
	table := fd.New()
	table.Attach(pid, fd.Streams{Input: &bufferStream{input: []byte("stdin")}})
	files := ProcessFiles{System: system, Store: table, PID: pid}
	id, err := files.Open(ctx, OpenRequest{Path: "/seek.txt", Flags: OpenFlags{Mode: OpenRead}})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	if offset, err := descriptors.Seek(ctx, id, 2, SeekSet); err != nil || offset != 2 {
		t.Fatalf("SEEK_SET: offset=%d err=%v", offset, err)
	}
	buffer := make([]byte, 2)
	loaded, err := descriptors.Read(ctx, id, buffer)
	if err != nil || loaded != 2 || string(buffer) != "cd" {
		t.Fatalf("定位后读=%d %q err=%v", loaded, buffer, err)
	}
	if offset, err := descriptors.Seek(ctx, id, -1, SeekEnd); err != nil || offset != 5 {
		t.Fatalf("SEEK_END: offset=%d err=%v", offset, err)
	}

	if _, err := descriptors.Seek(ctx, 0, 0, SeekSet); !errors.Is(err, errno.Seek) {
		t.Fatalf("标准流定位=%v want ESPIPE", err)
	}
}

func TestProcessDescriptorsRejectsOversizedAndUnknown(t *testing.T) {
	ctx := context.Background()
	const pid = 7
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	descriptors := ProcessDescriptors{Table: table, PID: pid}
	if err := descriptors.Close(ctx, math.MaxUint32+1); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭越界编号=%v want EBADF", err)
	}
	if err := descriptors.Close(ctx, 99); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭未打开的描述符=%v want EBADF", err)
	}
	if err := descriptors.Close(ctx, 0); err != nil {
		t.Fatalf("关闭标准流=%v", err)
	}
	if _, err := descriptors.Seek(ctx, 99, 0, SeekSet); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("定位未打开的描述符=%v want EBADF", err)
	}

	other := ProcessDescriptors{Table: table, PID: pid + 1}
	if _, err := other.Seek(ctx, 0, 0, SeekSet); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未挂载的进程=%v want ESRCH", err)
	}
	if err := (ProcessDescriptors{PID: pid}).Close(ctx, 0); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("空表关闭=%v want ENOSYS", err)
	}
}
