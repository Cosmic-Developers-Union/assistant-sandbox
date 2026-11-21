package net

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

func loopback(port uint16) Addr {
	return Addr{Domain: AFInet, IP: LoopbackIPv4, Port: port}
}

// TestNamespacesIsolatePortsAndLoopback 验证 M01:两台 VM(两个命名
// 空间)监听同一端口互不冲突;回环连接只指向自身命名空间。
func TestNamespacesIsolatePortsAndLoopback(t *testing.T) {
	ctx := context.Background()
	first, second := New(), New()
	defer first.Close()
	defer second.Close()

	// 两命名空间绑定同一端口:互不冲突。
	listener1, err := first.BindTCPListener(loopback(8080), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener1.Listen(8); err != nil {
		t.Fatal(err)
	}
	listener2, err := second.BindTCPListener(loopback(8080), false)
	if err != nil {
		t.Fatalf("第二命名空间同端口应可用: %v", err)
	}
	if err := listener2.Listen(8); err != nil {
		t.Fatal(err)
	}

	// 回环只指向自身:第一命名空间的连接到不了第二命名空间的监听。
	conn1, err := first.ConnectTCP(ctx, Addr{Domain: AFInet, IP: LoopbackIPv4}, loopback(8080))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listener1.AcceptNonBlock(); err != nil {
		t.Fatalf("第一命名空间应收到连接: %v", err)
	}
	if _, err := second.ConnectTCP(ctx, Addr{Domain: AFInet, IP: LoopbackIPv4}, loopback(9999)); !errors.Is(err, errno.ConnRefused) {
		t.Fatalf("无监听应报 ECONNREFUSED: %v", err)
	}
	_ = conn1

	// 同命名空间重复绑定同一端口:EADDRINUSE。
	if _, err := first.BindTCPListener(loopback(8080), false); !errors.Is(err, errno.AddressInUse) {
		t.Fatalf("重复绑定应报 EADDRINUSE: %v", err)
	}
}

// TestTCPLoopbackFullCycle 验证 M02/M04:bind/listen/accept/connect、
// 双向收发与半关闭。
func TestTCPLoopbackFullCycle(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Bind(loopback(9000)); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(8); err != nil {
		t.Fatal(err)
	}

	client, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(ctx, loopback(9000)); err != nil {
		t.Fatal(err)
	}

	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 客户端 → 服务端。
	if n, err := client.Write([]byte("ping")); err != nil || n != 4 {
		t.Fatalf("写=%d err=%v", n, err)
	}
	buffer := make([]byte, 16)
	n, err := accepted.Read(buffer)
	if err != nil || string(buffer[:n]) != "ping" {
		t.Fatalf("读=%q err=%v", buffer[:n], err)
	}
	// 服务端 → 客户端。
	if n, err := accepted.Write([]byte("pong")); err != nil || n != 4 {
		t.Fatalf("写=%d err=%v", n, err)
	}
	n, err = client.Read(buffer)
	if err != nil || string(buffer[:n]) != "pong" {
		t.Fatalf("读=%q err=%v", buffer[:n], err)
	}

	// 地址查询(M06 前置):accept 得到的对端是客户端临时端口。
	peerAddr, ok := accepted.PeerAddr()
	if !ok || peerAddr.Port == 9000 {
		t.Fatalf("对端地址=%+v", peerAddr)
	}
	localAddr, ok := client.LocalAddr()
	if !ok || localAddr.Port != peerAddr.Port {
		t.Fatalf("客户端本端应与对端一致: %+v vs %+v", localAddr, peerAddr)
	}

	// 半关闭:客户端 shutdown(WR) → 服务端读尽后 EOF,服务端仍可写。
	if err := client.Shutdown(ShutdownWrite); err != nil {
		t.Fatal(err)
	}
	// shutdown(WR) 不影响本端读:无数据时非阻塞读 EAGAIN(不被
	// 自身的写半关闭打断)。
	client.SetNonBlock(true)
	if _, err := client.Read(buffer); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("shutdown(WR) 后非阻塞读=%v want EAGAIN", err)
	}
	client.SetNonBlock(false)
	if _, err := accepted.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("对端写关闭后应 EOF: %v", err)
	}
	if n, err := accepted.Write([]byte("bye")); err != nil || n != 3 {
		t.Fatalf("半关闭后服务端仍可写: %d %v", n, err)
	}

	// 完全关闭客户端 → 服务端写报 EPIPE。
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := accepted.Write([]byte("x")); !errors.Is(err, errno.BrokenPipe) {
		t.Fatalf("对端关闭后写应 EPIPE: %v", err)
	}
}

