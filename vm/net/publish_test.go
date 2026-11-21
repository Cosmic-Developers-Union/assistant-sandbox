package net_test

import (
	"context"
	stdnet "net"
	"testing"
	"time"

	platformnet "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/network"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// TestPublishExplicitGuestPort 验证 M13:只有显式发布的宿主端口可访问
// 客体监听;撤销后宿主入口关闭而客体监听保持。
func TestPublishExplicitGuestPort(t *testing.T) {
	ctx := context.Background()
	ns := vmnet.New()
	defer ns.Close()
	server, err := ns.NewINETSocket(vmnet.SockStream)
	if err != nil {
		t.Fatal(err)
	}
	guestAddr := vmnet.Addr{Domain: vmnet.AFInet, IP: vmnet.LoopbackIPv4, Port: 7000}
	if err := server.Bind(guestAddr); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(4); err != nil {
		t.Fatal(err)
	}
	publication, err := ns.Publish(ctx, platformnet.Dialer{}, "127.0.0.1:0", guestAddr)
	if err != nil {
		t.Fatal(err)
	}
	host, err := stdnet.Dial("tcp", publication.Addr())
	if err != nil {
		t.Fatalf("发布端口不可访问: %v", err)
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8)
	read, err := accepted.Read(buffer)
	if err != nil || string(buffer[:read]) != "hello" {
		t.Fatalf("客体读到=%q err=%v", buffer[:read], err)
	}
	if _, err := accepted.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	read, err = host.Read(buffer)
	if err != nil || string(buffer[:read]) != "world" {
		t.Fatalf("宿主读到=%q err=%v", buffer[:read], err)
	}
	_ = host.Close()
	_ = accepted.Close()
	if err := publication.Revoke(); err != nil {
		t.Fatal(err)
	}
	// 撤销后宿主入口关闭。
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := stdnet.Dial("tcp", publication.Addr())
		if err != nil {
			break
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("撤销后宿主入口仍可连接")
		}
		time.Sleep(time.Millisecond)
	}
	// 客体监听本身仍在回环可连接。
	guestClient, err := ns.NewINETSocket(vmnet.SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := guestClient.Connect(ctx, guestAddr); err != nil {
		t.Fatalf("客体监听应保持: %v", err)
	}
	if _, err := server.Accept(ctx); err != nil {
		t.Fatal(err)
	}
}
