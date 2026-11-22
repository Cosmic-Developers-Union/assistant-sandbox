// Package net 实现 VM 内核网络栈(M 组)的内核核心部分:每 VM 网络命名
// 空间、回环地址配置与 socket 生命周期。回环连接全部在内核内完成,
// 不触达宿主网络;两台 VM 的命名空间相互独立,监听端口互不冲突。
//
// 本文件提供命名空间与 socket 端点的公共生命周期(创建/复制/关闭/
// 阻塞与非阻塞状态/poll 就绪);AF_UNIX(M03)、TCP(M04)与 UDP(M05)
// 的具体语义在各自文件实现。
package net

import (
	"context"
	"io"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// Domain 是 socket 域(AF_*),取值为 Linux asm-generic 常量。
type Domain uint64

// socket 域(Linux include/linux/socket.h)。
const (
	AFUnix    Domain = 1
	AFInet    Domain = 2
	AFInet6   Domain = 10
	AFNetlink Domain = 16
)

// SockType 是 socket 类型,取值为 Linux 常量。
type SockType uint64

// socket 类型。
const (
	SockStream    SockType = 1
	SockDatagram  SockType = 2
	SockSeqPacket SockType = 5
)

// ShutdownHow 是 shutdown 的方向。
type ShutdownHow uint64

// shutdown 方向(Linux SHUT_*)。
const (
	ShutdownRead  ShutdownHow = 0
	ShutdownWrite ShutdownHow = 1
	ShutdownAll   ShutdownHow = 2
)

// LoopbackIPv4 是本命名空间的回环地址;每个命名空间独立,回环只指向
// 自身(M01)。
var LoopbackIPv4 = netip.MustParseAddr("127.0.0.1")

// Addr 是 socket 地址(M01/M03/M04/M05):按域取 IP/Port 或 Path。
type Addr struct {
	Domain Domain
	IP     netip.Addr // AF_INET
	Port   uint16
	Path   string // AF_UNIX
}

// AddrIPv4FromBytes 由 4 字节构造 IPv4 地址(sockaddr 解码用)。
func AddrIPv4FromBytes(raw []byte) netip.Addr {
	if len(raw) != 4 {
		return netip.Addr{}
	}
	return netip.AddrFrom4([4]byte{raw[0], raw[1], raw[2], raw[3]})
}

// AddrIPv4Bytes 返回 IPv4 地址的 4 字节编码(sockaddr 编码用)。
func AddrIPv4Bytes(addr netip.Addr) []byte {
	encoded := addr.As4()
	return encoded[:]
}

// Namespace 是一个 VM 的网络命名空间(M01):地址配置、监听注册表与
// 连接撮合都在命名空间内完成。两台 VM 各有独立命名空间,端口互不冲突。
type Namespace struct {
	mu        sync.Mutex
	closed    bool
	tcpListen map[listenKey]*TCPListener
	udpBound  map[listenKey]*UDPEndpoint
	unixPaths map[string]*UnixListener

	// egress/policy 是宿主出口(M09):未配置时非回环连接拒绝。
	egress       Network
	policy       *EgressPolicy
	hostConns    map[io.Closer]struct{}
	conns        map[*streamConn]struct{}
	publications map[*Publication]struct{}
	budget       budgetState

	// nextCookie 供 accept/connect 观察用(测试)。
	nextCookie uint64

	wake wait.Queue
}

type listenKey struct {
	addr netip.Addr
	port uint16
}

// New 创建命名空间:回环地址即配置(M01)。
func New() *Namespace {
	return &Namespace{
		tcpListen: map[listenKey]*TCPListener{},
		udpBound:  map[listenKey]*UDPEndpoint{},
		unixPaths: map[string]*UnixListener{},
	}
}

// Loopback 返回命名空间的回环地址。
func (n *Namespace) Loopback() netip.Addr { return LoopbackIPv4 }

// Close 关闭命名空间:唤醒全部阻塞操作并拆除监听(随后操作报
// EBADF/连接重置)。随 VM 关闭调用(M12 的回收入口)。
func (n *Namespace) Close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	listeners := make([]*TCPListener, 0, len(n.tcpListen))
	for _, listener := range n.tcpListen {
		listeners = append(listeners, listener)
	}
	n.tcpListen = map[listenKey]*TCPListener{}
	udp := make([]*UDPEndpoint, 0, len(n.udpBound))
	for _, endpoint := range n.udpBound {
		udp = append(udp, endpoint)
	}
	n.udpBound = map[listenKey]*UDPEndpoint{}
	unix := make([]*UnixListener, 0, len(n.unixPaths))
	for _, listener := range n.unixPaths {
		unix = append(unix, listener)
	}
	n.unixPaths = map[string]*UnixListener{}
	hosts := make([]io.Closer, 0, len(n.hostConns))
	for host := range n.hostConns {
		hosts = append(hosts, host)
	}
	n.hostConns = nil
	conns := make([]*streamConn, 0, len(n.conns))
	for conn := range n.conns {
		conns = append(conns, conn)
	}
	n.conns = nil
	publications := make([]*Publication, 0, len(n.publications))
	for publication := range n.publications {
		publications = append(publications, publication)
	}
	n.publications = nil
	n.mu.Unlock()
	for _, publication := range publications {
		_ = publication.Revoke()
	}
	for _, listener := range listeners {
		listener.closeByNamespace()
	}
	for _, endpoint := range udp {
		endpoint.closeByNamespace()
	}
	for _, listener := range unix {
		listener.closeByNamespace()
	}
	for _, host := range hosts {
		_ = host.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	n.wake.WakeAll(wait.Data)
}

// Closed 报告命名空间是否已关闭。
func (n *Namespace) Closed() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.closed
}

