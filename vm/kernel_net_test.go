package vm

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// TestKernelNetworkNamespacesArePerVM 验证 M01:每台 VM 独立的网络
// 命名空间——同一端口在两台 VM 可分别绑定,互不可见;内核关闭时
// 命名空间一并关闭(阻塞操作放行)。
func TestKernelNetworkNamespacesArePerVM(t *testing.T) {
	ctx := context.Background()
	first, err := New(Services{
		Set:      capability.Set{FD: fd.New()},
		Programs: idleRegistry(t),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Services{
		Set:      capability.Set{FD: fd.New()},
		Programs: idleRegistry(t),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(ctx) }()

	firstNS := (procControl{k: first, self: first.init}).NetNamespace()
	secondNS := (procControl{k: second, self: second.init}).NetNamespace()
	if firstNS == nil || secondNS == nil {
		t.Fatal("内核应装配网络命名空间")
	}
	if firstNS == secondNS {
		t.Fatal("两台 VM 的命名空间应相互独立")
	}
	listener, err := firstNS.BindTCPListener(net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 7000}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Listen(4); err != nil {
		t.Fatal(err)
	}
	// 第二 VM 同端口独立绑定。
	other, err := secondNS.BindTCPListener(net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 7000}, false)
	if err != nil {
		t.Fatalf("第二 VM 同端口应可绑定: %v", err)
	}
	if err := other.Listen(4); err != nil {
		t.Fatal(err)
	}
	// 第一 VM 回环连接只到第一 VM 的监听。
	if _, err := firstNS.ConnectTCP(ctx, net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4}, net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 7000}); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.AcceptNonBlock(); err != nil {
		t.Fatalf("第一 VM 监听应收到本 VM 连接: %v", err)
	}

	// 内核关闭后命名空间关闭,后续绑定拒绝。
	if err := first.Close(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("内核关闭: %v", err)
	}
	if !firstNS.Closed() {
		t.Fatal("内核关闭后命名空间应关闭")
	}
	if _, err := firstNS.BindTCPListener(net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4, Port: 7001}, false); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后绑定=%v want EBADF", err)
	}
}
