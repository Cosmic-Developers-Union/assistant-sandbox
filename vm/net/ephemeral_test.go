package net

import (
	"context"
	"net/netip"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// TestBindPortZeroReportsAssignedAddress 验证 bind(..., 0) 后
// getsockname 返回内核分配的临时端口;UDP echo/asyncio 测试依赖
// 该地址回连。
func TestBindPortZeroReportsAssignedAddress(t *testing.T) {
	namespace := New()
	loopback := Addr{Domain: AFInet, IP: LoopbackIPv4}

	udp, err := namespace.NewINETSocket(SockDatagram)
	if err != nil {
		t.Fatal(err)
	}
	if udp.Ready()&poll.Writable == 0 {
		t.Fatal("未绑定数据报套接字应报告可写")
	}
	if err := udp.Bind(loopback); err != nil {
		t.Fatal(err)
	}
	udpAddress, ok := udp.LocalAddr()
	if !ok || udpAddress.Port == 0 {
		t.Fatalf("UDP bind(0) 地址=%+v", udpAddress)
	}
	if udp.Ready()&poll.Writable == 0 {
		t.Fatal("绑定后的数据报套接字应报告可写")
	}

	tcp, err := namespace.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := tcp.Bind(loopback); err != nil {
		t.Fatal(err)
	}
	if err := tcp.Listen(0); err != nil {
		t.Fatal(err)
	}
	tcpAddress, ok := tcp.LocalAddr()
	if !ok || tcpAddress.Port == 0 {
		t.Fatalf("TCP bind(0) 地址=%+v", tcpAddress)
	}
	if tcpAddress.Port == udpAddress.Port {
		t.Fatalf("UDP/TCP 临时端口冲突: %d", tcpAddress.Port)
	}
}

// TestWildcardBindMatchesLoopback 验证 bind(..., 0.0.0.0) 的监听/
// 接收端点可被发往 127.0.0.1 的连接与报文命中。
func TestWildcardBindMatchesLoopback(t *testing.T) {
	namespace := New()
	wildcard := Addr{Domain: AFInet, IP: netip.IPv4Unspecified()}

	tcp, err := namespace.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := tcp.Bind(wildcard); err != nil {
		t.Fatal(err)
	}
	if err := tcp.Listen(0); err != nil {
		t.Fatal(err)
	}
	tcpAddr, _ := tcp.LocalAddr()
	if _, ok := namespace.LookupTCPListener(Addr{Domain: AFInet, IP: LoopbackIPv4, Port: tcpAddr.Port}); !ok {
		t.Fatalf("通配 TCP 监听未命中回环端口 %d", tcpAddr.Port)
	}

	receiver, err := namespace.NewINETSocket(SockDatagram)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Bind(wildcard); err != nil {
		t.Fatal(err)
	}
	udpAddr, _ := receiver.LocalAddr()
	sender, err := namespace.BindUDP(Addr{Domain: AFInet, IP: LoopbackIPv4})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := namespace.SendTo(ctx, sender, []byte("x"),
		Addr{Domain: AFInet, IP: LoopbackIPv4, Port: udpAddr.Port}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	loaded, _, err := receiver.RecvFrom(ctx, buffer)
	if err != nil || loaded != 1 || buffer[0] != 'x' {
		t.Fatalf("通配 UDP 接收 loaded=%d data=%q err=%v", loaded, buffer[:loaded], err)
	}
}
