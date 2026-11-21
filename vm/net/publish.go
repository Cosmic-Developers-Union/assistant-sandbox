package net

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// HostListener 是宿主监听入口(M13):只由显式 Publish 创建。
type HostListener interface {
	Accept() (io.ReadWriteCloser, error)
	Close() error
	Addr() string
}

// ListenerNetwork 是宿主监听能力;未装配时不能发布客体服务。
type ListenerNetwork interface {
	Listen(ctx context.Context, network, address string) (HostListener, error)
}

// Publication 是一条宿主端口→客体 TCP 监听映射。Revoke 关闭宿主
// 入口并终止现存外部连接,客体监听本身保持原状。
type Publication struct {
	ns     *Namespace
	guest  Addr
	host   HostListener
	mu     sync.Mutex
	closed bool
	conns  map[io.Closer]struct{}
	done   chan struct{}
}

// Publish 显式发布一个客体 TCP 监听到宿主端口(M13):只有调用此
// 方法配置的端口才存在宿主入口。host 形如 "127.0.0.1:0"。
func (n *Namespace) Publish(ctx context.Context, network ListenerNetwork, host string, guest Addr) (*Publication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network == nil {
		return nil, errno.Wrap(errno.NotSupported, "宿主监听能力未装配")
	}
	listener, ok := n.LookupTCPListener(guest)
	if !ok {
		return nil, errno.Wrap(errno.ConnRefused, "客体监听不存在")
	}
	hostListener, err := network.Listen(ctx, "tcp", host)
	if err != nil {
		return nil, errno.Wrap(errno.AddressNotAvail, "宿主端口不可用")
	}
	publication := &Publication{ns: n, guest: guest, host: hostListener, conns: map[io.Closer]struct{}{}, done: make(chan struct{})}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		_ = hostListener.Close()
		return nil, errno.BadHandle
	}
	if n.publications == nil {
		n.publications = map[*Publication]struct{}{}
	}
	n.publications[publication] = struct{}{}
	n.mu.Unlock()
	go publication.acceptLoop(listener)
	return publication, nil
}

// Addr 返回实际宿主监听地址(端口 0 时由宿主分配)。
func (p *Publication) Addr() string { return p.host.Addr() }

// Revoke 撤销发布:关闭宿主监听与现存外部连接;幂等。
func (p *Publication) Revoke() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.done)
	host := p.host
	conns := make([]io.Closer, 0, len(p.conns))
	for conn := range p.conns {
		conns = append(conns, conn)
	}
	p.conns = nil
	p.mu.Unlock()
	p.ns.mu.Lock()
	delete(p.ns.publications, p)
	p.ns.mu.Unlock()
	var closeErr error
	for _, conn := range conns {
		closeErr = errors.Join(closeErr, conn.Close())
	}
	return errors.Join(closeErr, host.Close())
}

func (p *Publication) acceptLoop(listener *TCPListener) {
	for {
		hostConn, err := p.host.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = hostConn.Close()
			return
		}
		p.conns[hostConn] = struct{}{}
		p.mu.Unlock()
		client, server, pairErr := newBudgetedStreamPair(p.ns, Addr{Domain: AFInet, IP: LoopbackIPv4}, p.guest)
		if pairErr != nil {
			p.mu.Lock()
			delete(p.conns, hostConn)
			p.mu.Unlock()
			_ = hostConn.Close()
			continue
		}
		listener.mu.Lock()
		if listener.closed || listener.ns.Closed() {
			listener.mu.Unlock()
			_ = hostConn.Close()
			_ = client.Close()
			_ = server.Close()
			continue
		}
		listener.queue = append(listener.queue, server)
		listener.base.notify()
		listener.mu.Unlock()
		go func() {
			pumpHost(hostConn, client, func() {
				p.mu.Lock()
				delete(p.conns, hostConn)
				p.mu.Unlock()
			})
		}()
	}
}
