package common

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// fakeSockets 记录 socket 系统调用的端口请求(ABI 分派测试)。
type fakeSockets struct {
	domain, typ, protocol uint64
	nonblock, cloexec     bool
	paired                bool
	bound                 net.Addr
	listenBacklog         int
	connected             net.Addr
	shutdownHow           net.ShutdownHow
	nextFD                uint64
	optionLevel           uint64
	optionName            uint64
}

func (f *fakeSockets) Socket(domain, typ, protocol uint64, nonblock, cloexec bool) (uint64, error) {
	f.domain, f.typ, f.protocol = domain, typ, protocol
	f.nonblock, f.cloexec = nonblock, cloexec
	f.nextFD++
	return f.nextFD, nil
}

func (f *fakeSockets) SocketPair(domain, typ, protocol uint64, nonblock, cloexec bool) (uint64, uint64, error) {
	f.paired = true
	f.nextFD += 2
	return f.nextFD - 1, f.nextFD, nil
}

func (f *fakeSockets) Bind(_ context.Context, fd uint64, addr net.Addr) error {
	f.bound = addr
	return nil
}

func (f *fakeSockets) Listen(fd uint64, backlog int) error {
	f.listenBacklog = backlog
	return nil
}

func (f *fakeSockets) Accept(_ context.Context, fd uint64, nonblock, cloexec bool) (uint64, error) {
	f.nextFD++
	return f.nextFD, nil
}

func (f *fakeSockets) Connect(_ context.Context, fd uint64, addr net.Addr) error {
	f.connected = addr
	return nil
}

func (f *fakeSockets) SendTo(_ context.Context, fd uint64, data []byte, to *net.Addr) (int, error) {
	return len(data), nil
}

func (f *fakeSockets) RecvFrom(_ context.Context, fd uint64, buffer []byte) (int, net.Addr, error) {
	copy(buffer, "hi")
	return 2, net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 1}, nil
}

func (f *fakeSockets) Shutdown(fd uint64, how net.ShutdownHow) error {
	f.shutdownHow = how
	return nil
}

func (f *fakeSockets) SockName(fd uint64) (net.Addr, error) {
	return net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 7000}, nil
}

func (f *fakeSockets) PeerName(fd uint64) (net.Addr, error) {
	return net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 7001}, nil
}

func (f *fakeSockets) GetOption(fd uint64, level, name uint64) (uint64, error) {
	f.optionLevel, f.optionName = level, name
	return 1, nil
}

func (f *fakeSockets) SetOption(fd uint64, level, name, value uint64) error {
	f.optionLevel, f.optionName = level, name
	return nil
}

