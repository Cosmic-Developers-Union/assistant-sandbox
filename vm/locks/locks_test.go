package locks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestReadSharedWriteExclusive 锁定读写语义:两个持有者的读锁共存;
// 写锁与任意异持有者锁冲突(含共享读锁阻碍他人写升级——POSIX 语义);
// 同持有者可升级/降级。
func TestReadSharedWriteExclusive(t *testing.T) {
	table := New()
	defer table.Close()
	a, b := uint64(2), uint64(3)
	rng := Range{Start: 0}
	if err := table.Set(1, a, rng, Read); err != nil {
		t.Fatal(err)
	}
	if err := table.Set(1, b, rng, Read); err != nil {
		t.Fatalf("读锁应共享: %v", err)
	}
	if err := table.Set(1, b, rng, Write); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("读锁上的写锁: %v want EAGAIN", err)
	}
	// a 的升级被 b 的共享读锁阻碍(POSIX:异持有者写锁冲突)。
	if err := table.Set(1, a, rng, Write); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("他人读锁阻碍升级: %v want EAGAIN", err)
	}
	// b 解锁后 a 可升级为写。
	if err := table.Set(1, b, rng, UnLock); err != nil {
		t.Fatal(err)
	}
	if err := table.Set(1, a, rng, Write); err != nil {
		t.Fatalf("独占升级: %v", err)
	}
	if err := table.Set(1, b, rng, Read); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("写锁上的读锁: %v want EAGAIN", err)
	}
}

// TestSameHolderMergeAndSplit 锁定同进程范围的合并与拆分:
// 相邻/重叠同类型合并;解锁中段拆出前后段。
func TestSameHolderMergeAndSplit(t *testing.T) {
	table := New()
	defer table.Close()
	holder := uint64(5)
	if err := table.Set(7, holder, Range{Start: 0, End: 100}, Write); err != nil {
		t.Fatal(err)
	}
	if err := table.Set(7, holder, Range{Start: 100, End: 200}, Write); err != nil {
		t.Fatal(err)
	}
	// 合并后:同进程再加一段 50-150 仍是一条 [0,200)。
	if err := table.Set(7, holder, Range{Start: 50, End: 150}, Write); err != nil {
		t.Fatal(err)
	}
	// 他人探测:整段冲突。
	if _, _, _, has := table.Test(7, 9, Range{Start: 199, End: 200}, Read); !has {
		t.Fatal("合并段应覆盖 199")
	}
	// 解锁中段 [50,150):拆出 [0,50) 与 [150,200)——两端仍持锁,中段可被他人取得。
	if err := table.Set(7, holder, Range{Start: 50, End: 150}, UnLock); err != nil {
		t.Fatal(err)
	}
	if _, _, _, has := table.Test(7, 9, Range{Start: 0, End: 50}, Read); !has {
		t.Fatal("[0,50) 应仍持锁")
	}
	if _, _, _, has := table.Test(7, 9, Range{Start: 55, End: 60}, Read); has {
		t.Fatal("[50,150) 应已解锁")
	}
	if _, _, _, has := table.Test(7, 9, Range{Start: 150, End: 200}, Read); !has {
		t.Fatal("[150,200) 应仍持锁")
	}
	// EOF 语义:len=0 表示至文件尾。
	if err := table.Set(7, holder, Range{Start: 150}, Write); err != nil {
		t.Fatal(err)
	}
	if _, _, _, has := table.Test(7, 9, Range{Start: 5000}, Read); !has {
		t.Fatal("EOF 段应覆盖 5000 之后")
	}
}

// TestFGetLKReturnsFirstConflict 锁定探测返回冲突锁的持有者与类型。
func TestFGetLKReturnsFirstConflict(t *testing.T) {
	table := New()
	defer table.Close()
	if err := table.Set(9, 2, Range{Start: 10, End: 20}, Write); err != nil {
		t.Fatal(err)
	}
	rng, typ, holder, has := table.Test(9, 3, Range{Start: 15, End: 25}, Read)
	if !has || holder != 2 || typ != Write || rng.Start != 10 {
		t.Fatalf("探测=%v/%d/%d want 冲突/Write/2", has, typ, holder)
	}
	if _, _, _, has := table.Test(9, 2, Range{Start: 15, End: 25}, Read); has {
		t.Fatal("同持有者无冲突")
	}
}

// TestReleaseHolderAndWake 锁定进程退出释放与阻塞等待者唤醒:
// SETLKW 语义下 Set 冲突 → WaitConflict 阻塞,ReleaseHolder 唤醒。
func TestReleaseHolderAndWake(t *testing.T) {
	table := New()
	defer table.Close()
	if err := table.Set(4, 2, Range{Start: 0}, Write); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		// 模拟 F_SETLKW 重试节拍:冲突 → 等待唤醒 → 重试成功。
		for {
			if err := table.Set(4, 3, Range{Start: 0}, Write); err == nil {
				done <- nil
				return
			}
			if err := table.WaitConflict(context.Background()); err != nil {
				done <- err
				return
			}
		}
	}()
	table.ReleaseHolder(2) // 持有者退出
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("唤醒后应取得锁: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReleaseHolder 应唤醒阻塞等待者")
	}
}

// TestClosedTableRejects 锁定关闭语义:关闭后 Set 拒绝。
func TestClosedTableRejects(t *testing.T) {
	table := New()
	table.Close()
	if err := table.Set(1, 1, Range{Start: 0}, Read); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("关闭后 Set: %v want ECANCELED", err)
	}
}
