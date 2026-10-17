package devfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type trackedStream struct {
	input    bytes.Buffer
	output   bytes.Buffer
	closed   bool
	readErr  error
	writeErr error
}

func (s *trackedStream) Read(buffer []byte) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.input.Read(buffer)
}

func (s *trackedStream) Write(buffer []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.output.Write(buffer)
}

func (s *trackedStream) Close() error {
	s.closed = true
	return nil
}

type fdProvider struct {
	streams map[uint32]*trackedStream
	open    []uint32
	err     error
}

func (p *fdProvider) Descriptor(_ context.Context, id uint32) (io.ReadWriteCloser, error) {
	if p.err != nil {
		return nil, p.err
	}
	stream, ok := p.streams[id]
	if !ok {
		return nil, errno.Wrap(errno.BadHandle, "描述符不存在")
	}
	return stream, nil
}

func (p *fdProvider) OpenDescriptors(context.Context) ([]uint32, error) {
	if p.err != nil {
		return nil, p.err
	}
	return append([]uint32(nil), p.open...), nil
}

func newDevWithFD(t *testing.T, provider *fdProvider) *FS {
	t.Helper()
	fs, err := New(Options{Entropy: &entropy.Counter{}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestStdAliasesResolveToDescriptors(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{
		streams: map[uint32]*trackedStream{
			0: {input: *bytes.NewBufferString("typed")},
			1: {},
			2: {},
		},
		open: []uint32{0, 1, 2},
	}
	fs := newDevWithFD(t, provider)

	handle, err := fs.Open(ctx, "/stdout", vmfs.WriteOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if provider.streams[1].output.String() != "hello" {
		t.Fatalf("stdout 内容=%q", provider.streams[1].output.String())
	}
	if err := handle.Close(); err != nil {
		t.Error(err)
	}

	handle, err = fs.Open(ctx, "/stdin", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if read, err := handle.Read(buffer); err != nil || read != 5 {
		t.Fatalf("读取=%d err=%v", read, err)
	}
	if string(buffer) != "typed" {
		t.Fatalf("stdin 内容=%q", buffer)
	}
	if err := handle.Close(); err != nil {
		t.Error(err)
	}

	handle, err = fs.Open(ctx, "/stderr", vmfs.WriteOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Write([]byte("oops")); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Error(err)
	}
	if provider.streams[2].output.String() != "oops" {
		t.Fatalf("stderr 内容=%q", provider.streams[2].output.String())
	}
}

func TestClosingDevFdDoesNotCloseDescriptor(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{
		streams: map[uint32]*trackedStream{1: {}},
		open:    []uint32{1},
	}
	fs := newDevWithFD(t, provider)
	handle, err := fs.Open(ctx, "/fd/1", vmfs.WriteOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	if provider.streams[1].closed {
		t.Fatal("关闭 /dev/fd/1 句柄不得关闭真实描述符")
	}

	again, err := fs.Open(ctx, "/fd/1", vmfs.WriteOnly, 0)
	if err != nil {
		t.Fatalf("关闭句柄后应仍可打开描述符: %v", err)
	}
	if _, err := again.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := again.Close(); err != nil {
		t.Error(err)
	}
	if provider.streams[1].output.String() != "firstsecond" {
		t.Fatalf("输出=%q", provider.streams[1].output.String())
	}
	if provider.streams[1].closed {
		t.Fatal("真实描述符始终不该被关闭")
	}
}

func TestFDDirectoryListsOpenDescriptors(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{
		streams: map[uint32]*trackedStream{0: {}, 1: {}, 2: {}},
		open:    []uint32{2, 0, 1},
	}
	fs := newDevWithFD(t, provider)
	entries, err := fs.ReadDir(ctx, "/fd")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	if strings.Join(names, ",") != "0,1,2" {
		t.Fatalf("fd 目录=%v", names)
	}

	provider.open = []uint32{5}
	entries, err = fs.ReadDir(ctx, "/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "5" {
		t.Fatalf("fd 目录应跟随描述符集合: %v", names)
	}
}

func TestFDDirectoryRequiresListerCapability(t *testing.T) {
	ctx := context.Background()

	fs, err := New(Options{Entropy: &entropy.Counter{}, Provider: descriptorOnly{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadDir(ctx, "/fd"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("不支持列出应报 ENOTSUP: %v", err)
	}

	plain := newFS(t, nil)
	if _, err := plain.ReadDir(ctx, "/fd"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("无来源应报 ENOTSUP: %v", err)
	}
}

type descriptorOnly struct{}

func (descriptorOnly) Descriptor(context.Context, uint32) (io.ReadWriteCloser, error) {
	return nil, errno.BadHandle
}

func TestMissingDescriptorAndProvider(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{streams: map[uint32]*trackedStream{1: {}}, open: []uint32{1}}
	fs := newDevWithFD(t, provider)

	if _, err := fs.Open(ctx, "/fd/9", vmfs.ReadOnly, 0); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("不存在的描述符应报 EBADF: %v", err)
	}
	if _, err := fs.Stat(ctx, "/fd/9"); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("不存在的描述符 Stat 应报 EBADF: %v", err)
	}

	plain := newFS(t, nil)
	if _, err := plain.Open(ctx, "/fd/1", vmfs.ReadOnly, 0); !errors.Is(err, errno.NoDevice) {
		t.Fatalf("无来源应报 ENXIO: %v", err)
	}
	if _, err := plain.Open(ctx, "/stdout", vmfs.WriteOnly, 0); !errors.Is(err, errno.NoDevice) {
		t.Fatalf("无来源时 std 别名应报 ENXIO: %v", err)
	}

	for _, path := range []string{"/fd/abc", "/fd/", "/fd/99999999999999999999"} {
		if _, err := fs.Stat(ctx, path); err == nil {
			t.Errorf("路径 %q 应被拒绝", path)
		}
	}
}

func TestDescriptorOpenFlagsAreHonoured(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{
		streams: map[uint32]*trackedStream{1: {input: *bytes.NewBufferString("data")}},
		open:    []uint32{1},
	}
	fs := newDevWithFD(t, provider)

	writeOnly, err := fs.Open(ctx, "/fd/1", vmfs.WriteOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeOnly.Read(make([]byte, 4)); !errors.Is(err, errno.Permission) {
		t.Fatalf("只写句柄读取应报 EACCES: %v", err)
	}
	if err := writeOnly.Close(); err != nil {
		t.Error(err)
	}

	readOnly, err := fs.Open(ctx, "/fd/1", vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Write([]byte("x")); !errors.Is(err, errno.Permission) {
		t.Fatalf("只读句柄写入应报 EACCES: %v", err)
	}
	if err := readOnly.Close(); err != nil {
		t.Error(err)
	}

	both, err := fs.Open(ctx, "/fd/1", vmfs.ReadWrite, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := both.Read(make([]byte, 4)); err != nil {
		t.Fatalf("读写句柄读取应成功: %v", err)
	}
	if _, err := both.Write([]byte("ok")); err != nil {
		t.Fatalf("读写句柄写入应成功: %v", err)
	}
	if err := both.Close(); err != nil {
		t.Error(err)
	}
}

func TestDescriptorHandleClosedSemantics(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{streams: map[uint32]*trackedStream{1: {}}, open: []uint32{1}}
	fs := newDevWithFD(t, provider)
	handle, err := fs.Open(ctx, "/fd/1", vmfs.ReadWrite, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Seek(0, 0); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("定位应报 ENOTSUP: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	buffer := make([]byte, 4)
	if _, err := handle.Read(buffer); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后读取应报 ECANCELED: %v", err)
	}
	if _, err := handle.Write(buffer); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后写入应报 ECANCELED: %v", err)
	}
	if _, err := handle.Stat(); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Stat 应报 ECANCELED: %v", err)
	}
}

func TestRootListsFDAndAliases(t *testing.T) {
	ctx := context.Background()
	fs := newFS(t, nil)
	entries, err := fs.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := "fd,null,random,stderr,stdin,stdout,tty,urandom,zero"
	if strings.Join(names, ",") != want {
		t.Fatalf("根目录=%v want %s", names, want)
	}

	info, err := fs.Stat(ctx, "/fd")
	if err != nil || !info.IsDir() {
		t.Fatalf("/fd 应是目录: %+v err=%v", info, err)
	}

	if _, err := fs.Open(ctx, "/fd", vmfs.ReadOnly, 0); !errors.Is(err, errno.IsDirectory) {
		t.Fatalf("打开 /fd 应报 EISDIR: %v", err)
	}

	if _, err := fs.ReadDir(ctx, "/zero"); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("对设备读目录应报 ENOTDIR: %v", err)
	}
}

func TestProviderFaultsArePropagated(t *testing.T) {
	ctx := context.Background()
	provider := &fdProvider{err: errno.Limit}
	fs := newDevWithFD(t, provider)
	if _, err := fs.Open(ctx, "/fd/1", vmfs.ReadOnly, 0); !errors.Is(err, errno.Limit) {
		t.Fatalf("打开应上报来源故障: %v", err)
	}
	if _, err := fs.ReadDir(ctx, "/fd"); !errors.Is(err, errno.Limit) {
		t.Fatalf("列目录应上报来源故障: %v", err)
	}
	if _, err := fs.Stat(ctx, "/fd/1"); !errors.Is(err, errno.Limit) {
		t.Fatalf("Stat 应上报来源故障: %v", err)
	}
}
