package elf

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// TestSyscallHandlerWiresGuestRandom 验证 L04 回归:ELF 系统调用处理器
// 必须把进程熵源接到 getrandom 端口;此前缺失导致真实 libc/Python
// 收到 ENOSYS 并走不可用回退路径。
func TestSyscallHandlerWiresGuestRandom(t *testing.T) {
	process := proc.Proc{
		ID: 1, TID: 1, TGID: 1, Dir: "/",
		Resources: capability.Set{Entropy: entropy.NewSeeded(7)},
	}
	handler := syscalls(context.Background(), process, nil, nil, nil)
	if handler.Random == nil {
		t.Fatal("getrandom 熵源未接线")
	}
}