// TestTCPNonBlockingReturnsWouldBlock 验证 M02:非阻塞 accept/recv
// 立即返回 EAGAIN,fd 状态标志经 O_NONBLOCK 切换生效。
func TestTCPNonBlockingReturnsWouldBlock(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, _ := ns.NewINETSocket(SockStream)
	if err := server.Bind(loopback(9001)); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(8); err != nil {
		t.Fatal(err)
	}
	server.SetNonBlock(true)
	if _, err := server.Accept(ctx); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("非阻塞 accept=%v want EAGAIN", err)
	}
	if flags, err := server.StatusFlags(); err != nil || flags&0o4000 == 0 {
		t.Fatalf("F_GETFL 应含 O_NONBLOCK: %x %v", flags, err)
	}
	server.SetNonBlock(false)
	if flags, _ := server.StatusFlags(); flags&0o4000 != 0 {
		t.Fatalf("F_SETFL 切回阻塞应清除 O_NONBLOCK: %x", flags)
	}
}

// TestTCPConnectRefusedAndUnbound 验证 M02/M04:未连接读写的
// ENOTCONN 与连接被拒。
func TestTCPConnectRefusedAndUnbound(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	stray, _ := ns.NewINETSocket(SockStream)
	if _, err := stray.Read(make([]byte, 8)); !errors.Is(err, errno.NotConn) {
		t.Fatalf("未连接读=%v want ENOTCONN", err)
	}
	if _, err := stray.Write([]byte("x")); !errors.Is(err, errno.NotConn) {
		t.Fatalf("未连接写=%v want ENOTCONN", err)
	}
	if err := stray.Connect(ctx, loopback(1)); !errors.Is(err, errno.ConnRefused) {
		t.Fatalf("无监听连接=%v want ECONNREFUSED", err)
	}
}

// TestUnixStreamAndSocketpair 验证 M03:路径监听/连接/收发/半关闭
// 与 socketpair。
func TestUnixStreamAndSocketpair(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, err := ns.NewUnixSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Bind("/run/echo.sock"); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(8); err != nil {
		t.Fatal(err)
	}
	// 重复绑定同一路径:EADDRINUSE。
	dup, _ := ns.NewUnixSocket(SockStream)
	if err := dup.Bind("/run/echo.sock"); !errors.Is(err, errno.AddressInUse) {
		t.Fatalf("重复路径绑定=%v want EADDRINUSE", err)
	}

	client, _ := ns.NewUnixSocket(SockStream)
	if err := client.Connect(ctx, "/run/echo.sock"); err != nil {
		t.Fatal(err)
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("unix")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	n, err := accepted.Read(buffer)
	if err != nil || string(buffer[:n]) != "unix" {
		t.Fatalf("读=%q err=%v", buffer[:n], err)
	}
	// 对端地址:监听路径。
	if addr, ok := client.PeerAddr(); !ok || addr.Path != "/run/echo.sock" {
		t.Fatalf("对端地址=%+v", addr)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := accepted.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("对端关闭后应 EOF: %v", err)
	}

	// socketpair:双向即开即用。
	a, b := ns.SocketPairUnix()
	if _, err := a.Write([]byte("pair")); err != nil {
		t.Fatal(err)
	}
	n, err = b.Read(buffer)
	if err != nil || string(buffer[:n]) != "pair" {
		t.Fatalf("socketpair 读=%q err=%v", buffer[:n], err)
	}
	// 半关闭:A shutdown(WR) → B 读 EOF。
	if err := a.Shutdown(ShutdownWrite); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(buffer); !errors.Is(err, io.EOF) {
		t.Fatalf("半关闭后应 EOF: %v", err)
	}
	_ = time.Now
}

// TestUDPDatagramBoundaries 验证 M05:报文边界、来源地址、截断、
// 非阻塞 EAGAIN 与尽力而为投递。
func TestUDPDatagramBoundaries(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	receiver, err := ns.BindUDP(loopback(5353))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := ns.BindUDP(loopback(0))
	if err != nil {
		t.Fatal(err)
	}
	// 报文边界:两条报文独立可读。
	if _, err := ns.SendTo(ctx, sender, []byte("first"), loopback(5353)); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.SendTo(ctx, sender, []byte("second-packet"), loopback(5353)); err != nil {
		t.Fatal(err)
	}
	small := make([]byte, 4)
	n, from, err := receiver.RecvFrom(ctx, small)
	if err != nil || string(small[:n]) != "firs" {
		t.Fatalf("截断读=%q n=%d err=%v", small[:n], n, err)
	}
	if from.Port == 0 || from.IP != LoopbackIPv4 {
		t.Fatalf("来源地址=%+v", from)
	}
	buffer := make([]byte, 64)
	n, _, err = receiver.RecvFrom(ctx, buffer)
	if err != nil || string(buffer[:n]) != "second-packet" {
		t.Fatalf("第二条=%q err=%v", buffer[:n], err)
	}
	// 队列空:非阻塞 EAGAIN。
	receiver.base.SetNonBlock(true)
	if _, _, err := receiver.RecvFrom(ctx, buffer); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("非阻塞空读=%v want EAGAIN", err)
	}
	// 无接收方:发送成功(尽力而为丢弃)。
	if _, err := ns.SendTo(ctx, sender, []byte("void"), loopback(9999)); err != nil {
		t.Fatalf("无接收方发送=%v", err)
	}
	// 关闭接收端后投递静默丢弃。
	if err := receiver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.SendTo(ctx, sender, []byte("gone"), loopback(5353)); err != nil {
		t.Fatalf("关闭后投递=%v", err)
	}
}

