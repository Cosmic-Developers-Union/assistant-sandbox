package net

import (
	"context"
	"errors"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// UnixListener 是 AF_UNIX 流式监听(M03):按客体路径登记;路径
// 在命名空间内唯一(重复 bind 报 EADDRINUSE,关闭时清理)。
type UnixListener struct {
	ns    *Namespace
	base  endpointBase
	local Addr

	mu     sync.Mutex
	queue  []*UnixConn
	closed bool
}

// BindUnixListener 登记路径监听;路径已被占用报 EADDRINUSE。
func (n *Namespace) BindUnixListener(path string) (*UnixListener, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, errno.BadHandle
	}
	if _, ok := n.unixPaths[path]; ok {
		return nil, errno.Wrap(errno.AddressInUse, "路径已被监听")
	}
	listener := &UnixListener{ns: n, local: Addr{Domain: AFUnix, Path: path}}
	n.unixPaths[path] = listener
	return listener, nil
}

// Listen 把端点标记为可接受连接。
func (l *UnixListener) Listen(backlog int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errno.BadHandle
	}
	_ = backlog
	return nil
}

// Accept 取出一个已建立的连接;阻塞模式等待,非阻塞立即返回。
func (l *UnixListener) Accept(ctx context.Context) (*UnixConn, error) {
	for {
		conn, err := l.AcceptNonBlock()
		if err == nil {
			return conn, nil
		}
		if !errors.Is(err, errno.WouldBlock) {
			return nil, err
		}
		if werr := l.base.waitBlocked(ctx, func() (bool, error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.closed {
				return false, errno.BadHandle
			}
			return len(l.queue) > 0, nil
		}); werr != nil {
			return nil, werr
		}
	}
}

// AcceptNonBlock 在无待接受连接时立即返回 EAGAIN。
func (l *UnixListener) AcceptNonBlock() (*UnixConn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errno.BadHandle
	}
	if len(l.queue) == 0 {
		return nil, errno.WouldBlock
	}
	conn := l.queue[0]
	l.queue = l.queue[1:]
	return conn, nil
}

// Close 关闭监听并清理路径登记;排队连接被重置。
func (l *UnixListener) Close() error {
	if !l.base.markClosed() {
		return errno.BadHandle
	}
	l.mu.Lock()
	pending := l.queue
	l.queue = nil
	l.closed = true
	ns := l.ns
	l.mu.Unlock()
	if ns != nil {
		ns.removeUnixListener(l)
	}
	for _, conn := range pending {
		conn.reset()
	}
	l.base.notify()
	return nil
}

func (n *Namespace) removeUnixListener(listener *UnixListener) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for path, registered := range n.unixPaths {
		if registered == listener {
			delete(n.unixPaths, path)
		}
	}
}

func (l *UnixListener) closeByNamespace() { _ = l.Close() }

// OnChange 订阅就绪变化(poll/epoll 等待图,M07)。
func (l *UnixListener) OnChange(fn func()) (cancel func()) { return l.base.OnChange(fn) }

// Ready 实现 poll 就绪:有待接受连接时可读。
func (l *UnixListener) Ready() poll.Readiness {
	l.mu.Lock()
	pending := len(l.queue) > 0
	l.mu.Unlock()
	if pending {
		return l.base.Ready(poll.Readable | poll.Writable)
	}
	return l.base.Ready(poll.Writable)
}

// Read/Write:监听套接字不可读写。
func (l *UnixListener) Read(p []byte) (int, error) {
	return 0, errno.NotConn
}

func (l *UnixListener) Write(p []byte) (int, error) {
	return 0, errno.NotConn
}

// ConnectUnix 发起 AF_UNIX 流式连接:路径无监听报 ECONNREFUSED;
// 成功返回客户端端连接。
func (n *Namespace) ConnectUnix(ctx context.Context, path string) (*UnixConn, error) {
	n.mu.Lock()
	listener, ok := n.unixPaths[path]
	closed := n.closed
	n.mu.Unlock()
	if closed || !ok {
		return nil, errno.Wrap(errno.ConnRefused, "路径无监听")
	}
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if listener.closed {
		return nil, errno.Wrap(errno.ConnRefused, "监听已关闭")
	}
	if len(listener.queue) >= backlogLimit {
		return nil, errno.Wrap(errno.ConnRefused, "监听队列已满")
	}
	clientAddr := Addr{Domain: AFUnix, Path: ""}
	client, server := newStreamPair(clientAddr, listener.local)
	listener.queue = append(listener.queue, server)
	listener.base.notify()
	return client, nil
}

// SocketpairUnix 建立 AF_UNIX 流式套接字对(M03):两端互联,与
// socketpair(2) 一致。
func SocketpairUnix() (*UnixConn, *UnixConn) {
	client, server := newStreamPair(
		Addr{Domain: AFUnix}, Addr{Domain: AFUnix})
	return client, server
}

// LocalAddr 返回绑定地址(路径)。
func (l *UnixListener) LocalAddr() (Addr, bool) { return l.local, true }

// PeerAddr:监听套接字无对端。
func (l *UnixListener) PeerAddr() (Addr, bool) { return Addr{}, false }

// Shutdown:监听套接字不支持半关闭。
func (l *UnixListener) Shutdown(ShutdownHow) error { return errno.NotConn }
