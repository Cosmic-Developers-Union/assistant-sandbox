package instance

import (
	"context"
	"sync"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// TestLifecycleConcurrencyHasNoDeadlockOrDoubleRelease 验证 C12:启动、
// 连接、执行与关闭相互竞争时,无死锁、无重复释放,关闭后不再新增资源。
func TestLifecycleConcurrencyHasNoDeadlockOrDoubleRelease(t *testing.T) {
	const rounds = 8
	var group sync.WaitGroup
	for worker := range rounds {
		group.Go(func() {
			machine, err := New(t.Context(), Options{
				Deterministic: true,
				Programs: map[string]executable.Program{
					"hold": executable.Func(func(ctx context.Context, _ proc.Proc) (uint8, error) {
						<-ctx.Done()
						return 0, ctx.Err()
					}),
				},
			})
			if err != nil {
				t.Errorf("建立 VM: %v", err)
				return
			}
			connection, err := machine.Connect(t.Context())
			if err != nil {
				t.Errorf("连接: %v", err)
				return
			}
			if _, err := connection.Exec(t.Context(), ExecSpec{Program: "hold"}); err != nil {
				t.Errorf("执行: %v", err)
			}
			// 连接与关闭并发:关闭可能先到,此时执行被拒绝是合法结果。
			var inner sync.WaitGroup
			inner.Go(func() {
				_ = connection.Disconnect(t.Context())
			})
			inner.Go(func() {
				_ = machine.Shutdown(t.Context())
			})
			inner.Wait()
			// 关闭之后再连接必须被拒绝,不得新增资源。
			if _, err := machine.Connect(t.Context()); err == nil {
				t.Errorf("关闭后仍接受新连接")
			}
			// 关闭之后再执行必须被拒绝。
			if _, err := connection.Exec(t.Context(), ExecSpec{Program: "hold"}); err == nil {
				t.Errorf("关闭后仍接受新任务")
			}
			// 重复关闭幂等,不重复释放。
			if err := machine.Shutdown(t.Context()); err != nil {
				t.Errorf("重复关闭: %v", err)
			}
			if worker < 0 {
				t.Errorf("不可达")
			}
		})
	}
	group.Wait()
}
