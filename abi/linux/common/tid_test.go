package common

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// fakeTidAddresses 记录 set_tid_address 的登记。
type fakeTidAddresses struct {
	registered uint64
	calls      int
	err        error
}

func (f *fakeTidAddresses) SetTidAddress(address uint64) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.registered = address
	return nil
}

func (f *fakeTidAddresses) TidAddress() uint64 { return f.registered }

// TestSetTidAddressRegistersAndReturnsTid 验证 K03:登记透传(含 0
// 撤销)并返回调用方 TID;端口缺失 ENOSYS;登记错误透传。
func TestSetTidAddressRegistersAndReturnsTid(t *testing.T) {
	ctx := context.Background()
	port := &fakeTidAddresses{}
	handler := Syscalls{
		TidAddresses: port,
		Identity:     &Identity{PID: 41, TID: 77},
	}
	if r := handler.Handle(ctx, Call{Syscall: SysSetTidAddress, Args: [6]uint64{0x1234}}); r.Errno != "" || r.Value != 77 {
		t.Fatalf("返回=%+v want TID 77", r)
	}
	if port.registered != 0x1234 || port.calls != 1 {
		t.Fatalf("登记=%#x calls=%d", port.registered, port.calls)
	}
	// TID 未区分时回退 PID。
	fallback := Syscalls{TidAddresses: port, Identity: &Identity{PID: 41}}
	if r := fallback.Handle(ctx, Call{Syscall: SysSetTidAddress, Args: [6]uint64{0}}); r.Errno != "" || r.Value != 41 {
		t.Fatalf("回退=%+v want PID 41", r)
	}
	// 端口未装配:ENOSYS。
	if r := (Syscalls{Identity: &Identity{PID: 1}}).Handle(ctx, Call{Syscall: SysSetTidAddress}); r.Errno != errno.NotImplemented {
		t.Fatalf("未装配=%+v want ENOSYS", r)
	}
	// 登记错误透传。
	broken := Syscalls{TidAddresses: &fakeTidAddresses{err: errno.NoProcess}, Identity: &Identity{PID: 1}}
	if r := broken.Handle(ctx, Call{Syscall: SysSetTidAddress}); r.Errno != errno.NoProcess {
		t.Fatalf("错误透传=%+v want ESRCH", r)
	}
}
