package storage

import (
	"context"
	"errors"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func encodedSource(t *testing.T) (*memfs.FS, string) {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := system.Mkdir(ctx, "/etc", 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(ctx, "/etc/config", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return system, "/etc"
}

func TestCaptureEncodedRoundTripAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	source, note := encodedSource(t)
	envelope, err := CaptureEncoded(ctx, Source{Filesystem: source, Language: "bash", Environment: map[string]string{"A": "1"}, Cursor: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !envelope.HasFSBody() {
		t.Fatal("编码捕获应携带快照本体")
	}

	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.HasFSBody() {
		t.Fatal("解码后应保留快照本体")
	}

	target, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := target.Open(ctx, "/stale", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("stale")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if err := decoded.ResumeEncoded(ctx, target, memfs.DecodeSnapshot); !errors.Is(err, errno.Version) {
		t.Fatalf("初态不匹配应报 EPROTO: %v", err)
	}

	if err := decoded.Resume(ctx, source, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 快照应报 EINVAL: %v", err)
	}

	fresh, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := memfs.DecodeSnapshot(ctx, decoded.FSBody)
	if err != nil {
		t.Fatal(err)
	}

	seed, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Restore(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Resume(ctx, fresh, snapshot); err != nil {
		t.Fatalf("初态一致时应恢复成功: %v", err)
	}
	if _, err := fresh.Stat(ctx, note+"/config"); err != nil {
		t.Fatalf("恢复后应存在 %s/config: %v", note, err)
	}
}

func TestEncodeExcludesInProcessSnapshot(t *testing.T) {
	ctx := context.Background()
	source, _ := encodedSource(t)
	envelope, err := Capture(ctx, Source{Filesystem: source})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.HasFSBody() {
		t.Fatal("Capture 不应产出快照本体（那是 CaptureEncoded 的职责）")
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	target, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.ResumeEncoded(ctx, target, memfs.DecodeSnapshot); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺本体应报 EINVAL: %v", err)
	}
}

func TestCaptureEncodedRejectsUnsupportedBackend(t *testing.T) {
	ctx := context.Background()
	source, _ := encodedSource(t)

	wrapper := &inProcessOnly{inner: source}
	if _, err := CaptureEncoded(ctx, Source{Filesystem: wrapper}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("不可序列化的后端应报 ENOTSUP: %v", err)
	}
}

type inProcessOnly struct{ inner *memfs.FS }

func (w *inProcessOnly) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	return w.inner.Open(ctx, name, flags, mode)
}

func (w *inProcessOnly) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return w.inner.Stat(ctx, name)
}

func (w *inProcessOnly) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return w.inner.Lstat(ctx, name)
}

func (w *inProcessOnly) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	return w.inner.ReadDir(ctx, name)
}

func (w *inProcessOnly) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	return w.inner.Mkdir(ctx, name, mode)
}

func (w *inProcessOnly) Remove(ctx context.Context, name string) error {
	return w.inner.Remove(ctx, name)
}

func (w *inProcessOnly) Rename(ctx context.Context, from, to string) error {
	return w.inner.Rename(ctx, from, to)
}

func (w *inProcessOnly) Link(ctx context.Context, from, to string) error {
	return w.inner.Link(ctx, from, to)
}

func (w *inProcessOnly) Symlink(ctx context.Context, target, link string) error {
	return w.inner.Symlink(ctx, target, link)
}

func (w *inProcessOnly) Readlink(ctx context.Context, name string) (string, error) {
	return w.inner.Readlink(ctx, name)
}

func (w *inProcessOnly) Snapshot(ctx context.Context) (vmfs.Snapshotted, error) {
	snapshotted, err := w.inner.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return opaqueSnapshot{inner: snapshotted}, nil
}

type opaqueSnapshot struct{ inner vmfs.Snapshotted }

func (o opaqueSnapshot) Restore(ctx context.Context, target vmfs.System) error {
	return o.inner.Restore(ctx, target)
}

func TestCaptureEncodedValidation(t *testing.T) {
	ctx := context.Background()
	source, _ := encodedSource(t)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := CaptureEncoded(cancelled, Source{Filesystem: source}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := CaptureEncoded(ctx, Source{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺少文件系统应报 EINVAL: %v", err)
	}

	empty := &emptyBody{inner: source}
	if _, err := CaptureEncoded(ctx, Source{Filesystem: empty}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空本体应报 EINVAL: %v", err)
	}

	if _, err := CaptureEncoded(ctx, Source{Filesystem: unsnapshottable{}}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("不支持快照应报 ENOTSUP: %v", err)
	}

	envelope, err := CaptureEncoded(ctx, Source{Filesystem: source, Language: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.ResumeEncoded(cancelled, target, memfs.DecodeSnapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if err := envelope.ResumeEncoded(ctx, target, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 解码器应报 EINVAL: %v", err)
	}
	var nilEnvelope *Envelope
	if err := nilEnvelope.ResumeEncoded(ctx, target, memfs.DecodeSnapshot); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 信封应报 EINVAL: %v", err)
	}

	broken := &Envelope{Format: EnvelopeFormat, FSBody: []byte("not a snapshot")}
	if err := broken.ResumeEncoded(ctx, target, memfs.DecodeSnapshot); err == nil {
		t.Fatal("损坏本体应导致恢复失败")
	}

	if nilEnvelope.HasFSBody() {
		t.Fatal("nil 信封不应报告有本体")
	}
}

type emptyBody struct{ inner *memfs.FS }

func (e *emptyBody) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	return e.inner.Open(ctx, name, flags, mode)
}
func (e *emptyBody) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return e.inner.Stat(ctx, name)
}
func (e *emptyBody) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return e.inner.Lstat(ctx, name)
}
func (e *emptyBody) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	return e.inner.ReadDir(ctx, name)
}
func (e *emptyBody) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	return e.inner.Mkdir(ctx, name, mode)
}
func (e *emptyBody) Remove(ctx context.Context, name string) error { return e.inner.Remove(ctx, name) }
func (e *emptyBody) Rename(ctx context.Context, from, to string) error {
	return e.inner.Rename(ctx, from, to)
}
func (e *emptyBody) Link(ctx context.Context, from, to string) error {
	return e.inner.Link(ctx, from, to)
}
func (e *emptyBody) Symlink(ctx context.Context, target, link string) error {
	return e.inner.Symlink(ctx, target, link)
}
func (e *emptyBody) Readlink(ctx context.Context, name string) (string, error) {
	return e.inner.Readlink(ctx, name)
}
func (e *emptyBody) Snapshot(context.Context) (vmfs.Snapshotted, error) {
	return emptyEncodable{}, nil
}