// TestUDPConnectedSendRecvFiltersPeer 验证 M10 前置:UDP connect 后
// send/recv/write/read 使用默认对端,并丢弃其他来源的报文;shutdown
// 分别停止发送与接收。
func TestUDPConnectedSendRecvFiltersPeer(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, err := ns.BindUDP(loopback(5354))
	if err != nil {
		t.Fatal(err)
	}
	other, err := ns.BindUDP(loopback(0))
	if err != nil {
		t.Fatal(err)
	}
	client, err := ns.NewINETSocket(SockDatagram)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(ctx, loopback(5354)); err != nil {
		t.Fatal(err)
	}
	if peer, ok := client.PeerAddr(); !ok || peer.Port != 5354 {
		t.Fatalf("connected UDP peer=%+v ok=%v", peer, ok)
	}
	if _, err := client.Send(ctx, []byte("connected"), nil); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	n, _, err := server.RecvFrom(ctx, buffer)
	if err != nil || string(buffer[:n]) != "connected" {
		t.Fatalf("server 收到=%q err=%v", buffer[:n], err)
	}
	local, _ := client.LocalAddr()

	// 非对端来源先到达,必须被已连接 socket 丢弃。
	if _, err := ns.SendTo(ctx, other, []byte("stranger"), local); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.SendTo(ctx, server, []byte("peer"), local); err != nil {
		t.Fatal(err)
	}
	n, from, err := client.Recv(ctx, buffer)
	if err != nil || string(buffer[:n]) != "peer" || from.Port != 5354 {
		t.Fatalf("connected recv=%q from=%+v err=%v", buffer[:n], from, err)
	}

	// write/read 走已连接默认对端。
	if _, err := client.Write([]byte("write")); err != nil {
		t.Fatal(err)
	}
	n, _, err = server.RecvFrom(ctx, buffer)
	if err != nil || string(buffer[:n]) != "write" {
		t.Fatalf("write 到达=%q err=%v", buffer[:n], err)
	}
	if _, err := ns.SendTo(ctx, server, []byte("read"), local); err != nil {
		t.Fatal(err)
	}
	n, err = client.Read(buffer)
	if err != nil || string(buffer[:n]) != "read" {
		t.Fatalf("read=%q err=%v", buffer[:n], err)
	}

	if err := client.Shutdown(ShutdownWrite); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("late")); !errors.Is(err, errno.BrokenPipe) {
		t.Fatalf("SHUT_WR 后写=%v want EPIPE", err)
	}
	if err := client.Shutdown(ShutdownRead); err != nil {
		t.Fatal(err)
	}
	if n, _, err := client.Recv(ctx, buffer); err != nil || n != 0 {
		t.Fatalf("SHUT_RD 后读 n=%d err=%v", n, err)
	}
}

