// Package network 是宿主网络出口适配器(M09):只在部署层建立宿主
// TCP 连接;地址/端口/协议策略由 vm/net.EgressPolicy 在拨号前裁决。
package network

import (
	"context"
	"io"
	"net"
	"time"

	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// Dialer 使用宿主 net.Dialer 建立连接。Timeout 为 0 时由上下文控制。
type Dialer struct {
	Timeout time.Duration
}

func (d Dialer) Dial(ctx context.Context, network, address string) (io.ReadWriteCloser, error) {
	dialer := net.Dialer{Timeout: d.Timeout}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// DialPacket 建立已连接 UDP 宿主套接字(M10 DNS 复用)。
func (d Dialer) DialPacket(ctx context.Context, network, address string) (io.ReadWriteCloser, error) {
	dialer := net.Dialer{Timeout: d.Timeout}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// Listener 是宿主监听入口(M13),由 Dialer.Listen 创建。
type Listener struct {
	inner net.Listener
}

// Listen 在宿主地址上创建 TCP 监听。
func (d Dialer) Listen(ctx context.Context, network, address string) (vmnet.HostListener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	return &Listener{inner: listener}, nil
}

func (l *Listener) Accept() (io.ReadWriteCloser, error) { return l.inner.Accept() }

func (l *Listener) Close() error { return l.inner.Close() }

func (l *Listener) Addr() string { return l.inner.Addr().String() }
