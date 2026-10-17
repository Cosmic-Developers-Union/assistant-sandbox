package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestSessionLifecycle(t *testing.T) {
	m := New(0)
	ctx := context.Background()
	if err := m.Save(ctx, "session-1", []byte("state")); err != nil {
		t.Fatal(err)
	}
	data, err := m.Load(ctx, "session-1")
	if err != nil || string(data) != "state" {
		t.Fatalf("data=%q err=%v", data, err)
	}

	data[0] = 'X'
	again, _ := m.Load(ctx, "session-1")
	if string(again) != "state" {
		t.Fatal("会话数据被外部修改")
	}

	if err := m.Save(ctx, "session-1", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if data, _ = m.Load(ctx, "session-1"); string(data) != "updated" {
		t.Fatal(data)
	}
	if err := m.Delete(ctx, "session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(ctx, "session-1"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "session-1"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	if names := m.SessionNames(); len(names) != 0 {
		t.Fatal(names)
	}
}

func TestSessionCapacity(t *testing.T) {
	m := New(8)
	if err := m.Save(context.Background(), "a", []byte("12345678")); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), "b", []byte("x")); !errors.Is(err, errno.NoSpace) {
		t.Fatal(err)
	}

	if err := m.Save(context.Background(), "a", []byte("12")); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), "b", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(context.Background(), "", nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestSnapshotsAreContentAddressedAndImmutable(t *testing.T) {
	m := New(0)
	ctx := context.Background()
	digest, err := m.Put(ctx, []byte("snapshot-v1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("digest=%q", digest)
	}

	same, err := m.Put(ctx, []byte("snapshot-v1"))
	if err != nil || same != digest {
		t.Fatalf("digest=%q err=%v", same, err)
	}

	data, err := m.Get(ctx, digest)
	if err != nil || string(data) != "snapshot-v1" {
		t.Fatal(data, err)
	}
	data[0] = 'X'
	if again, _ := m.Get(ctx, digest); string(again) != "snapshot-v1" {
		t.Fatal("快照内容可被篡改")
	}

	other, _ := m.Put(ctx, []byte("snapshot-v2"))
	if other == digest {
		t.Fatal("不同内容不应共享摘要")
	}
	if _, err := m.Get(ctx, "sha256:deadbeef"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, ""); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestNilContentIsStableEmptyDigest(t *testing.T) {
	m := New(0)
	nilDigest, err := m.Put(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest, err := m.Put(context.Background(), []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if nilDigest != emptyDigest {
		t.Fatalf("%q != %q", nilDigest, emptyDigest)
	}
}

func TestCancelRefusesStorage(t *testing.T) {
	m := New(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Save(ctx, "x", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := m.Load(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := m.Get(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := m.Put(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEmptyNamesAndUnknownDigestRejected(t *testing.T) {
	m := New(0)
	ctx := context.Background()
	if _, err := m.Load(ctx, ""); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空名称读取应报 EINVAL: %v", err)
	}
	if _, err := m.Get(ctx, ""); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空摘要应报 EINVAL: %v", err)
	}
	if _, err := m.Get(ctx, "sha256:missing"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未知摘要应报 ENOENT: %v", err)
	}
}

func TestSnapshotBudgetRejectsOverspend(t *testing.T) {
	m := New(4)
	ctx := context.Background()
	digest, err := m.Put(ctx, []byte("abcd"))
	if err != nil {
		t.Fatal(err)
	}

	if again, err := m.Put(ctx, []byte("abcd")); err != nil || again != digest {
		t.Fatalf("again=%q err=%v", again, err)
	}
	if _, err := m.Put(ctx, []byte("e")); !errors.Is(err, errno.NoSpace) {
		t.Fatalf("超预算应报 ENOSPC: %v", err)
	}
}