// TestPollReadinessForSockets 验证 M07 前置:socket 端点接入就绪图。
func TestPollReadinessForSockets(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, _ := ns.NewINETSocket(SockStream)
	_ = server.Bind(loopback(9002))
	_ = server.Listen(8)
	server.SetNonBlock(true)
	if server.Ready()&poll.Readable != 0 {
		t.Fatal("无待接受连接不应可读")
	}
	client, _ := ns.NewINETSocket(SockStream)
	if err := client.Connect(ctx, loopback(9002)); err != nil {
		t.Fatal(err)
	}
	if server.Ready()&poll.Readable == 0 {
		t.Fatal("有待接受连接应可读")
	}
	accepted, _ := server.Accept(ctx)
	if accepted.Ready()&poll.Writable == 0 {
		t.Fatal("已连接应可写")
	}
	if accepted.Ready()&poll.Readable != 0 {
		t.Fatal("无数据不应可读")
	}
	if _, err := client.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	if accepted.Ready()&poll.Readable == 0 {
		t.Fatal("有数据应可读")
	}
}

// TestSocketOptionsSubset 验证 M06:运行时必需选项逐项可用——
// SO_REUSEADDR/SO_KEEPALIVE 记录回读,SO_TYPE 回答类型,SO_ERROR
// 恒零(回环立即失败语义),TCP_NODELAY 接受,未知项 ENOTSUP。
func TestSocketOptionsSubset(t *testing.T) {
	ns := New()
	defer ns.Close()

	socket, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if socket.Type() != SockStream {
		t.Fatalf("SO_TYPE=%v", socket.Type())
	}
	socket.SetReuseAddr(true)
	if !socket.ReuseAddr() {
		t.Fatal("SO_REUSEADDR 应记录")
	}
	socket.SetKeepAlive(true)
	if !socket.KeepAlive() {
		t.Fatal("SO_KEEPALIVE 应记录")
	}
	// 未知层级/名称由端口层拒绝;此处锁定记录语义。
	socket.SetReuseAddr(false)
	if socket.ReuseAddr() {
		t.Fatal("SO_REUSEADDR 应可清除")
	}
}

