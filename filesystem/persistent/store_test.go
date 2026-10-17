package persistent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.Save(ctx, "session-a", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	data, err := store.Load(ctx, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("内容=%q", data)
	}

	if err := store.Save(ctx, "session-a", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	data, err = store.Load(ctx, "session-a")
	if err != nil || string(data) != "updated" {
		t.Fatalf("覆盖后=%q err=%v", data, err)
	}

	if _, err := store.Load(ctx, "missing"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不存在应报 ENOENT: %v", err)
	}

	if err := store.Delete(ctx, "session-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "session-a"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("删除后应报 ENOENT: %v", err)
	}
	if err := store.Delete(ctx, "session-a"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("重复删除应报 ENOENT: %v", err)
	}
}

func TestSaveCommitsAtomically(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.Save(ctx, "s", []byte("first")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			t.Fatalf("提交后不得残留临时文件: %s", entry.Name())
		}
	}

	if _, err := os.Stat(filepath.Join(store.Root(), "s"+sessionSuffix)); err != nil {
		t.Fatalf("目标文件应存在: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), "s"+sessionSuffix+checksumSuffix)); err != nil {
		t.Fatalf("摘要文件应存在: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.Root(), "s"+sessionSuffix))
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := os.ReadFile(filepath.Join(store.Root(), "s"+sessionSuffix+checksumSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if digestOf(data) != strings.TrimSpace(string(checksum)) {
		t.Fatal("内容与摘要不符")
	}
}

func TestLoadRejectsCorruptedContent(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.Save(ctx, "s", []byte("original")); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(store.Root(), "s"+sessionSuffix)
	if err := os.WriteFile(target, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "s"); !errors.Is(err, errno.Version) {
		t.Fatalf("内容被篡改应报校验失败: %v", err)
	}

	if err := os.Remove(filepath.Join(store.Root(), "s"+sessionSuffix+checksumSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "s"); !errors.Is(err, errno.Version) {
		t.Fatalf("摘要缺失应报校验失败: %v", err)
	}
}

func TestLoadRejectsLeftoverTempFile(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if err := store.Save(ctx, "s", []byte("data")); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(store.Root(), tempPrefix+"s.session-123456")
	if err := os.WriteFile(leftover, []byte("half written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "s"); !errors.Is(err, errno.Version) {
		t.Fatalf("残留应报校验失败: %v", err)
	}

	if err := os.Remove(leftover); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, "s"); err != nil {
		t.Fatalf("清理后应可读: %v", err)
	}
}

func TestSessionNamesAreValidated(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	for _, name := range []string{
		"", ".", "..", "a/b", "a\\b", "../escape", " lead", "trail ",
	} {
		if err := store.Save(ctx, name, []byte("x")); !errors.Is(err, errno.Invalid) {
			t.Errorf("会话名 %q 应被拒绝: %v", name, err)
		}
		if _, err := store.Load(ctx, name); !errors.Is(err, errno.Invalid) {
			t.Errorf("会话名 %q 读取应被拒绝: %v", name, err)
		}
		if err := store.Delete(ctx, name); !errors.Is(err, errno.Invalid) {
			t.Errorf("会话名 %q 删除应被拒绝: %v", name, err)
		}
	}

	parent := filepath.Dir(store.Root())
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "escape"+sessionSuffix) {
			t.Fatalf("会话名逃出了根目录: %s", entry.Name())
		}
	}
}

func TestListReportsOnlyCompleteSessions(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	for _, name := range []string{"b", "a", "c"} {
		if err := store.Save(ctx, name, []byte(name)); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.Remove(filepath.Join(store.Root(), "c"+sessionSuffix+checksumSuffix)); err != nil {
		t.Fatal(err)
	}
	names, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("列表=%v want %v", names, want)
	}
}

func TestSnapshotsAreContentAddressedAndImmutable(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	first, err := store.Put(ctx, []byte("snapshot-body"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, digestPrefix) || len(first) != len(digestPrefix)+64 {
		t.Fatalf("标识格式=%q", first)
	}

	second, err := store.Put(ctx, []byte("snapshot-body"))
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("同一内容应得到同一标识: %q vs %q", first, second)
	}

	other, err := store.Put(ctx, []byte("different"))
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("不同内容不得同标识")
	}

	data, err := store.Get(ctx, first)
	if err != nil || string(data) != "snapshot-body" {
		t.Fatalf("读取=%q err=%v", data, err)
	}

	missing := digestPrefix + strings.Repeat("0", 64)
	if _, err := store.Get(ctx, missing); !errors.Is(err, errno.NotFound) {
		t.Fatalf("不存在应报 ENOENT: %v", err)
	}

	for _, bad := range []string{"", "deadbeef", digestPrefix + "short", digestPrefix + strings.Repeat("z", 64), digestPrefix + "../../escape"} {
		if _, err := store.Get(ctx, bad); !errors.Is(err, errno.Invalid) {
			t.Errorf("非法标识 %q 应被拒绝: %v", bad, err)
		}
	}
}

func TestGetRejectsTamperedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	digest, err := store.Put(ctx, []byte("original"))
	if err != nil {
		t.Fatal(err)
	}

	target := store.snapshotPath(digest)
	if err := os.WriteFile(target, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, digest); !errors.Is(err, errno.Version) {
		t.Fatalf("被改动的快照应报校验失败: %v", err)
	}
}

func TestCancellationIsHonoured(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	store := newStore(t)
	if err := store.Save(cancelled, "s", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save 取消应报取消: %v", err)
	}
	if _, err := store.Load(cancelled, "s"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load 取消应报取消: %v", err)
	}
	if err := store.Delete(cancelled, "s"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete 取消应报取消: %v", err)
	}
	if _, err := store.List(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("List 取消应报取消: %v", err)
	}
	if _, err := store.Put(cancelled, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put 取消应报取消: %v", err)
	}
	if _, err := store.Get(cancelled, digestPrefix+strings.Repeat("0", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get 取消应报取消: %v", err)
	}
}

func TestNewRejectsEmptyRoot(t *testing.T) {
	if _, err := New(""); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空根目录应报 EINVAL: %v", err)
	}

	root := filepath.Join(t.TempDir(), "deep", "nested")
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if store.Root() != root {
		t.Fatalf("root=%q want %q", store.Root(), root)
	}

	if _, err := os.Stat(filepath.Join(root, snapshotDir)); err != nil {
		t.Fatalf("快照目录应存在: %v", err)
	}
}

func TestSaveIsDurableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "durable", []byte("persisted")); err != nil {
		t.Fatal(err)
	}
	digest, err := store.Put(ctx, []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := reopened.Load(ctx, "durable")
	if err != nil || string(data) != "persisted" {
		t.Fatalf("重开后读取=%q err=%v", data, err)
	}
	snapshot, err := reopened.Get(ctx, digest)
	if err != nil || string(snapshot) != "snapshot" {
		t.Fatalf("重开后快照=%q err=%v", snapshot, err)
	}
	names, err := reopened.List(ctx)
	if err != nil || len(names) != 1 || names[0] != "durable" {
		t.Fatalf("重开后列表=%v err=%v", names, err)
	}
}
