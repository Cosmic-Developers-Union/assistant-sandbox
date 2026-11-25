package net

import (
	"context"
	"io"
	"net/netip"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// udpQueueLimit 是 UDP 端点接收队列的报文数上限;队满后新报文按
// UDP 尽力而为语义丢弃(Linux:接收缓冲满丢包)。
const udpQueueLimit = 128

// udpDatagramLimit 是单报文长度上限(IPv4 UDP 载荷上限 65507)。
const udpDatagramLimit = 65507

// udpPacket 是队列中的一个报文:边界与来源地址随报文保留(M05)。
type udpPacket struct {
	data []byte
	from Addr
}

// UDPEndpoint 是 UDP 端点(M05):绑定地址、报文队列与尽力而为的
// 回环投递。
type UDPEndpoint struct {
	ns   *Namespace
	base endpointBase

	mu    sync.Mutex
	local Addr
	queue []udpPacket
	drain bool

	// hosts 是宿主 UDP 出口的已连接数据报(按目标地址:端口)。
	hosts map[netip.AddrPort]io.ReadWriteCloser
}

// BindUDP 在命名空间登记 UDP 端点;端口已占用报 EADDRINUSE。
// port=0 时由命名空间分配临时端口(首包发送前显式绑定语义)。
func (n *Namespace) BindUDP(addr Addr) (*UDPEndpoint, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, errno.BadHandle
	}
	if addr.Port == 0 {
		n.nextCookie++
		addr.Port = uint16(32768 + n.nextCookie%16384)
	}
	key := listenKey{addr: addr.IP, port: addr.Port}
	if _, ok := n.udpBound[key]; ok {
		return nil, errno.Wrap(errno.AddressInUse, "端口已占用")
	}
	endpoint := &UDPEndpoint{ns: n, local: addr}
	n.udpBound[key] = endpoint
	return endpoint, nil
}

// SendTo 向回环目标投递报文:目标端点不存在或队列满时静默丢弃
// (UDP 尽力而为);报文过长报 EMSGSIZE。from 为空时自动绑定
// 临时端口。
func (n *Namespace) SendTo(ctx context.Context, from *UDPEndpoint, data []byte, to Addr) (int, error) {
	if len(data) > udpDatagramLimit {
		return 0, errno.MessageSize
	}
	// 非回环 UDP 需要宿主出口;策略默认拒绝,允许后经 PacketNetwork
	// 拨号宿主(未实现数据报能力时 ENOTSUP)。DNS(M10)复用此路径。
	if to.IP.IsValid() && to.IP != LoopbackIPv4 {
		return n.sendHostUDP(ctx, from, data, to)
	}
	if err := n.chargeBytes(len(data)); err != nil {
		return 0, err
	}
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return 0, errno.BadHandle
	}
	target, ok := n.udpBound[listenKey{addr: to.IP, port: to.Port}]
	if !ok && to.IP == LoopbackIPv4 {
		target, ok = n.udpBound[listenKey{addr: netip.IPv4Unspecified(), port: to.Port}]
	}
	n.mu.Unlock()
	if !ok || target.drain {
		return len(data), nil // 尽力而为:无接收方即丢弃,发送仍成功
	}
	source := to
	source.Port = 0
	if from != nil {
		source = from.local
	}
	packet := udpPacket{data: append([]byte(nil), data...), from: source}
	target.mu.Lock()
	sent := false
	if len(target.queue) < udpQueueLimit {
		target.queue = append(target.queue, packet)
		sent = true
	}
	target.mu.Unlock()
	if sent {
		target.base.notify()
	}
	return len(data), nil
}

// RecvFrom 取出一个报文:报文边界保留;缓冲小于报文时截断并丢弃
// 剩余(数据报语义,MSG_TRUNC 经 recvmsg 上报);无报文时阻塞或
// 非阻塞返回 EAGAIN。
func (e *UDPEndpoint) RecvFrom(ctx context.Context, buffer []byte) (int, Addr, error) {
	if err := e.base.waitBlocked(ctx, func() (bool, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.queue) > 0, nil
	}); err != nil {
		return 0, Addr{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.queue) == 0 {
		return 0, Addr{}, errno.WouldBlock
	}
	packet := e.queue[0]
	e.queue = e.queue[1:]
	if len(buffer) >= len(packet.data) {
		copy(buffer, packet.data)
		return len(packet.data), packet.from, nil
	}
	copy(buffer, packet.data[:len(buffer)])
	return len(buffer), packet.from, nil
}

// Read 实现 fd.Stream:与 RecvFrom 等价但丢弃来源地址。
func (e *UDPEndpoint) Read(p []byte) (int, error) {
	n, _, err := e.RecvFrom(context.Background(), p)
	return n, err
}

// Write 实现 fd.Stream:发送到已连接目标在本子集未定义,报 ENOTSUP
// (sendto 才是投递入口)。
func (e *UDPEndpoint) Write(p []byte) (int, error) {
	return 0, errno.NotSupported
}

// LocalAddr 返回绑定地址。
func (e *UDPEndpoint) LocalAddr() (Addr, bool) { return e.local, true }

// PeerAddr:无连接套接字无对端。
func (e *UDPEndpoint) PeerAddr() (Addr, bool) { return Addr{}, false }