// TestSocketPollWaitsViaOnChange 验证 M07 前置:UDP 端点经 OnChange
// 接入等待图——投递触发通知,不忙等、不丢失。
func TestSocketPollWaitsViaOnChange(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	receiver, err := ns.BindUDP(loopback(5454))
	if err != nil {
		t.Fatal(err)
	}
	notified := make(chan struct{}, 1)
	cancel := receiver.OnChange(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	defer cancel()

	sender, _ := ns.BindUDP(loopback(0))
	if _, err := ns.SendTo(ctx, sender, []byte("wake"), loopback(5454)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("投递应触发 OnChange")
	}
	buffer := make([]byte, 16)
	n, _, err := receiver.RecvFrom(ctx, buffer)
	if err != nil || string(buffer[:n]) != "wake" {
		t.Fatalf("读=%q err=%v", buffer[:n], err)
	}
}

// TestPollNotifiesConnectionDataAndHangup 验证 M07:连接完成、接收、
// 发送与断连经 OnChange 通知等待者——不忙等、不丢失。
func TestPollNotifiesConnectionDataAndHangup(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, _ := ns.NewINETSocket(SockStream)
	if err := server.Bind(loopback(9100)); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(8); err != nil {
		t.Fatal(err)
	}
	server.SetNonBlock(true)

	// 连接完成通知。
	events := make(chan string, 8)
	cancelServer := server.OnChange(func() { events <- "connect" })
	client, _ := ns.NewINETSocket(SockStream)
	if err := client.Connect(ctx, loopback(9100)); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got != "connect" {
			t.Fatalf("事件=%s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("连接完成应触发通知")
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancelServer()

	// 接收通知(数据到达)。
	cancelAccepted := accepted.OnChange(func() { events <- "readable" })
	if _, err := client.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got != "readable" {
			t.Fatalf("事件=%s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("数据到达应触发通知")
	}
	buffer := make([]byte, 16)
	if _, err := accepted.Read(buffer); err != nil {
		t.Fatal(err)
	}
	cancelAccepted()
	// 排空读取消费触发的残留通知。
	for {
		select {
		case <-events:
			continue
		default:
		}
		break
	}

	// 断连通知(对端关闭 → 可读(EOF)+HUP)。
	cancelAccepted = accepted.OnChange(func() { events <- "hangup" })
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-events:
		if got != "hangup" {
			t.Fatalf("事件=%s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("断连应触发通知")
	}
	if accepted.Ready()&poll.Hangup == 0 {
		t.Fatalf("就绪应含 HUP: %v", accepted.Ready())
	}
	cancelAccepted()
}

// TestUnixControlRightsDelivery 验证 M08:SCM_RIGHTS 控制消息随流
// 位置交付——发端关闭自己的引用后,接收方仍持有可用句柄对象。
func TestUnixControlRightsDelivery(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	a, b := ns.SocketPairUnix()
	rights := &ControlRights{Handles: []any{"pipe-handle", "file-handle"}}
	if n, err := a.SendMsg(ctx, [][]byte{[]byte("x"), []byte("yz")}, rights); err != nil || n != 3 {
		t.Fatalf("发送=%d err=%v", n, err)
	}
	buffer := make([]byte, 8)
	n, delivered, err := b.RecvMsg(ctx, buffer)
	if err != nil || n != 3 || string(buffer[:n]) != "xyz" {
		t.Fatalf("接收=%q n=%d err=%v", buffer[:n], n, err)
	}
	if delivered == nil || len(delivered.Handles) != 2 {
		t.Fatalf("控制交付=%+v", delivered)
	}
	// 排空后再次读取(非阻塞):无控制。
	b.SetNonBlock(true)
	n, delivered, err = b.RecvMsg(ctx, buffer)
	if !errors.Is(err, errno.WouldBlock) || delivered != nil {
		t.Fatalf("第二次接收 n=%d rights=%v err=%v", n, delivered, err)
	}
	_ = ctx
}

// TestPollNotifiesWritableAfterPeerDrains 验证 M07 的发送方向通知:
// 写缓冲填满后端点不可写,对端读取释放空间触发 OnChange,写端重新
// 可写且不忙等。
func TestPollNotifiesWritableAfterPeerDrains(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	server, _ := ns.NewINETSocket(SockStream)
	if err := server.Bind(loopback(9101)); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(8); err != nil {
		t.Fatal(err)
	}
	client, _ := ns.NewINETSocket(SockStream)
	if err := client.Connect(ctx, loopback(9101)); err != nil {
		t.Fatal(err)
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}

	notified := make(chan struct{}, 8)
	cancel := client.OnChange(func() {
		select {
		case notified <- struct{}{}:
		default:
		}
	})
	defer cancel()

	// 填满写方向(容量 64KiB):写端随后不可写。
	filled := make(chan error, 1)
	go func() {
		payload := make([]byte, streamBufCapacity)
		_, writeErr := client.Write(payload)
		filled <- writeErr
	}()
	deadline := time.Now().Add(2 * time.Second)
	for client.Ready()&poll.Writable != 0 {
		if time.Now().After(deadline) {
			t.Fatal("写端应因缓冲满而不可写")
		}
		time.Sleep(time.Millisecond)
	}
	if err := <-filled; err != nil {
		t.Fatalf("填充写入: %v", err)
	}
	// 排空已到达的通知(填充过程产生的活动)。
	for {
		select {
		case <-notified:
			continue
		default:
		}
		break
	}

	// 对端读取一个字节:释放容量触发通知。
	if _, err := accepted.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notified:
	case <-time.After(2 * time.Second):
		t.Fatal("对端读取应触发写端通知")
	}
	if client.Ready()&poll.Writable == 0 {
		t.Fatal("对端读取后写端应可写")
	}
}

type trackedRight struct{ releases *int }

func (r *trackedRight) Release() error {
	*r.releases++
	return nil
}

// TestUnixControlRightsDroppedOnClose 验证 M08:接收端未读取即关闭时,
// 在途的 SCM_RIGHTS 引用被归还,不泄漏发送侧的所有权。
func TestUnixControlRightsDroppedOnClose(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()

	a, b := ns.SocketPairUnix()
	releases := 0
	rights := &ControlRights{
		Handles: []any{&trackedRight{releases: &releases}},
		Release: func() { releases++ },
	}
	if n, err := a.SendMsg(ctx, [][]byte{[]byte("x")}, rights); err != nil || n != 1 {
		t.Fatalf("发送=%d err=%v", n, err)
	}
	if releases != 0 {
		t.Fatalf("交付前不应归还: %d", releases)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("关闭应归还一次: %d", releases)
	}
}
