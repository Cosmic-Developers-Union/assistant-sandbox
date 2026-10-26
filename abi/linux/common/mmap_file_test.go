package common

import (
	"context"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// randomFileHandle 是可随机访问的测试文件句柄(E08/E09)。
type randomFileHandle struct {
	data     []byte
	readable bool
	writable bool
}

func (h *randomFileHandle) Read(p []byte) (int, error) { return copy(p, h.data), nil }
func (h *randomFileHandle) Write(p []byte) (int, error) {
	h.data = append(h.data, p...)
	return len(p), nil
}
func (h *randomFileHandle) Close() error { return nil }
func (h *randomFileHandle) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(h.data)) {
		return 0, nil
	}
	return copy(p, h.data[off:]), nil
}
func (h *randomFileHandle) WriteAt(p []byte, off int64) (int, error) {
	if end := off + int64(len(p)); end > int64(len(h.data)) {
		grown := make([]byte, end)
		copy(grown, h.data)
		h.data = grown
	}
	return copy(h.data[off:], p), nil
}
func (h *randomFileHandle) Readable() bool { return h.readable }
func (h *randomFileHandle) Writable() bool { return h.writable }

// TestMmapFileMappingValidatesAndDelegates 验证 E08/E09:文件映射经
// 描述符解析与访问模式校验后委托地址空间;不可定位描述符 ENODEV,
// 偏移未对齐 EINVAL,访问模式与保护位冲突 EACCES。
func TestMmapFileMappingValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	table := fd.New()
	const pid = 51
	table.Attach(pid, fd.Streams{})
	file := &randomFileHandle{data: []byte("payload"), readable: true, writable: true}
	if err := table.Install(pid, 3, file); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(pid, 4, &plainHandle{}); err != nil {
		t.Fatal(err)
	}
	if err := table.Install(pid, 5, &randomFileHandle{readable: true}); err != nil { // 只读
		t.Fatal(err)
	}
	if err := table.Install(pid, 6, &randomFileHandle{writable: true}); err != nil { // 只写
		t.Fatal(err)
	}
	handler := Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
	}
	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"坏描述符", [6]uint64{0, 4096, ProtRead, mapPrivate, 99, 0}, errno.BadHandle},
		{"不可定位描述符", [6]uint64{0, 4096, ProtRead, mapPrivate, 4, 0}, errno.NoDevice},
		{"偏移未对齐", [6]uint64{0, 4096, ProtRead, mapPrivate, 3, 4095}, errno.Invalid},
		{"只读文件共享写", [6]uint64{0, 4096, ProtRead | ProtWrite, mapShared, 5, 0}, errno.Permission},
		{"只写文件读", [6]uint64{0, 4096, ProtRead, mapPrivate, 6, 0}, errno.Permission},
	} {
		mapper := &fakeMapper{reply: 0x100000000}
		handler.Mapper = mapper
		if reply := handler.Handle(ctx, Call{Syscall: SysMmap, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(mapper.files) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达地址空间", tc.label)
		}
	}

	// 私有映射:委托 Shared=false,偏移与保护位透传;只读文件的
	// 私有可写映射合法(私有修改不写回)。
	mapper := &fakeMapper{reply: 0x100000000}
	handler.Mapper = mapper
	reply := handler.Handle(ctx, Call{Syscall: SysMmap, Args: [6]uint64{0, 8192, ProtRead | ProtWrite, mapPrivate, 5, 8192}})
	if reply.Errno != "" || reply.Value != 0x100000000 {
		t.Fatalf("私有映射: %+v", reply)
	}
	if len(mapper.files) != 1 || mapper.files[0].Shared || mapper.files[0].Offset != 8192 || mapper.files[0].Length != 8192 {
		t.Fatalf("私有映射请求: %+v", mapper.files[0])
	}
	// 共享映射:Shared=true,长度向上取整。
	reply = handler.Handle(ctx, Call{Syscall: SysMmap, Args: [6]uint64{0, 4097, ProtRead | ProtWrite, mapShared, 3, 0}})
	if reply.Errno != "" || reply.Value != 0x100000000 {
		t.Fatalf("共享映射: %+v", reply)
	}
	if !mapper.files[1].Shared || mapper.files[1].Length != 8192 {
		t.Fatalf("共享映射请求: %+v", mapper.files[1])
	}
}

// TestMsyncValidatesAndDelegates 验证 E09:msync 标志校验与范围委托。
func TestMsyncValidatesAndDelegates(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label string
		args  [6]uint64
		want  errno.Code
	}{
		{"未知标志", [6]uint64{0x1000, 4096, 0x8, 0, 0, 0}, errno.Invalid},
		{"同步异步并存", [6]uint64{0x1000, 4096, msSync | msAsync, 0, 0, 0}, errno.Invalid},
		{"缺同步类型", [6]uint64{0x1000, 4096, msInvalidate, 0, 0, 0}, errno.Invalid},
		{"地址未对齐", [6]uint64{0x1001, 4096, msSync, 0, 0, 0}, errno.Invalid},
		{"零长度", [6]uint64{0x1000, 0, msSync, 0, 0, 0}, errno.Invalid},
	} {
		mapper := &fakeMapper{}
		handler := Syscalls{Mapper: mapper}
		if reply := handler.Handle(ctx, Call{Syscall: SysMsync, Args: tc.args}); reply.Errno != tc.want {
			t.Errorf("%s: err=%v want %v", tc.label, reply.Errno, tc.want)
		}
		if len(mapper.synced) != 0 {
			t.Errorf("%s: 被拒绝的调用不得触达地址空间", tc.label)
		}
	}
	mapper := &fakeMapper{}
	handler := Syscalls{Mapper: mapper}
	if reply := handler.Handle(ctx, Call{Syscall: SysMsync, Args: [6]uint64{0x2000, 4096, msSync, 0, 0, 0}}); reply.Errno != "" {
		t.Fatalf("正常同步: %+v", reply)
	}
	if len(mapper.synced) != 1 || mapper.synced[0] != [2]uint64{0x2000, 0x3000} {
		t.Fatalf("同步范围: %+v", mapper.synced)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysMsync, Args: [6]uint64{0x2000, 4096, msAsync | msInvalidate, 0, 0, 0}}); reply.Errno != "" {
		t.Fatalf("异步+失效: %+v", reply)
	}
}
