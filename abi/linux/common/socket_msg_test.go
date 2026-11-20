package common

import (
	"context"
	"encoding/binary"
	"testing"
)

func putMsghdr(t *testing.T, s Syscalls, address uint64, name, nameLen, iov, iovLen, control, controlLen uint64) {
	t.Helper()
	var raw [msghdrSize]byte
	binary.LittleEndian.PutUint64(raw[0:8], name)
	binary.LittleEndian.PutUint64(raw[8:16], nameLen)
	binary.LittleEndian.PutUint64(raw[16:24], iov)
	binary.LittleEndian.PutUint64(raw[24:32], iovLen)
	binary.LittleEndian.PutUint64(raw[32:40], control)
	binary.LittleEndian.PutUint64(raw[40:48], controlLen)
	if err := s.Memory.Write(context.Background(), address, raw[:]); err != nil {
		t.Fatal(err)
	}
}

func putIovec(t *testing.T, s Syscalls, address uint64, index int, base, length uint64) {
	t.Helper()
	var raw [16]byte
	binary.LittleEndian.PutUint64(raw[0:8], base)
	binary.LittleEndian.PutUint64(raw[8:16], length)
	if err := s.Memory.Write(context.Background(), address+uint64(index*16), raw[:]); err != nil {
		t.Fatal(err)
	}
}

func putRightsControl(t *testing.T, s Syscalls, address uint64, fds ...uint32) {
	t.Helper()
	length := uint64(cmsghdrSize + 4*len(fds))
	raw := make([]byte, (length+7)&^7)
	binary.LittleEndian.PutUint64(raw[0:8], length)
	binary.LittleEndian.PutUint32(raw[8:12], solSocketLevel)
	binary.LittleEndian.PutUint32(raw[12:16], scmRights)
	for index, fd := range fds {
		binary.LittleEndian.PutUint32(raw[cmsghdrSize+index*4:], fd)
	}
	if err := s.Memory.Write(context.Background(), address, raw); err != nil {
		t.Fatal(err)
	}
}

// TestSendmsgScatterGatherAndRights 验证 M08 的发送侧:多个 iovec
// 按序拼接、SCM_RIGHTS fd 编号透传给 socket 端口、未知标志拒绝。
func TestSendmsgScatterGatherAndRights(t *testing.T) {
	ctx := context.Background()
	sockets := &fakeSockets{}
	handler := Syscalls{Memory: newFakeMemory(), Sockets: sockets}
	if err := handler.Memory.Write(ctx, 0x2000, []byte("ab")); err != nil {
		t.Fatal(err)
	}
	if err := handler.Memory.Write(ctx, 0x2100, []byte("cde")); err != nil {
		t.Fatal(err)
	}
	putIovec(t, handler, 0x1000, 0, 0x2000, 2)
	putIovec(t, handler, 0x1000, 1, 0x2100, 3)
	putRightsControl(t, handler, 0x3000, 9, 10)
	putMsghdr(t, handler, 0x4000, 0, 0, 0x1000, 2, 0x3000, 24)

	reply := handler.Handle(ctx, Call{Syscall: SysSendmsg, Args: [6]uint64{4, 0x4000, msgNosignal}})
	if reply.Errno != "" || reply.Value != 5 {
		t.Fatalf("sendmsg=%+v want 5", reply)
	}
	if len(sockets.msgBuffers) != 2 || string(sockets.msgBuffers[0]) != "ab" || string(sockets.msgBuffers[1]) != "cde" {
		t.Fatalf("分散缓冲=%q", sockets.msgBuffers)
	}
	if len(sockets.msgHandles) != 2 || sockets.msgHandles[0].(uint64) != 9 || sockets.msgHandles[1].(uint64) != 10 {
		t.Fatalf("传递句柄=%v", sockets.msgHandles)
	}

	// 未知标志 EINVAL。
	if reply := handler.Handle(ctx, Call{Syscall: SysSendmsg, Args: [6]uint64{4, 0x4000, 1}}); reply.Errno != "EINVAL：参数无效" {
		t.Fatalf("未知标志=%+v", reply)
	}
}

