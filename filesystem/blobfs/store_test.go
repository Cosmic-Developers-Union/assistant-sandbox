package blobfs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func (m *Memory) overwriteForTest(identity string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[identity] = data
}

func readAll(t *testing.T, store Store, identity string) string {
	t.Helper()
	reader, err := store.Open(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPutAndOpenRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(0)
	identity, err := store.Put(ctx, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if identity != digest.Content([]byte("hello")) || !strings.HasPrefix(identity, "sha256:") {
		t.Fatalf("身份=%q", identity)
	}
	if got := readAll(t, store, identity); got != "hello" {
		t.Fatalf("内容=%q", got)
	}
	if ok, err := store.Has(ctx, identity); err != nil || !ok {
		t.Fatalf("Has=%v err=%v", ok, err)
	}
	account, err := store.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if account.Blobs != 1 || account.Bytes != 5 || account.Limit != 0 {
		t.Fatalf("账户=%+v", account)
	}
}

func TestSameContentIsStoredOnce(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(0)
	first, err := store.Put(ctx, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, []byte("same"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("身份不同: %q %q", first, second)
	}
	account, err := store.Account(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if account.Blobs != 1 || account.Bytes != 4 {
		t.Fatalf("重复写入占用了额外预算: %+v", account)
	}
}

func TestContentIsCopied(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(0)
	source := []byte("payload")
	identity, err := store.Put(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'X'
	if got := readAll(t, store, identity); got != "payload" {
		t.Fatalf("写入后改写源缓冲区影响了存储: %q", got)
	}
	reader, err := store.Open(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 7)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Error(err)
	}
	buffer[0] = 'Y'
	if got := readAll(t, store, identity); got != "payload" {
		t.Fatalf("改写读结果影响了存储: %q", got)
	}
}

func TestBudgetIsEnforced(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(4)
	if _, err := store.Put(ctx, []byte("12345")); !errors.Is(err, errno.Limit) {
		t.Fatalf("超限写入=%v want EAGAIN", err)
	}
	if account, err := store.Account(ctx); err != nil || account.Bytes != 0 || account.Blobs != 0 {
		t.Fatalf("被拒绝的写入占了额度: %+v err=%v", account, err)
	}
	if _, err := store.Put(ctx, []byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, []byte("5")); !errors.Is(err, errno.Limit) {
		t.Fatalf("额度用尽后写入=%v want EAGAIN", err)
	}

	if _, err := store.Put(ctx, []byte("1234")); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejections(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(0)
	if _, err := store.Open(ctx, "sha256:deadbeef"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失内容=%v want ENOENT", err)
	}
	if _, err := store.Open(ctx, ""); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空身份=%v want EINVAL", err)
	}
	identity, err := store.Put(ctx, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}

	store.overwriteForTest(identity, []byte("payloa"))
	if _, err := store.Open(ctx, identity); !errors.Is(err, errno.Version) {
		t.Fatalf("损坏内容=%v want EPROTO", err)
	}

	if ok, err := store.Has(ctx, identity); err != nil || !ok {
		t.Fatalf("Has=%v err=%v", ok, err)
	}
}

func TestCancellation(t *testing.T) {
	store := NewMemory(0)
	identity, err := store.Put(context.Background(), []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Put(cancelled, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put=%v", err)
	}
	if _, err := store.Open(cancelled, identity); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open=%v", err)
	}
	if _, err := store.Has(cancelled, identity); !errors.Is(err, context.Canceled) {
		t.Fatalf("Has=%v", err)
	}
	if _, err := store.Account(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Account=%v", err)
	}
}

func TestEmptyContent(t *testing.T) {
	ctx := context.Background()
	store := NewMemory(0)
	first, err := store.Put(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != digest.Content(nil) {
		t.Fatalf("空内容身份=%q %q", first, second)
	}
	if got := readAll(t, store, first); got != "" {
		t.Fatalf("空内容=%q", got)
	}
}