// Shutdown:无连接套接字不支持半关闭。
func (e *UDPEndpoint) Shutdown(ShutdownHow) error { return errno.NotConn }

// Close 关闭端点并从命名空间摘除;阻塞接收被唤醒报 EBADF。
func (e *UDPEndpoint) Close() error {
	if !e.base.markClosed() {
		return errno.BadHandle
	}
	hosts := e.closeHosts()
	for _, host := range hosts {
		_ = host.Close()
	}
	if e.ns != nil {
		e.ns.removeUDPEndpoint(e)
	}
	e.base.notify()
	return nil
}

func (n *Namespace) removeUDPEndpoint(endpoint *UDPEndpoint) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, registered := range n.udpBound {
		if registered == endpoint {
			delete(n.udpBound, key)
		}
	}
}

func (e *UDPEndpoint) closeByNamespace() {
	hosts := e.closeHosts()
	for _, host := range hosts {
		_ = host.Close()
	}
	e.base.markClosed()
	e.base.notify()
}

// closeHosts 清空并返回宿主数据报连接;调用方在锁外关闭。
func (e *UDPEndpoint) closeHosts() []io.ReadWriteCloser {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.drain = true
	e.queue = nil
	hosts := make([]io.ReadWriteCloser, 0, len(e.hosts))
	for _, host := range e.hosts {
		hosts = append(hosts, host)
	}
	e.hosts = nil
	return hosts
}

// sendHostUDP 把报文写到宿主数据报连接,并把回复按网关来源入队。
func (n *Namespace) sendHostUDP(ctx context.Context, endpoint *UDPEndpoint, data []byte, to Addr) (int, error) {
	if endpoint == nil {
		return 0, errno.Wrap(errno.NetworkUnreachable, "没有本地 UDP 端点")
	}
	n.mu.Lock()
	network := n.egress
	policy := n.policy
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return 0, errno.BadHandle
	}
	if network == nil || policy == nil {
		return 0, errno.Wrap(errno.NetworkUnreachable, "宿主 UDP 出口未配置")
	}
	target, port, allowed := policy.Target(ProtocolUDP, to)
	if !allowed {
		return 0, errno.Wrap(errno.NetworkUnreachable, "UDP 出口策略拒绝目标")
	}
	if err := n.chargeBytes(len(data)); err != nil {
		return 0, err
	}
	packets, ok := network.(PacketNetwork)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "宿主 UDP 出口未实现")
	}
	key := netip.AddrPortFrom(to.IP, to.Port)
	endpoint.mu.Lock()
	if endpoint.drain || endpoint.base.isClosed() {
		endpoint.mu.Unlock()
		return 0, errno.BadHandle
	}
	host := endpoint.hosts[key]
	if host == nil {
		if endpoint.hosts == nil {
			endpoint.hosts = map[netip.AddrPort]io.ReadWriteCloser{}
		}
		endpoint.mu.Unlock()
		connection, err := packets.DialPacket(ctx, "udp", joinHostPort(target.String(), port))
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, errno.Wrap(errno.NetworkUnreachable, "宿主 UDP 服务不可达")
		}
		endpoint.mu.Lock()
		if endpoint.drain || endpoint.base.isClosed() {
			endpoint.mu.Unlock()
			_ = connection.Close()
			return 0, errno.BadHandle
		}
		if existing := endpoint.hosts[key]; existing != nil {
			endpoint.mu.Unlock()
			_ = connection.Close()
			host = existing
		} else {
			endpoint.hosts[key] = connection
			host = connection
			endpoint.mu.Unlock()
			go readHostUDP(endpoint, connection, to)
		}
	} else {
		endpoint.mu.Unlock()
	}
	written, err := host.Write(data)
	if err != nil {
		return 0, errno.Wrap(errno.NetworkUnreachable, "宿主 UDP 写入失败")
	}
	return written, nil
}

// readHostUDP 把宿主数据报回复按原 guest 目标地址投递到端点队列。
func readHostUDP(endpoint *UDPEndpoint, host io.ReadWriteCloser, from Addr) {
	buffer := make([]byte, udpDatagramLimit)
	for {
		read, err := host.Read(buffer)
		if err != nil {
			return
		}
		if read == 0 {
			continue
		}
		endpoint.mu.Lock()
		if endpoint.drain || len(endpoint.queue) >= udpQueueLimit {
			endpoint.mu.Unlock()
			continue
		}
		endpoint.queue = append(endpoint.queue, udpPacket{
			data: append([]byte(nil), buffer[:read]...),
			from: from,
		})
		endpoint.mu.Unlock()
		endpoint.base.notify()
	}
}

// OnChange 订阅就绪变化(poll/epoll 等待图,M07)。
func (e *UDPEndpoint) OnChange(fn func()) (cancel func()) { return e.base.OnChange(fn) }

// Ready 实现 poll 就绪:有报文时可读;数据报发送不排队,始终可写。
func (e *UDPEndpoint) Ready() poll.Readiness {
	e.mu.Lock()
	pending := len(e.queue) > 0
	e.mu.Unlock()
	bits := poll.Writable
	if pending {
		bits |= poll.Readable
	}
	return e.base.Ready(bits)
}
