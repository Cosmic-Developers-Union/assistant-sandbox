package storage

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func newMemoryFS(t *testing.T, files map[string]string) *memfs.FS {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for path, content := range files {
		for dir := dirname(path); dir != "/" && dir != "."; dir = dirname(dir) {
			if err := system.Mkdir(ctx, dir, 0o755); err != nil && !errors.Is(err, errno.Exist) {
				t.Fatal(err)
			}
		}
		file, err := system.Open(ctx, path, vmfs.Create|vmfs.ReadWrite, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return system
}

func readFile(t *testing.T, system vmfs.System, path string) string {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.ReadOnly, 0)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	var builder strings.Builder
	buffer := make([]byte, 64)
	for {
		n, err := file.Read(buffer)
		builder.Write(buffer[:n])
		if err != nil {
			break
		}
	}
	return builder.String()
}

func TestCaptureEncodeDecodeResumeRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := newMemoryFS(t, map[string]string{"/note.txt": "hello", "/dir/keep": "x"})
	envelope, err := Capture(ctx, Source{
		Filesystem:  source,
		Language:    "",
		Environment: map[string]string{"PATH": "/bin", "HOME": "/"},
		Cursor:      7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Format != EnvelopeFormat {
		t.Fatalf("format=%q", envelope.Format)
	}
	if envelope.LanguageID() != defaultLanguage {
		t.Fatalf("空语言必须按 %s 记录: %q", defaultLanguage, envelope.LanguageID())
	}
	if envelope.Cursor() != 7 || envelope.Digest == "" || envelope.FSSha == "" {
		t.Fatalf("envelope=%+v", envelope)
	}

	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Digest != envelope.Digest || decoded.FSSha != envelope.FSSha {
		t.Fatalf("decoded=%+v", decoded)
	}
	if decoded.LanguageID() != envelope.LanguageID() || decoded.CursorPos != envelope.Cursor() {
		t.Fatalf("decoded=%+v", decoded)
	}
	if decoded.snapshot != nil {
		t.Fatal("解码结果不应携带进程内快照")
	}

	clean := newMemoryFS(t, map[string]string{"/note.txt": "hello", "/dir/keep": "x"})
	if err := envelope.Resume(ctx, clean, envelope.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, clean, "/note.txt"); got != "hello" {
		t.Fatalf("note=%q", got)
	}
	if got := readFile(t, clean, "/dir/keep"); got != "x" {
		t.Fatalf("keep=%q", got)
	}
}

func TestEnvironmentIsCopiedAndReturnedAsCopy(t *testing.T) {
	source := newMemoryFS(t, nil)
	environment := map[string]string{"A": "1"}
	envelope, err := Capture(context.Background(), Source{
		Filesystem: source, Environment: environment,
	})
	if err != nil {
		t.Fatal(err)
	}

	environment["A"] = "changed"
	if got := envelope.EnvironmentCopy()["A"]; got != "1" {
		t.Fatalf("env=%q", got)
	}

	got := envelope.EnvironmentCopy()
	got["A"] = "mutated"
	if envelope.EnvironmentCopy()["A"] != "1" {
		t.Fatal("Environment 必须返回副本")
	}
}

func TestDigestIsStableAndOrderIndependent(t *testing.T) {
	source := newMemoryFS(t, map[string]string{"/a": "1", "/b": "2"})
	first, err := Capture(context.Background(), Source{
		Filesystem: source,
		Environment: map[string]string{
			"B": "2", "A": "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Capture(context.Background(), Source{
		Filesystem: source,
		Environment: map[string]string{
			"A": "1", "B": "2",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest {
		t.Fatalf("摘要应与 map 迭代顺序无关: %s vs %s", first.Digest, second.Digest)
	}

	third, err := Capture(context.Background(), Source{Filesystem: source, Cursor: 1})
	if err != nil {
		t.Fatal(err)
	}
	if third.Digest == first.Digest {
		t.Fatal("游标变化必须改变摘要")
	}
}

func TestDecodeRejectsCorruptionAndVersionMismatch(t *testing.T) {
	source := newMemoryFS(t, map[string]string{"/f": "v"})
	envelope, err := Capture(context.Background(), Source{Filesystem: source, Cursor: 3})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}

	tampered := strings.Replace(string(encoded), `"replay_cursor":3`, `"replay_cursor":9`, 1)
	if tampered == string(encoded) {
		t.Fatalf("未找到可篡改字段: %s", encoded)
	}
	if _, err := Decode([]byte(tampered)); !errors.Is(err, errno.Version) {
		t.Fatalf("篡改负载应报版本/校验失败: %v", err)
	}

	badDigest := strings.Replace(string(encoded), envelope.Digest, strings.Repeat("0", len(envelope.Digest)), 1)
	if _, err := Decode([]byte(badDigest)); !errors.Is(err, errno.Version) {
		t.Fatalf("篡改摘要应失败: %v", err)
	}

	unknown := strings.TrimSuffix(string(encoded), "}") + `,"future":true}`
	if _, err := Decode([]byte(unknown)); err == nil {
		t.Fatal("未知字段必须拒绝")
	}

	for _, bad := range [][]byte{nil, {}, []byte("not json"), []byte(`{"format":"other"}`), []byte(`{"format":"` + EnvelopeFormat + `","digest":""}`)} {
		if _, err := Decode(bad); err == nil {
			t.Fatalf("Decode(%q) 应失败", bad)
		}
	}

	if _, err := Decode([]byte(`{"format":"assistant-sandbox-snapshot-0","digest":"x"}`)); !errors.Is(err, errno.Version) {
		t.Fatalf("旧版本应报 EPROTO: %v", err)
	}
}

func TestResumeRejectsMismatchedFilesystemBeforeRestore(t *testing.T) {
	ctx := context.Background()
	source := newMemoryFS(t, map[string]string{"/keep.txt": "original"})
	envelope, err := Capture(ctx, Source{Filesystem: source})
	if err != nil {
		t.Fatal(err)
	}

	target := newMemoryFS(t, map[string]string{"/keep.txt": "different"})
	if err := envelope.Resume(ctx, target, envelope.Snapshot()); !errors.Is(err, errno.Version) {
		t.Fatalf("初态不一致应报 EPROTO: %v", err)
	}
	if got := readFile(t, target, "/keep.txt"); got != "different" {
		t.Fatalf("拒绝时必须保持目标不变: %q", got)
	}

	clean := newMemoryFS(t, map[string]string{"/keep.txt": "original"})
	if err := envelope.Resume(ctx, clean, envelope.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestResumeValidatesArguments(t *testing.T) {
	ctx := context.Background()
	target := newMemoryFS(t, nil)
	var missing *Envelope
	if err := missing.Resume(ctx, target, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 信封应报 EINVAL: %v", err)
	}
	envelope, err := Capture(ctx, Source{Filesystem: target})
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Resume(ctx, nil, envelope.Snapshot()); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 目标应报 EINVAL: %v", err)
	}
	if err := envelope.Resume(ctx, target, nil); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 快照应报 EINVAL: %v", err)
	}

	stale := &Envelope{Format: "assistant-sandbox-snapshot-0", FSSha: envelope.FSSha}
	if err := stale.Resume(ctx, target, envelope.Snapshot()); !errors.Is(err, errno.Version) {
		t.Fatalf("旧格式应报 EPROTO: %v", err)
	}
}

func TestCaptureRejectsUnsupportedAndCancelledInputs(t *testing.T) {
	if _, err := Capture(context.Background(), Source{}); !errors.Is(err, errno.Invalid) {
		t.Fatalf("缺少文件系统应报 EINVAL: %v", err)
	}

	if _, err := Capture(context.Background(), Source{Filesystem: noSnapshot{}}); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("不支持快照应报 ENOTSUP: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, Source{Filesystem: newMemoryFS(t, nil)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的捕获应报取消: %v", err)
	}
}

func TestEncodeNilAndDecoderAccessors(t *testing.T) {
	var envelope *Envelope
	if _, err := envelope.Encode(); !errors.Is(err, errno.Invalid) {
		t.Fatalf("nil 编码应报 EINVAL: %v", err)
	}
	if envelope.LanguageID() != defaultLanguage || envelope.Cursor() != 0 || envelope.EnvironmentCopy() != nil || envelope.Snapshot() != nil {
		t.Fatal("nil 信封的宽松访问器必须返回值语义")
	}
}

type noSnapshot struct{}

func (noSnapshot) Open(context.Context, string, uint32, fs.FileMode) (vmfs.File, error) {
	return nil, errno.NotSupported
}
func (noSnapshot) Stat(context.Context, string) (fs.FileInfo, error)  { return nil, errno.NotSupported }
func (noSnapshot) Lstat(context.Context, string) (fs.FileInfo, error) { return nil, errno.NotSupported }
func (noSnapshot) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, errno.NotSupported
}
func (noSnapshot) Mkdir(context.Context, string, fs.FileMode) error { return errno.NotSupported }
func (noSnapshot) Remove(context.Context, string) error             { return errno.NotSupported }
func (noSnapshot) Rename(context.Context, string, string) error     { return errno.NotSupported }
func (noSnapshot) Link(context.Context, string, string) error       { return errno.NotSupported }
func (noSnapshot) Symlink(context.Context, string, string) error    { return errno.NotSupported }
func (noSnapshot) Readlink(context.Context, string) (string, error) { return "", errno.NotSupported }

func dirname(path string) string {
	index := strings.LastIndex(path, "/")
	if index <= 0 {
		return "/"
	}
	return path[:index]
}

type failingSnapshot struct{ restored bool }

func (s *failingSnapshot) Restore(context.Context, vmfs.System) error {
	s.restored = true
	return errors.New("回放故障")
}

type failingSnapshotterFS struct{ noSnapshot }

func (failingSnapshotterFS) Snapshot(context.Context) (vmfs.Snapshotted, error) {
	return nil, errors.New("快照故障")
}

func TestCaptureReportsSnapshotFailure(t *testing.T) {
	if _, err := Capture(context.Background(), Source{Filesystem: failingSnapshotterFS{}}); err == nil {
		t.Fatal("快照失败必须中止捕获")
	}
}

func TestResumeReportsRestoreFailure(t *testing.T) {
	ctx := context.Background()
	fs := newMemoryFS(t, nil)
	envelope, err := Capture(ctx, Source{Filesystem: fs})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &failingSnapshot{}
	if err := envelope.Resume(ctx, fs, snapshot); err == nil {
		t.Fatal("回放失败必须上报")
	}
	if !snapshot.restored {
		t.Fatal("初态校验通过后应真正尝试回放")
	}
}

func TestDigestCoversSymlinks(t *testing.T) {
	ctx := context.Background()
	system := newMemoryFS(t, map[string]string{"/real": "content"})
	if err := system.Symlink(ctx, "/real", "/link"); err != nil {
		t.Fatal(err)
	}
	first, err := Capture(ctx, Source{Filesystem: system})
	if err != nil {
		t.Fatal(err)
	}

	if err := system.Remove(ctx, "/link"); err != nil {
		t.Fatal(err)
	}
	if err := system.Symlink(ctx, "/elsewhere", "/link"); err != nil {
		t.Fatal(err)
	}
	second, err := Capture(ctx, Source{Filesystem: system})
	if err != nil {
		t.Fatal(err)
	}
	if first.FSSha == second.FSSha {
		t.Fatal("链接目标变化必须改变文件系统摘要")
	}

	if _, err := Capture(ctx, Source{Filesystem: system}); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeRejectsVersionZero(t *testing.T) {
	if _, err := Decode([]byte(`{"format":"assistant-sandbox-snapshot-0","digest":"abc"}`)); !errors.Is(err, errno.Version) {
		t.Fatalf("旧版本应报 EPROTO: %v", err)
	}
}
