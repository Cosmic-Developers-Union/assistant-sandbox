package elf

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vfd "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// TestUnixSocketPairSendAndRecvFrom 验证 AF_UNIX 套接字经 sendto/
// recvfrom 路径收发:musl 的 send(2) 以 sendto(fd, ..., NULL, 0)
// 下发,不能报 ENOTSOCK。asyncio 自管道依赖该路径唤醒选择器。
func TestUnixSocketPairSendAndRecvFrom(t *testing.T) {
	ctx := context.Background()
	sockets := newSocketHarness(t, 96)
	first, second, err := sockets.SocketPair(uint64(net.AFUnix), uint64(net.SockStream), 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if written, err := sockets.SendTo(ctx, second, []byte("x"), nil); err != nil || written != 1 {
		t.Fatalf("sendto: written=%d err=%v", written, err)
	}
	buffer := make([]byte, 4)
	loaded, from, err := sockets.RecvFrom(ctx, first, buffer)
	if err != nil || loaded != 1 || string(buffer[:loaded]) != "x" {
		t.Fatalf("recvfrom: loaded=%d data=%q err=%v", loaded, buffer[:loaded], err)
	}
	if from.Domain != net.AFUnix {
		t.Fatalf("来源域=%d want AF_UNIX", from.Domain)
	}
	if _, err := sockets.SendTo(ctx, second, []byte("x"),
		&net.Addr{Domain: net.AFUnix}); !errors.Is(err, errno.IsConn) {
		t.Fatalf("已连接流套接字带目标地址 sendto err=%v want EISCONN", err)
	}
}

// TestSocketOptionsAcceptRuntimeBuffers 验证 asyncio/socketserver 在
// 启动路径设置的 SO_SNDBUF/SO_RCVBUF 与 AF_UNIX 上的 SO_REUSEADDR
// 不会被误报 ENOTSUP/ENOTSOCK。
func TestSocketOptionsAcceptRuntimeBuffers(t *testing.T) {
	sockets := newSocketHarness(t, 97)
	unix, _, err := sockets.SocketPair(uint64(net.AFUnix), uint64(net.SockStream), 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []uint64{soSndBuf, soRcvBuf, soReuseAddr} {
		if err := sockets.SetOption(unix, solSocket, name, 1024); err != nil {
			t.Fatalf("AF_UNIX setsockopt %d: %v", name, err)
		}
	}
	tcp, err := sockets.Socket(uint64(net.AFInet), uint64(net.SockStream), 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sockets.SetOption(tcp, solSocket, soSndBuf, 1024); err != nil {
		t.Fatalf("AF_INET SO_SNDBUF: %v", err)
	}
	if value, err := sockets.GetOption(tcp, solSocket, soSndBuf); err != nil || value == 0 {
		t.Fatalf("SO_SNDBUF 回读=%d err=%v", value, err)
	}
}

func newSocketHarness(t *testing.T, pid uint64) processSockets {
	t.Helper()
	table := vfd.New()
	table.Attach(pid, vfd.Streams{})
	return processSockets{
		ns:        net.New(),
		installer: table,
		descriptors: func(ctx context.Context, fd uint64) (any, error) {
			return descriptorsHandle{table: table, pid: pid}.Get(ctx, fd)
		},
		pid: pid,
	}
}
