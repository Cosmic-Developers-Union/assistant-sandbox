package fd

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestCloseCloexecKeepsInheritedDescriptors(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	inherited := &counted{}
	cloexec := &counted{}
	inheritedFD, err := table.InstallNext(1, inherited)
	if err != nil {
		t.Fatal(err)
	}
	cloexecFD, err := table.InstallNext(1, cloexec)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.SetCloexec(1, cloexecFD, true); err != nil {
		t.Fatal(err)
	}
	closed, err := table.CloseCloexec(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closed, []uint32{cloexecFD}) {
		t.Fatalf("closed=%v want [%d]", closed, cloexecFD)
	}
	if closings := cloexec.closes.Load(); closings != 1 {
		t.Fatalf("被关闭对象必须释放: %d", closings)
	}
	if closings := inherited.closes.Load(); closings != 0 {
		t.Fatalf("保留对象不得释放: %d", closings)
	}
	opened := table.Opened(1)
	for _, id := range []uint32{0, 1, 2, inheritedFD} {
		if !slices.Contains(opened, id) {
			t.Fatalf("描述符 %d 必须保留: %v", id, opened)
		}
	}
	if slices.Contains(opened, cloexecFD) {
		t.Fatalf("描述符 %d 必须关闭: %v", cloexecFD, opened)
	}

	again, err := table.CloseCloexec(t.Context(), 1)
	if err != nil || len(again) != 0 {
		t.Fatalf("again=%v err=%v", again, err)
	}
}

func TestCloseCloexecFailures(t *testing.T) {
	table := New()
	table.Attach(1, Streams{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := table.CloseCloexec(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := table.CloseCloexec(t.Context(), 9); !errors.Is(err, errno.NoProcess) {
		t.Fatalf("未知进程应报 ESRCH: %v", err)
	}

	if err := table.SetCloexec(1, 42, true); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("未知描述符应报 EBADF: %v", err)
	}
}