type emptyEncodable struct{}

func (emptyEncodable) Restore(context.Context, vmfs.System) error { return nil }
func (emptyEncodable) Encode(context.Context) ([]byte, error)     { return nil, nil }

type unsnapshottable struct{}

func (unsnapshottable) Open(context.Context, string, uint32, stdfs.FileMode) (vmfs.File, error) {
	return nil, errno.NotSupported
}
func (unsnapshottable) Stat(context.Context, string) (stdfs.FileInfo, error) {
	return nil, errno.NotSupported
}
func (unsnapshottable) Lstat(context.Context, string) (stdfs.FileInfo, error) {
	return nil, errno.NotSupported
}
func (unsnapshottable) ReadDir(context.Context, string) ([]stdfs.DirEntry, error) {
	return nil, errno.NotSupported
}
func (unsnapshottable) Mkdir(context.Context, string, stdfs.FileMode) error {
	return errno.NotSupported
}
func (unsnapshottable) Remove(context.Context, string) error          { return errno.NotSupported }
func (unsnapshottable) Rename(context.Context, string, string) error  { return errno.NotSupported }
func (unsnapshottable) Link(context.Context, string, string) error    { return errno.NotSupported }
func (unsnapshottable) Symlink(context.Context, string, string) error { return errno.NotSupported }
func (unsnapshottable) Readlink(context.Context, string) (string, error) {
	return "", errno.NotSupported
}