// TestRecvmsgScattersAcrossIovecsAndInstallsRights 验证 M08 的接收侧:
// 数据按多个 iovec 展开、来源地址可控、SCM_RIGHTS 安装为新描述符并
// 按 cmsg 布局回写。
func TestRecvmsgScattersAcrossIovecsAndInstallsRights(t *testing.T) {
	ctx := context.Background()
	sockets := &fakeSockets{returnHandles: []any{"handle"}}
	handler := Syscalls{Memory: newFakeMemory(), Sockets: sockets}

	putIovec(t, handler, 0x1000, 0, 0x5000, 1)
	putIovec(t, handler, 0x1000, 1, 0x5100, 2)
	putMsghdr(t, handler, 0x5300, 0, 0, 0x1000, 2, 0x5200, 24)

	reply := handler.Handle(ctx, Call{Syscall: SysRecvmsg, Args: [6]uint64{4, 0x5300, 0}})
	if reply.Errno != "" || reply.Value != 3 {
		t.Fatalf("recvmsg=%+v want 3", reply)
	}
	var first [1]byte
	var second [2]byte
	if err := handler.Memory.Read(ctx, 0x5000, first[:]); err != nil {
		t.Fatal(err)
	}
	if err := handler.Memory.Read(ctx, 0x5100, second[:]); err != nil {
		t.Fatal(err)
	}
	if string(first[:]) != "m" || string(second[:]) != "sg" {
		t.Fatalf("分散接收=%q/%q", first, second)
	}
	// SCM_RIGHTS 回写:长度 20、level/type=1、fd=InstallPassed 返回编号。
	var control [24]byte
	if err := handler.Memory.Read(ctx, 0x5200, control[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(control[0:8]) != 20 ||
		binary.LittleEndian.Uint32(control[8:12]) != solSocketLevel ||
		binary.LittleEndian.Uint32(control[12:16]) != scmRights {
		t.Fatalf("控制消息=% x", control[:20])
	}
	if got := binary.LittleEndian.Uint32(control[16:20]); got != uint32(sockets.nextFD) {
		t.Fatalf("安装描述符=%d want %d", got, sockets.nextFD)
	}
	var header [msghdrSize]byte
	if err := handler.Memory.Read(ctx, 0x5300, header[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(header[40:48]) != 20 || binary.LittleEndian.Uint64(header[48:56]) != 0 {
		t.Fatalf("msghdr controllen/flags=%d/%d", header[40:48], header[48:56])
	}
}

// TestRecvmsgTruncatesControlAndReleasesRights 验证 M08:控制缓冲
// 不足时数据照常交付、置 MSG_CTRUNC,且传递引用不被安装(不泄漏)。
func TestRecvmsgTruncatesControlAndReleasesRights(t *testing.T) {
	ctx := context.Background()
	released := 0
	handle := &releasableHandle{release: func() { released++ }}
	sockets := &fakeSockets{returnHandles: []any{handle}}
	handler := Syscalls{Memory: newFakeMemory(), Sockets: sockets}

	putIovec(t, handler, 0x1000, 0, 0x5000, 3)
	putMsghdr(t, handler, 0x5300, 0, 0, 0x1000, 1, 0x5200, 8)
	reply := handler.Handle(ctx, Call{Syscall: SysRecvmsg, Args: [6]uint64{4, 0x5300, 0}})
	if reply.Errno != "" || reply.Value != 3 {
		t.Fatalf("recvmsg=%+v", reply)
	}
	if released != 1 {
		t.Fatalf("截断应归还引用,release=%d", released)
	}
	var header [msghdrSize]byte
	if err := handler.Memory.Read(ctx, 0x5300, header[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint64(header[40:48]) != 0 {
		t.Fatalf("truncated controllen=%d want 0", header[40:48])
	}
	if binary.LittleEndian.Uint64(header[48:56])&msgCtrunc == 0 {
		t.Fatalf("flags 应含 MSG_CTRUNC: %d", header[48:56])
	}
	if len(sockets.passed) != 0 {
		t.Fatalf("截断不应安装描述符: %v", sockets.passed)
	}
}

type releasableHandle struct{ release func() }

func (h *releasableHandle) Release() error {
	h.release()
	return nil
}

// TestSendmsgValidatesControlAndVectors 验证 M08 的错误路径:畸形
// 控制消息、超限 iovec 与未装配端口均明确失败。
func TestSendmsgValidatesControlAndVectors(t *testing.T) {
	ctx := context.Background()
	handler := Syscalls{Memory: newFakeMemory(), Sockets: &fakeSockets{}}

	var malformed [16]byte
	binary.LittleEndian.PutUint64(malformed[0:8], 64) // 超过控制缓冲
	if err := handler.Memory.Write(ctx, 0x3000, malformed[:]); err != nil {
		t.Fatal(err)
	}
	putIovec(t, handler, 0x1000, 0, 0x2000, 1)
	putMsghdr(t, handler, 0x4000, 0, 0, 0x1000, 1, 0x3000, 16)
	if reply := handler.Handle(ctx, Call{Syscall: SysSendmsg, Args: [6]uint64{4, 0x4000, 0}}); reply.Errno != "EINVAL：参数无效" {
		t.Fatalf("畸形 cmsg=%+v", reply)
	}

	putMsghdr(t, handler, 0x4000, 0, 0, 0x1000, iovMax+1, 0, 0)
	if reply := handler.Handle(ctx, Call{Syscall: SysSendmsg, Args: [6]uint64{4, 0x4000, 0}}); reply.Errno != "EINVAL：参数无效" {
		t.Fatalf("超限 iovec=%+v", reply)
	}

	empty := Syscalls{Memory: newFakeMemory()}
	if reply := empty.Handle(ctx, Call{Syscall: SysRecvmsg, Args: [6]uint64{1, 0x4000, 0}}); reply.Errno != "ENOSYS：接口尚未实现" {
		t.Fatalf("未装配端口=%+v", reply)
	}
}
