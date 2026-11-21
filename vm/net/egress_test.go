package net

import (
	"context"
	"errors"
	"io"
	stdnet "net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type hostDialer struct{}

func (hostDialer) Dial(ctx context.Context, network, address string) (io.ReadWriteCloser, error) {
	var dialer stdnet.Dialer
	return dialer.DialContext(ctx, network, address)
}

func (hostDialer) DialPacket(ctx context.Context, network, address string) (io.ReadWriteCloser, error) {
	var dialer stdnet.Dialer
	return dialer.DialContext(ctx, network, address)
}

type staticResolver struct{ addresses []netip.Addr }

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, nil
}

func startEcho(t *testing.T) (int, *atomic.Int64) {
	t.Helper()
	listener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := &atomic.Int64{}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func(conn stdnet.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}(conn)
		}
	}()
	return listener.Addr().(*stdnet.TCPAddr).Port, accepted
}

func egressPolicy(port int) *EgressPolicy {
	return NewEgressPolicy(EgressRule{
		Protocol: ProtocolTCP,
		Prefix:   netip.MustParsePrefix("10.0.2.2/32"),
		PortMin:  uint16(port),
		PortMax:  uint16(port),
	})
}

// TestEgressAllowsOnlyPolicyTargets 验证 M09:策略允许的宿主服务可
// 访问,地址/端口/协议被拒绝时不触达宿主。
func TestEgressAllowsOnlyPolicyTargets(t *testing.T) {
	ctx := context.Background()
	port, accepted := startEcho(t)
	gateway := netip.MustParseAddr(DefaultGatewayIPv4)
	policy := egressPolicy(port)
	ns := New()
	ns.SetEgress(hostDialer{}, policy)
	defer ns.Close()

	client, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(ctx, Addr{Domain: AFInet, IP: gateway, Port: uint16(port)}); err != nil {
		t.Fatalf("允许的宿主服务: %v", err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	read, err := client.Read(buffer)
	if err != nil || string(buffer[:read]) != "ping" {
		t.Fatalf("回显=%q err=%v", buffer[:read], err)
	}
	if accepted.Load() != 1 {
		t.Fatalf("宿主 accept=%d want 1", accepted.Load())
	}

	deniedAddress, _ := ns.NewINETSocket(SockStream)
	if err := deniedAddress.Connect(ctx, Addr{Domain: AFInet, IP: netip.MustParseAddr("10.0.2.3"), Port: uint16(port)}); !errors.Is(err, errno.NetworkUnreachable) {
		t.Fatalf("拒绝地址 err=%v want ENETUNREACH", err)
	}
	deniedPort, _ := ns.NewINETSocket(SockStream)
	if err := deniedPort.Connect(ctx, Addr{Domain: AFInet, IP: gateway, Port: uint16(port + 1)}); !errors.Is(err, errno.NetworkUnreachable) {
		t.Fatalf("拒绝端口 err=%v want ENETUNREACH", err)
	}
	datagram, _ := ns.NewINETSocket(SockDatagram)
	if _, err := datagram.SendTo(ctx, []byte("dgram"), Addr{Domain: AFInet, IP: gateway, Port: uint16(port)}); !errors.Is(err, errno.NetworkUnreachable) {
		t.Fatalf("拒绝 UDP err=%v want ENETUNREACH", err)
	}
	if accepted.Load() != 1 {
		t.Fatalf("被拒绝目标触达宿主: accept=%d", accepted.Load())
	}
}

// TestEgressRequiresPolicyAndDNSRespectsIt 验证 M09:未配置策略时
// 非回环目标拒绝;DNS 解析结果逐地址经过策略,不能绕过。
func TestEgressRequiresPolicyAndDNSRespectsIt(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()
	socket, _ := ns.NewINETSocket(SockStream)
	if err := socket.Connect(ctx, Addr{Domain: AFInet, IP: netip.MustParseAddr(DefaultGatewayIPv4), Port: 80}); !errors.Is(err, errno.NetworkUnreachable) {
		t.Fatalf("未配置出口 err=%v want ENETUNREACH", err)
	}

	policy := egressPolicy(443)
	allowed, err := policy.Resolve(ctx, staticResolver{addresses: []netip.Addr{netip.MustParseAddr(DefaultGatewayIPv4)}}, ProtocolTCP, "allowed.example", 443)
	if err != nil || allowed.String() != DefaultGatewayIPv4 {
		t.Fatalf("允许的 DNS 答案=%v err=%v", allowed, err)
	}
	if _, err := policy.Resolve(ctx, staticResolver{addresses: []netip.Addr{netip.MustParseAddr("10.0.2.3")}}, ProtocolTCP, "denied.example", 443); !errors.Is(err, errno.NetworkUnreachable) {
		t.Fatalf("拒绝的 DNS 答案 err=%v want ENETUNREACH", err)
	}
	// 混合答案只返回被允许的地址。
	mixed, err := policy.Resolve(ctx, staticResolver{addresses: []netip.Addr{
		netip.MustParseAddr("10.0.2.3"), netip.MustParseAddr(DefaultGatewayIPv4),
	}}, ProtocolTCP, "mixed.example", 443)
	if err != nil || mixed.String() != DefaultGatewayIPv4 {
		t.Fatalf("混合 DNS 答案=%v err=%v", mixed, err)
	}
}

// TestEgressCloseClosesHostConnection 验证关闭命名空间会关闭已建立的
// 宿主连接,不遗留宿主 goroutine/socket。
func TestEgressCloseClosesHostConnection(t *testing.T) {
	ctx := context.Background()
	port, _ := startEcho(t)
	ns := New()
	ns.SetEgress(hostDialer{}, egressPolicy(port))
	client, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(ctx, Addr{Domain: AFInet, IP: netip.MustParseAddr(DefaultGatewayIPv4), Port: uint16(port)}); err != nil {
		t.Fatal(err)
	}
	ns.Close()
	// 关闭后 guest 端点要么报 EBADF,要么读到 EOF,不阻塞。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		buffer := make([]byte, 1)
		client.SetNonBlock(true)
		if _, err := client.Read(buffer); err != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("关闭命名空间后宿主连接仍可读")
}

// TestEgressAllowsPolicyUDP 验证 M09/M10:策略允许的宿主 UDP 服务
// 可收发数据报,回复按 guest 网关来源交付。
func TestEgressAllowsPolicyUDP(t *testing.T) {
	ctx := context.Background()
	packet, err := stdnet.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = packet.Close() }()
	go func() {
		buffer := make([]byte, 64)
		for {
			n, from, err := packet.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = packet.WriteTo(buffer[:n], from)
		}
	}()
	port := uint16(packet.LocalAddr().(*stdnet.UDPAddr).Port) //nolint:gosec // 内核分配的端口在 uint16 范围
	gateway := netip.MustParseAddr(DefaultGatewayIPv4)
	policy := NewEgressPolicy(EgressRule{
		Protocol: ProtocolUDP,
		Prefix:   netip.MustParsePrefix("10.0.2.2/32"),
		PortMin:  port,
		PortMax:  port,
	})
	ns := New()
	ns.SetEgress(hostDialer{}, policy)
	defer ns.Close()
	socket, err := ns.NewINETSocket(SockDatagram)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := socket.SendTo(ctx, []byte("dns-like"), Addr{Domain: AFInet, IP: gateway, Port: port}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64)
	deadline := time.Now().Add(2 * time.Second)
	for {
		read, from, err := socket.RecvFrom(ctx, buffer)
		if err == nil {
			if string(buffer[:read]) != "dns-like" || from.IP != gateway || from.Port != port {
				t.Fatalf("UDP 回复=%q from=%+v", buffer[:read], from)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("UDP 回复超时: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestNonBlockingReceiveReturnsQueuedData 验证 M05/M10 回归:非阻塞
// 套接字已有报文时先返回数据,只有队列为空才 EWOULDBLOCK。
func TestNonBlockingReceiveReturnsQueuedData(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()
	receiver, err := ns.NewINETSocket(SockDatagram)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.Bind(Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5455}); err != nil {
		t.Fatal(err)
	}
	receiver.SetNonBlock(true)
	sender, _ := ns.NewINETSocket(SockDatagram)
	if _, err := sender.SendTo(ctx, []byte("queued"), Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5455}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16)
	read, _, err := receiver.RecvFrom(ctx, buffer)
	if err != nil || string(buffer[:read]) != "queued" {
		t.Fatalf("非阻塞已有报文: %q err=%v", buffer[:read], err)
	}
	if _, _, err := receiver.RecvFrom(ctx, buffer); !errors.Is(err, errno.WouldBlock) {
		t.Fatalf("队列空后 err=%v want EWOULDBLOCK", err)
	}
}
