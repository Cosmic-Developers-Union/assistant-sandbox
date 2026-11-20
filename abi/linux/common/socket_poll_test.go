package common

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// socketPollHarness 构造真实 fd 表 + 命名空间的系统调用层:监听与
// 连接端点直接经 vm/net 创建并安装为描述符,poll/epoll 系统调用经
// 既有 pollSource 能力断言工作(M07)。
func socketPollHarness(t *testing.T) (Syscalls, *fd.Table, *net.Namespace) {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = system
	const pid = 81
	table := fd.New()
	table.Attach(pid, fd.Streams{})
	ns := net.New()
	virtual := &clock.Virtual{}
	return Syscalls{
		Memory:      newFakeMemory(),
		Descriptors: ProcessDescriptors{Table: table, PID: pid},
		Files:       ProcessFiles{System: system, Store: table, PID: pid, Dir: "/"},
		Clock:       virtual,
		Sleeper:     virtual,
	}, table, ns
}

// TestEpollReportsListenerAndConnectionReadiness 验证 M07:监听套接字
// 的连接完成与已连接套接字的接收/断连经 epoll 就绪上报,等待不忙等
// (超时路径)且通知不丢失(事件路径)。
func TestEpollReportsListenerAndConnectionReadiness(t *testing.T) {
	ctx := context.Background()
	s, table, ns := socketPollHarness(t)
	const pid = 81

	// 监听套接字(经 vm/net 直接装配,epoll_ctl 走既有能力断言)。
	listener, err := ns.NewINETSocket(net.SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Bind(net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 9200}); err != nil {
		t.Fatal(err)
	}
	if err := listener.Listen(4); err != nil {
		t.Fatal(err)
	}
	listener.SetNonBlock(true)
	if err := table.Install(pid, 3, listener); err != nil {
		t.Fatal(err)
	}

	// epoll_create1 + epoll_ctl(ADD)。
	createReply := s.Handle(ctx, Call{Syscall: SysEpollCreate1, Args: [6]uint64{}})
	if createReply.Errno != "" {
		t.Fatalf("epoll_create1: %+v", createReply)
	}
	epfd := createReply.Value
	// epoll_event:events(u32)+data(u64),16 字节。
	writeEvent := func(address uint64, events uint32, data uint64) {
		var raw [16]byte
		binary.LittleEndian.PutUint32(raw[0:4], events)
		binary.LittleEndian.PutUint64(raw[8:16], data)
		if err := s.Memory.Write(ctx, address, raw[:]); err != nil {
			t.Fatal(err)
		}
	}
	writeEvent(0xE000, 1 /*EPOLLIN*/, 0x1234)
	if reply := s.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, 1 /*ADD*/, 3, 0xE000}}); reply.Errno != "" {
		t.Fatalf("epoll_ctl: %+v", reply)
	}

	// 无连接:100ms 超时返回 0(不忙等)。
	if reply := s.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0xE100, 4, 100}}); reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("超时等待: %+v", reply)
	}

	// 连接完成:epoll 立即可读。
	clientConn, err := ns.ConnectTCP(ctx, net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4}, net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 9200})
	if err != nil {
		t.Fatal(err)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0xE100, 4, 0}}); reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("连接完成就绪: %+v", reply)
	}
	var event [16]byte
	if err := s.Memory.Read(ctx, 0xE100, event[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(event[0:4])&1 == 0 || binary.LittleEndian.Uint64(event[8:16]) != 0x1234 {
		t.Fatalf("事件=% x", event)
	}

	// 接受并把已连接套接字加入 epoll。
	accepted, err := listener.Accept(ctx)
	if err != nil {
		t.Fatalf("应有待接受连接: %v", err)
	}
	if err := table.Install(pid, 5, accepted); err != nil {
		t.Fatal(err)
	}
	writeEvent(0xE200, 1, 0x5678)
	if reply := s.Handle(ctx, Call{Syscall: SysEpollCtl, Args: [6]uint64{epfd, 1, 5, 0xE200}}); reply.Errno != "" {
		t.Fatalf("epoll_ctl conn: %+v", reply)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0xE300, 4, 0}}); reply.Errno != "" || reply.Value != 0 {
		t.Fatalf("无数据应无事件: %+v", reply)
	}
	if _, err := clientConn.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0xE300, 4, 0}}); reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("数据到达就绪: %+v", reply)
	}

	// 断连:HUP(0x2000) 与 IN 一起上报。
	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}
	if reply := s.Handle(ctx, Call{Syscall: SysEpollPwait, Args: [6]uint64{epfd, 0xE300, 4, 0}}); reply.Errno != "" || reply.Value != 1 {
		t.Fatalf("断连就绪: %+v", reply)
	}
	if err := s.Memory.Read(ctx, 0xE300, event[:]); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(event[0:4])&(1|0x2000) == 0 {
		t.Fatalf("断连事件应含 IN|HUP: %x", event[0:4])
	}
}
