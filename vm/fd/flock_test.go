package fd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestFlockConflictAndUnlock(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	if err := table.Install(1, 3, &stream{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(1, 4, &stream{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 3, flockShared); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 3, flockExclusive); err != nil {
		t.Fatalf("同打开文件描述升级锁: %v", err)
	}
	if err := table.Flock(t.Context(), 1, 4, flockShared|flockNoBlock); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("冲突锁=%v want EWOULDBLOCK", err)
	}
	if err := table.Flock(t.Context(), 1, 3, flockUnlock); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 4, flockShared); err != nil {
		t.Fatalf("解锁后加锁: %v", err)
	}
}

func TestFlockBlockingWaitHonorsContext(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	if err := table.Install(1, 3, &stream{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(1, 4, &stream{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 3, flockExclusive); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := table.Flock(ctx, 1, 4, flockExclusive); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("阻塞锁上下文=%v want deadline", err)
	}
}

func TestFlockLivesWithOpenFileDescription(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	if err := table.Install(1, 3, &stream{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(1, 4, &stream{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Dup(t.Context(), 1, 3, 5); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 3, flockExclusive); err != nil {
		t.Fatal(err)
	}
	if err := table.Close(t.Context(), 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 4, flockShared|flockNoBlock); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("dup 仍持有锁时不应释放: %v", err)
	}
	if err := table.Close(t.Context(), 1, 5); err != nil {
		t.Fatal(err)
	}
	if err := table.Flock(t.Context(), 1, 4, flockShared); err != nil {
		t.Fatalf("最后一个打开文件描述关闭后应释放锁: %v", err)
	}
}
