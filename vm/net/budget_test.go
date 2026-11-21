package net

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestConnectionAndTrafficBudgets 验证 M12:连接数和累计流量受限,
// 关闭连接后额度归还。
func TestConnectionAndTrafficBudgets(t *testing.T) {
	ctx := context.Background()
	ns := New()
	defer ns.Close()
	ns.SetBudget(Budget{MaxConnections: 1, MaxBytes: 10})
	server, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Bind(Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5600}); err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(4); err != nil {
		t.Fatal(err)
	}
	client, err := ns.NewINETSocket(SockStream)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(ctx, Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5600}); err != nil {
		t.Fatal(err)
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := ns.NewINETSocket(SockStream)
	if err := second.Connect(ctx, Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5600}); !errors.Is(err, errno.Limit) {
		t.Fatalf("超连接预算 err=%v want EAGAIN", err)
	}
	if usage := ns.Usage(); usage.Connections != 1 {
		t.Fatalf("连接使用=%+v", usage)
	}
	// 流量:已写 6,再写 6 应返回部分完成 + ENOSPC。
	if n, err := client.Write([]byte("123456")); err != nil || n != 6 {
		t.Fatalf("首写=%d err=%v", n, err)
	}
	if n, err := client.Write([]byte("abcdef")); n != 0 || !errors.Is(err, errno.NoSpace) {
		t.Fatalf("超流量写=%d err=%v want 0/ENOSPC", n, err)
	}
	if usage := ns.Usage(); usage.Bytes != 6 {
		t.Fatalf("流量使用=%+v", usage)
	}
	// 关闭连接后连接额度归还。
	_ = accepted.Close()
	_ = client.Close()
	third, _ := ns.NewINETSocket(SockStream)
	if err := third.Connect(ctx, Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5600}); err != nil {
		t.Fatalf("归还后连接: %v", err)
	}
	if _, err := server.Accept(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestNamespaceCloseWakesBlockedRead 验证 M12:关闭命名空间会唤醒
// 阻塞网络操作,不遗留宿主 goroutine。
func TestNamespaceCloseWakesBlockedRead(t *testing.T) {
	ctx := context.Background()
	ns := New()
	server, _ := ns.NewINETSocket(SockStream)
	_ = server.Bind(Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5601})
	_ = server.Listen(1)
	client, _ := ns.NewINETSocket(SockStream)
	if err := client.Connect(ctx, Addr{Domain: AFInet, IP: LoopbackIPv4, Port: 5601}); err != nil {
		t.Fatal(err)
	}
	accepted, err := server.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := accepted.Read(make([]byte, 1))
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	ns.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("关闭命名空间未唤醒阻塞读")
	}
}
