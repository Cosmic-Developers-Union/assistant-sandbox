// Package network 是宿主网络出口适配器(M09):只在部署层建立宿主
// TCP 连接;地址/端口/协议策略由 vm/net.EgressPolicy 在拨号前裁决。
package network

import (
	"context"
	"io"
	"net"
	"time"
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