// TestSocketSyscallsDispatchWithSockaddrEncoding 验证 M02-M06 的 ABI
// 分派与 sockaddr 编解码:创建(SOCK_NONBLOCK/CLOEXEC 提取)、绑定/
// 连接的 AF_INET 16 字节布局(端口大端)、accept4 标志、shutdown、
// 选项层级透传;无端口报 NotImplemented 族。
func TestSocketSyscallsDispatchWithSockaddrEncoding(t *testing.T) {
	ctx := context.Background()
	sockets := &fakeSockets{}
	handler := Syscalls{Memory: newFakeMemory(), Sockets: sockets}

	// socket(AF_INET, SOCK_STREAM|SOCK_NONBLOCK|SOCK_CLOEXEC, 0)。
	reply := handler.Handle(ctx, Call{Syscall: SysSocket, Args: [6]uint64{2, 1 | 0o4000 | 0o2000000}})
	if reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("socket: %+v", reply)
	}
	if sockets.domain != 2 || sockets.typ != 1 || !sockets.nonblock || !sockets.cloexec {
		t.Fatalf("socket 参数=%+v", sockets)
	}

	// bind:sockaddr_in 16 字节,端口大端 0x1F90=8080,地址 127.0.0.1。
	var addr [16]byte
	binary.LittleEndian.PutUint16(addr[0:2], 2)   // AF_INET
	binary.BigEndian.PutUint16(addr[2:4], 0x1F90) // 端口(网络序)
	copy(addr[4:8], []byte{127, 0, 0, 1})
	if err := handler.Memory.Write(ctx, 0xA000, addr[:]); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysBind, Args: [6]uint64{1, 0xA000, 16}}); reply.Errno != "" {
		t.Fatalf("bind: %+v", reply)
	}
	if sockets.bound.Port != 0x1F90 || sockets.bound.IP.String() != "127.0.0.1" {
		t.Fatalf("绑定地址=%+v", sockets.bound)
	}

	// listen。
	if reply := handler.Handle(ctx, Call{Syscall: SysListen, Args: [6]uint64{1, 32}}); reply.Errno != "" {
		t.Fatalf("listen: %+v", reply)
	}
	if sockets.listenBacklog != 32 {
		t.Fatalf("backlog=%d", sockets.listenBacklog)
	}

	// accept4(SOCK_NONBLOCK)。
	if reply := handler.Handle(ctx, Call{Syscall: SysAccept4, Args: [6]uint64{1, 0, 0, 0o4000}}); reply.Errno != "" || reply.Value != 2 {
		t.Fatalf("accept4: %+v", reply)
	}
	// accept 等价 flags=0。
	if reply := handler.Handle(ctx, Call{Syscall: SysAccept, Args: [6]uint64{1}}); reply.Errno != "" || reply.Value != 3 {
		t.Fatalf("accept: %+v", reply)
	}

	// connect(同一地址)。
	if reply := handler.Handle(ctx, Call{Syscall: SysConnect, Args: [6]uint64{3, 0xA000, 16}}); reply.Errno != "" {
		t.Fatalf("connect: %+v", reply)
	}
	if sockets.connected.Port != 0x1F90 {
		t.Fatalf("连接地址=%+v", sockets.connected)
	}

	// shutdown(SHUT_WR)。
	if reply := handler.Handle(ctx, Call{Syscall: SysShutdown, Args: [6]uint64{3, 1}}); reply.Errno != "" {
		t.Fatalf("shutdown: %+v", reply)
	}
	if sockets.shutdownHow != net.ShutdownWrite {
		t.Fatalf("shutdown how=%v", sockets.shutdownHow)
	}

	// getsockname:地址回写(端口转回网络序)。
	if err := handler.Memory.Write(ctx, 0xB000, []byte{16, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysGetsockname, Args: [6]uint64{3, 0xA100, 0xB000}}); reply.Errno != "" {
		t.Fatalf("getsockname: %+v", reply)
	}
	var back [16]byte
	if err := handler.Memory.Read(ctx, 0xA100, back[:]); err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint16(back[2:4]) != 7000 {
		t.Fatalf("回写端口=%x", back[2:4])
	}
	if got := binary.LittleEndian.Uint32(make([]byte, 4)); false {
		_ = got
	}

	// getsockopt/setsockopt 层级透传。
	if reply := handler.Handle(ctx, Call{Syscall: SysGetsockopt, Args: [6]uint64{3, 1, 9, 0xC000, 0xC100}}); reply.Errno != "" {
		t.Fatalf("getsockopt: %+v", reply)
	}
	if sockets.optionLevel != 1 || sockets.optionName != 9 {
		t.Fatalf("选项参数=%+v", sockets)
	}
	if reply := handler.Handle(ctx, Call{Syscall: SysSetsockopt, Args: [6]uint64{3, 6, 1, 0xC200, 4}}); reply.Errno != "" {
		t.Fatalf("setsockopt: %+v", reply)
	}
	if sockets.optionLevel != 6 || sockets.optionName != 1 {
		t.Fatalf("设置选项=%+v", sockets)
	}

	// socketpair(AF_UNIX, SOCK_STREAM):两个编号写回。
	if reply := handler.Handle(ctx, Call{Syscall: SysSocketpair, Args: [6]uint64{1, 1, 0, 0xD000}}); reply.Errno != "" {
		t.Fatalf("socketpair: %+v", reply)
	}
	if !sockets.paired {
		t.Fatal("socketpair 应转发端口")
	}
	var pair [8]byte
	if err := handler.Memory.Read(ctx, 0xD000, pair[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(pair[0:4]) == 0 || binary.LittleEndian.Uint32(pair[4:8]) == 0 {
		t.Fatalf("socketpair 编号=% x", pair)
	}

	// 无端口:ENOSYS 族。
	if reply := (Syscalls{}).Handle(ctx, Call{Syscall: SysSocket, Args: [6]uint64{2, 1}}); reply.Errno != errno.NotImplemented {
		t.Fatalf("无端口=%+v want ENOSYS 族", reply)
	}
	// 端口错误透传。
	failing := &fakeSockets{}
	_ = failing
	if reply := (Syscalls{Sockets: &errSockets{}}).Handle(ctx, Call{Syscall: SysSocket, Args: [6]uint64{2, 1}}); reply.Errno != errno.IO {
		t.Fatalf("端口错误=%+v want EIO", reply)
	}
}

type errSockets struct{}

func (f *errSockets) Socket(uint64, uint64, uint64, bool, bool) (uint64, error) {
	return 0, errors.New("boom")
}

func (f *errSockets) SocketPair(uint64, uint64, uint64, bool, bool) (uint64, uint64, error) {
	return 0, 0, errors.New("boom")
}

func (f *errSockets) Bind(context.Context, uint64, net.Addr) error { return errors.New("boom") }

func (f *errSockets) Listen(uint64, int) error { return errors.New("boom") }

func (f *errSockets) Accept(context.Context, uint64, bool, bool) (uint64, error) {
	return 0, errors.New("boom")
}

func (f *errSockets) Connect(context.Context, uint64, net.Addr) error { return errors.New("boom") }

func (f *errSockets) SendTo(context.Context, uint64, []byte, *net.Addr) (int, error) {
	return 0, errors.New("boom")
}

func (f *errSockets) RecvFrom(context.Context, uint64, []byte) (int, net.Addr, error) {
	return 0, net.Addr{}, errors.New("boom")
}

func (f *errSockets) Shutdown(uint64, net.ShutdownHow) error { return errors.New("boom") }

func (f *errSockets) SockName(uint64) (net.Addr, error) { return net.Addr{}, errors.New("boom") }

func (f *errSockets) PeerName(uint64) (net.Addr, error) { return net.Addr{}, errors.New("boom") }

func (f *errSockets) GetOption(uint64, uint64, uint64) (uint64, error) {
	return 0, errors.New("boom")
}

func (f *errSockets) SetOption(uint64, uint64, uint64, uint64) error { return errors.New("boom") }