// endpointBase 是各 socket 端点共享的生命周期核心(M02):非阻塞
// 状态、关闭语义、poll 就绪订阅与等待队列。
type endpointBase struct {
	wake     wait.Queue
	nonblock atomic.Bool
	watchers []*watchFunc
	mu       sync.Mutex
	closed   bool
}

type watchFunc struct{ fn func() }

func (b *endpointBase) blocked() bool { return b.nonblock.Load() }

// SetNonBlock 切换非阻塞状态(M02,fcntl F_SETFL O_NONBLOCK 生效)。
func (b *endpointBase) SetNonBlock(nonblock bool) {
	b.nonblock.Store(nonblock)
	b.notify()
}

func (b *endpointBase) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func (b *endpointBase) markClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.closed = true
	return true
}

func (b *endpointBase) Ready(extra poll.Readiness) poll.Readiness {
	var bits poll.Readiness
	if b.isClosed() {
		return bits | poll.Hangup
	}
	return bits | extra
}

func (b *endpointBase) OnChange(fn func()) (cancel func()) {
	w := &watchFunc{fn: fn}
	b.mu.Lock()
	b.watchers = append(b.watchers, w)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for index, registered := range b.watchers {
			if registered == w {
				b.watchers = append(b.watchers[:index], b.watchers[index+1:]...)
				return
			}
		}
	}
}

// notify 唤醒等待者并通知就绪订阅者;调用方不得持有与 wake 关联的锁。
func (b *endpointBase) notify() {
	b.wake.WakeAll(wait.Data)
	b.mu.Lock()
	watchers := make([]*watchFunc, len(b.watchers))
	copy(watchers, b.watchers)
	b.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}

// waitBlocked 在自身唤醒队列上等待条件成立;非阻塞模式不等待。
// ctx 结束返回等待队列的取消错误;条件闭包必须自行加锁。
func (b *endpointBase) waitBlocked(ctx context.Context, ready func() (bool, error)) error {
	return waitOn(ctx, &b.wake, b.blocked, ready)
}

// waitOn 在指定唤醒队列上等待条件成立:数据到达的唤醒队列与
// 非阻塞旗标所在地(端点/套接字)可以不同(流式连接按方向核
// 心唤醒)。ctx 结束返回取消错误;条件闭包必须自行加锁。
func waitOn(ctx context.Context, wake *wait.Queue, blocked func() bool, ready func() (bool, error)) error {
	for {
		// 非阻塞也必须先看当前就绪状态:数据已在队列时返回数据,
		// 只有确实不可用才 EWOULDBLOCK(M05/M10)。
		ok, err := ready()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if blocked() {
			return errno.WouldBlock
		}
		if _, err := wake.Wait(ctx, func() (bool, wait.Reason, error) {
			ok, err := ready()
			return ok || blocked(), wait.Data, err
		}); err != nil {
			return err
		}
	}
}
