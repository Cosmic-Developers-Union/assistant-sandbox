package common

import (
	"context"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// BenchmarkGetpidSyscall 测量固定配置下最小系统调用的分派开销(T08)。
func BenchmarkGetpidSyscall(b *testing.B) {
	space := newFakeMemory()
	table := fd.New()
	table.Attach(1, fd.Streams{})
	handler := Syscalls{
		Memory:      space,
		Descriptors: ProcessDescriptors{Table: table, PID: 1},
		Identity:    &Identity{PID: 1},
	}
	call := Call{Syscall: SysGetPID}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		reply := handler.Handle(context.Background(), call)
		if reply.Errno != "" || reply.Value != 1 {
			b.Fatalf("getpid=%+v", reply)
		}
	}
}

// BenchmarkWriteSyscall 测量带缓冲区与描述符表的写路径开销(T08)。
func BenchmarkWriteSyscall(b *testing.B) {
	space := newFakeMemory()
	table := fd.New()
	table.Attach(1, fd.Streams{Output: io.Discard})
	handler := Syscalls{
		Memory:      space,
		Descriptors: ProcessDescriptors{Table: table, PID: 1},
		Identity:    &Identity{PID: 1},
	}
	call := Call{Syscall: SysWrite, Args: [6]uint64{1, 0x1000, 64}}
	b.SetBytes(64)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		reply := handler.Handle(context.Background(), call)
		if reply.Errno != "" || reply.Value != 64 {
			b.Fatalf("write=%+v", reply)
		}
	}
}
