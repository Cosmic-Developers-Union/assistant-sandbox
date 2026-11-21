package net

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// socketState 是 socket 的生命周期状态(M02)。
type socketState uint8

const (
	stateUnbound socketState = iota
	stateBound
	stateListening
	stateConnected
)

// INETSocket 是 AF_INET socket 的完整生命周期视图(M02/M04/M05):
// 创建→绑定→监听/连接→收发→关闭。一个对象覆盖全部状态转换,fd 表
// 不需要中途替换句柄。
type INETSocket struct {
	ns   *Namespace
	base endpointBase

	typ   SockType
	mu    sync.Mutex
	state socketState
	local Addr

	listener *TCPListener // TCP 监听形态
	conn     *TCPConn     // TCP 连接形态
	udp      *UDPEndpoint // UDP 形态

	mu2       sync.Mutex
	reuse     bool // SO_REUSEADDR 记录(M06)
	keepAlive bool // SO_KEEPALIVE 记录(M06)
}

// SetReuseAddr 记录 SO_REUSEADDR(M06):本模型无 TIME_WAIT,重绑定
// 仍报 EADDRINUSE,选项被接受并回读。
func (s *INETSocket) SetReuseAddr(on bool) {
	s.mu2.Lock()
	s.reuse = on
	s.mu2.Unlock()
}

// ReuseAddr 报告 SO_REUSEADDR 记录值。
func (s *INETSocket) ReuseAddr() bool {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	return s.reuse
}

// SetKeepAlive 记录 SO_KEEPALIVE(M06):回环撮合无探测语义,选项被
// 接受并回读。
func (s *INETSocket) SetKeepAlive(on bool) {
	s.mu2.Lock()
	s.keepAlive = on
	s.mu2.Unlock()
}

// KeepAlive 报告 SO_KEEPALIVE 记录值。
func (s *INETSocket) KeepAlive() bool {
	s.mu2.Lock()
	defer s.mu2.Unlock()
	return s.keepAlive
}

// SetNonBlock 切换非阻塞(M02,fcntl F_SETFL 与 SOCK_NONBLOCK 生效)。
func (s *INETSocket) SetNonBlock(on bool) { s.base.SetNonBlock(on) }

// watchConn 订阅连接两个方向核心的就绪变化(数据到达/对端关闭经
// 共享核心通知到本 socket 的 poll/epoll 等待图,M07)。
func (s *INETSocket) watchConn(conn *TCPConn) {
	conn.rx.onChange(s.base.notify)
	conn.tx.onChange(s.base.notify)
}

// OnChange 订阅就绪变化(poll/epoll 等待图,M07)。
func (s *INETSocket) OnChange(fn func()) (cancel func()) { return s.base.OnChange(fn) }

// NonBlock 报告非阻塞状态。
func (s *INETSocket) NonBlock() bool { return s.base.blocked() }

// Type 返回 socket 类型(SO_TYPE 用)。
func (s *INETSocket) Type() SockType { return s.typ }

// StatusFlags 报告 fd 状态标志(O_RDWR + O_NONBLOCK;fcntl F_GETFL 用)。
func (s *INETSocket) StatusFlags() (uint32, error) {
	flags := uint32(2) // O_RDWR
	if s.base.blocked() {
		flags |= 0o4000 // O_NONBLOCK
	}
	return flags, nil
}

// SetStatusFlags 切换 O_NONBLOCK(其余访问位忽略,与常规句柄一致)。
func (s *INETSocket) SetStatusFlags(flags uint32) error {
	s.base.SetNonBlock(flags&0o4000 != 0)
	return nil
}

// NewINETSocket 创建未绑定 socket(M02);域/类型组合不支持时报错。
func (n *Namespace) NewINETSocket(typ SockType) (*INETSocket, error) {
	switch typ {
	case SockStream, SockDatagram:
		return &INETSocket{ns: n, typ: typ, state: stateUnbound, local: Addr{Domain: AFInet}}, nil
	default:
		return nil, errno.NotSupported
	}
}

// Bind 绑定地址(M04/M05):端口 0 由命名空间分配临时端口;TCP
// 端口占用报 EADDRINUSE(SO_REUSEADDR 不解除,本模型无 TIME_WAIT)。
func (s *INETSocket) Bind(addr Addr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateUnbound {
		return errno.Invalid
	}
	if s.typ == SockStream {
		listener, err := s.ns.BindTCPListener(addr, s.ReuseAddr())
		if err != nil {
			return err
		}
		if err := listener.Listen(0); err != nil {
			return err
		}
		listener.OnChange(s.base.notify)
		s.listener = listener
	} else {
		endpoint, err := s.ns.BindUDP(addr)
		if err != nil {
			return err
		}
		endpoint.OnChange(s.base.notify)
		s.udp = endpoint
	}
	s.local = addr
	s.state = stateBound
	return nil
}

// Listen 进入监听(M04)。
func (s *INETSocket) Listen(backlog int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.typ != SockStream || s.listener == nil {
		// 未绑定先 listen:Linux 允许(隐式绑定通配地址);本子集
		// 要求先绑定,未绑定报 EINVAL。
		return errno.Invalid
	}
	if s.state == stateListening {
		return nil
	}
	if s.state != stateBound {
		return errno.Invalid
	}
	if err := s.listener.Listen(backlog); err != nil {
		return err
	}
	s.state = stateListening
	return nil
}

// Accept 接受连接(M04):返回新的已连接 socket。非阻塞语义由
// socket 的 O_NONBLOCK 决定。
func (s *INETSocket) Accept(ctx context.Context) (*INETSocket, error) {
	s.mu.Lock()
	listener := s.listener
	listening := s.state == stateListening
	nonblock := s.base.blocked()
	s.mu.Unlock()
	if !listening || listener == nil {
		return nil, errno.Invalid
	}
	var conn *TCPConn
	var err error
	if nonblock {
		conn, err = listener.AcceptNonBlock()
	} else {
		conn, err = listener.Accept(ctx)
	}
	if err != nil {
		return nil, err
	}
	accepted := &INETSocket{
		ns:    s.ns,
		typ:   s.typ,
		state: stateConnected,
		local: conn.local,
		conn:  conn,
	}
	accepted.conn.blockCheck = accepted.base.blocked
	accepted.watchConn(accepted.conn)
	if nonblock {
		// accept4(SOCK_NONBLOCK) 语义:新 socket 继承非阻塞。
		accepted.base.SetNonBlock(true)
	}
	return accepted, nil
}

// Adopt 把已接受的连接装配为 socket(accept 流程的装配侧,M07 测试
// 与执行器共用)。
func (s *INETSocket) Adopt(conn *TCPConn) {
	s.mu.Lock()
	conn.blockCheck = s.base.blocked
	s.watchConn(conn)
	s.conn = conn
	s.local = conn.local
	s.state = stateConnected
	s.mu.Unlock()
}

// Connect 连接到回环地址(M04):TCP 建立连接;UDP 记录默认目标
// (无连接收发子集不支持 connect 形态,报 ENOTSUP)。
func (s *INETSocket) Connect(ctx context.Context, addr Addr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.typ == SockStream && s.state == stateUnbound:
		local := s.ns.EphemeralIPv4()
		conn, err := s.ns.ConnectTCP(ctx, local, addr)
		if err != nil {
			return err
		}
		conn.blockCheck = s.base.blocked
		s.watchConn(conn)
		s.conn = conn
		s.local = local
		s.state = stateConnected
		return nil
	case s.typ == SockStream && s.state == stateBound:
		conn, err := s.ns.ConnectTCP(ctx, s.local, addr)
		if err != nil {
			return err
		}
		conn.blockCheck = s.base.blocked
		s.watchConn(conn)
		s.conn = conn
		s.state = stateConnected
		return nil
	default:
		return errno.NotSupported
	}
}

// SendMsg 发送消息(M08):TCP 为拼接写;UDP 按数据报投递;控制
// 消息仅 AF_UNIX 支持(接收方获得独立描述符引用)。
func (s *INETSocket) SendMsg(ctx context.Context, buffers [][]byte, to *Addr, rights *ControlRights) (int, error) {
	s.mu.Lock()
	conn := s.conn
	udp := s.udp
	state := s.state
	s.mu.Unlock()
	if rights != nil {
		return 0, errno.Invalid // SCM_RIGHTS 仅 AF_UNIX
	}
	if state == stateConnected && conn != nil {
		return conn.WriteMsg(ctx, buffers, nil)
	}
	if udp != nil || s.typ == SockDatagram {
		if to == nil {
			return 0, errno.DestAddrReq
		}
		// 数据报:多 iovec 拼接为单个报文,不能拆成多个数据报。
		total := 0
		payload := make([]byte, 0, 64)
		for _, buffer := range buffers {
			payload = append(payload, buffer...)
			total += len(buffer)
		}
		if _, err := s.SendTo(ctx, payload, *to); err != nil {
			return 0, err
		}
		return total, nil
	}
	return 0, errno.NotConn
}

// RecvMsg 接收消息(M08)。
func (s *INETSocket) RecvMsg(ctx context.Context, buffer []byte) (int, *Addr, *ControlRights, error) {
	if s.typ == SockDatagram {
		// UDP 数据报:复用 RecvFrom 的报文边界/来源地址语义;
		// 控制消息不在数据报子集内。
		n, from, err := s.RecvFrom(ctx, buffer)
		if err != nil {
			return 0, nil, nil, err
		}
		return n, &from, nil, nil
	}
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected || conn == nil {
		return 0, nil, nil, errno.NotConn
	}
	n, rights, err := conn.ReadMsg(ctx, buffer)
	return n, nil, rights, err
}

// Read 读 TCP 接收方向(连接形态);其余形态报 ENOTCONN/ENOTSUP。
func (s *INETSocket) Read(p []byte) (int, error) {
	return s.ReadContext(context.Background(), p)
}

func (s *INETSocket) ReadContext(ctx context.Context, p []byte) (int, error) {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected || conn == nil {
		return 0, errno.NotConn
	}
	return conn.ReadContext(ctx, p)
}

// Write 发送(连接形态)。
func (s *INETSocket) Write(p []byte) (int, error) {
	return s.WriteContext(context.Background(), p)
}

func (s *INETSocket) WriteContext(ctx context.Context, p []byte) (int, error) {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected || conn == nil {
		return 0, errno.NotConn
	}
	return conn.WriteContext(ctx, p)
}

// Send 统一 send/sendto:to 为 nil 时等价于 send(2),作用于已连接流。
func (s *INETSocket) Send(ctx context.Context, data []byte, to *Addr) (int, error) {
	if to == nil {
		s.mu.Lock()
		typ := s.typ
		state := s.state
		conn := s.conn
		s.mu.Unlock()
		if typ == SockStream && state == stateConnected && conn != nil {
			return conn.WriteContext(ctx, data)
		}
		if typ == SockDatagram {
			return 0, errno.DestAddrReq
		}
		return 0, errno.NotConn
	}
	return s.SendTo(ctx, data, *to)
}

// Recv 统一 recv/recvfrom:已连接流从连接读取,数据报走来源队列。
func (s *INETSocket) Recv(ctx context.Context, buffer []byte) (int, Addr, error) {
	s.mu.Lock()
	typ := s.typ
	state := s.state
	conn := s.conn
	s.mu.Unlock()
	if typ == SockStream && state == stateConnected && conn != nil {
		loaded, err := conn.ReadContext(ctx, buffer)
		return loaded, Addr{}, err
	}
	return s.RecvFrom(ctx, buffer)
}

// SendTo UDP 投递(M05);TCP 连接形态不支持 sendto 目标语义。
func (s *INETSocket) SendTo(ctx context.Context, data []byte, to Addr) (int, error) {
	s.mu.Lock()
	udp := s.udp
	state := s.state
	s.mu.Unlock()
	if s.typ != SockDatagram || (state != stateBound && state != stateUnbound) {
		return 0, errno.NotConn
	}
	if udp == nil {
		// 未绑定发送:自动绑定临时端口(发送端地址可见)。
		endpoint, err := s.ns.BindUDP(Addr{Domain: AFInet, IP: s.ns.Loopback()})
		if err != nil {
			return 0, err
		}
		s.mu.Lock()
		s.udp = endpoint
		s.local = endpoint.local
		if state == stateUnbound {
			s.state = stateBound
		}
		s.mu.Unlock()
		endpoint.OnChange(s.base.notify)
		udp = endpoint
	}
	return s.ns.SendTo(ctx, udp, data, to)
}

// RecvFrom UDP 接收(M05):报文边界、截断与来源地址。未绑定端点
// 自动绑定(接收端口的既定行为,阻断无流量到达的永久等待)。
func (s *INETSocket) RecvFrom(ctx context.Context, buffer []byte) (int, Addr, error) {
	s.mu.Lock()
	udp := s.udp
	s.mu.Unlock()
	if s.typ != SockDatagram {
		return 0, Addr{}, errno.NotConn
	}
	if udp == nil {
		endpoint, err := s.ns.BindUDP(Addr{Domain: AFInet, IP: s.ns.Loopback()})
		if err != nil {
			return 0, Addr{}, err
		}
		s.mu.Lock()
		s.udp = endpoint
		s.local = endpoint.local
		if s.state == stateUnbound {
			s.state = stateBound
		}
		s.mu.Unlock()
		endpoint.OnChange(s.base.notify)
		udp = endpoint
	}
	// 非阻塞状态传递:端点与 socket 共享同一非阻塞旗标来源。
	endpoint := &delegatingUDP{inner: udp, base: &s.base}
	return endpoint.RecvFrom(ctx, buffer)
}

// delegatingUDP 把 socket 的非阻塞状态接到端点接收等待上。
type delegatingUDP struct {
	inner *UDPEndpoint
	base  *endpointBase
}

func (d *delegatingUDP) RecvFrom(ctx context.Context, buffer []byte) (int, Addr, error) {
	if err := waitOn(ctx, &d.inner.base.wake, d.base.blocked, func() (bool, error) {
		d.inner.mu.Lock()
		defer d.inner.mu.Unlock()
		return len(d.inner.queue) > 0, nil
	}); err != nil {
		return 0, Addr{}, err
	}
	d.inner.mu.Lock()
	defer d.inner.mu.Unlock()
	if len(d.inner.queue) == 0 {
		return 0, Addr{}, errno.WouldBlock
	}
	packet := d.inner.queue[0]
	d.inner.queue = d.inner.queue[1:]
	if len(buffer) >= len(packet.data) {
		copy(buffer, packet.data)
		return len(packet.data), packet.from, nil
	}
	copy(buffer, packet.data[:len(buffer)])
	return len(buffer), packet.from, nil
}

// LocalAddr 返回本端地址;未绑定时端口为 0。
func (s *INETSocket) LocalAddr() (Addr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.local, true
}

// PeerAddr 返回对端地址(连接形态)。
func (s *INETSocket) PeerAddr() (Addr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateConnected && s.conn != nil {
		return s.conn.remote, true
	}
	return Addr{}, false
}

// Shutdown 半关闭(M04)。
func (s *INETSocket) Shutdown(how ShutdownHow) error {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected || conn == nil {
		return errno.NotConn
	}
	return conn.Shutdown(how)
}

// Close 关闭 socket 与其活动端点。
func (s *INETSocket) Close() error {
	if !s.base.markClosed() {
		return errno.BadHandle
	}
	s.mu.Lock()
	listener := s.listener
	conn := s.conn
	udp := s.udp
	s.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if udp != nil {
		_ = udp.Close()
	}
	s.base.notify()
	return nil
}

// Ready 实现 poll 就绪(M07 前置)。
func (s *INETSocket) Ready() poll.Readiness {
	s.mu.Lock()
	listener := s.listener
	conn := s.conn
	udp := s.udp
	state := s.state
	s.mu.Unlock()
	switch {
	case state == stateListening && listener != nil:
		return listener.Ready()
	case state == stateConnected && conn != nil:
		return conn.Ready()
	case udp != nil:
		return udp.Ready()
	default:
		return s.base.Ready(0)
	}
}

// UnixSocket 是 AF_UNIX socket 的生命周期视图(M03):流式。
type UnixSocket struct {
	ns   *Namespace
	base endpointBase

	mu    sync.Mutex
	state socketState
	local Addr

	listener *UnixListener
	conn     *UnixConn
}

// SetNonBlock 切换非阻塞(M02)。
func (s *UnixSocket) SetNonBlock(on bool) { s.base.SetNonBlock(on) }

// watchConn 订阅连接两个方向核心的就绪变化(M07)。
func (s *UnixSocket) watchConn(conn *UnixConn) {
	conn.rx.onChange(s.base.notify)
	conn.tx.onChange(s.base.notify)
}

// OnChange 订阅就绪变化(poll/epoll 等待图,M07)。
func (s *UnixSocket) OnChange(fn func()) (cancel func()) { return s.base.OnChange(fn) }

// NonBlock 报告非阻塞状态。
func (s *UnixSocket) NonBlock() bool { return s.base.blocked() }

// StatusFlags 报告 fd 状态标志。
func (s *UnixSocket) StatusFlags() (uint32, error) {
	flags := uint32(2)
	if s.base.blocked() {
		flags |= 0o4000
	}
	return flags, nil
}

// SetStatusFlags 切换 O_NONBLOCK。
func (s *UnixSocket) SetStatusFlags(flags uint32) error {
	s.base.SetNonBlock(flags&0o4000 != 0)
	return nil
}

// NewUnixSocket 创建未绑定 AF_UNIX 流式 socket。
func (n *Namespace) NewUnixSocket(typ SockType) (*UnixSocket, error) {
	if typ != SockStream {
		return nil, errno.NotSupported
	}
	return &UnixSocket{ns: n, state: stateUnbound, local: Addr{Domain: AFUnix}}, nil
}

// Bind 登记路径监听。
func (s *UnixSocket) Bind(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateUnbound {
		return errno.Invalid
	}
	listener, err := s.ns.BindUnixListener(path)
	if err != nil {
		return err
	}
	if err := listener.Listen(0); err != nil {
		return err
	}
	listener.OnChange(s.base.notify)
	s.listener = listener
	s.local = Addr{Domain: AFUnix, Path: path}
	s.state = stateBound
	return nil
}

// Listen 进入监听。
func (s *UnixSocket) Listen(backlog int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil || (s.state != stateBound && s.state != stateListening) {
		return errno.Invalid
	}
	if s.state == stateListening {
		return nil
	}
	if err := s.listener.Listen(backlog); err != nil {
		return err
	}
	s.state = stateListening
	return nil
}

// Accept 接受连接(M03)。
func (s *UnixSocket) Accept(ctx context.Context) (*UnixSocket, error) {
	s.mu.Lock()
	listener := s.listener
	listening := s.state == stateListening
	nonblock := s.base.blocked()
	s.mu.Unlock()
	if !listening || listener == nil {
		return nil, errno.Invalid
	}
	var conn *UnixConn
	var err error
	if nonblock {
		conn, err = listener.AcceptNonBlock()
	} else {
		conn, err = listener.Accept(ctx)
	}
	if err != nil {
		return nil, err
	}
	accepted := &UnixSocket{
		ns:    s.ns,
		state: stateConnected,
		local: conn.local,
		conn:  conn,
	}
	accepted.conn.blockCheck = accepted.base.blocked
	accepted.watchConn(accepted.conn)
	if nonblock {
		accepted.base.SetNonBlock(true)
	}
	return accepted, nil
}

// Connect 连接到路径监听(M03)。
func (s *UnixSocket) Connect(ctx context.Context, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateUnbound {
		return errno.IsConn
	}
	conn, err := s.ns.ConnectUnix(ctx, path)
	if err != nil {
		return err
	}
	conn.blockCheck = s.base.blocked
	s.watchConn(conn)
	s.conn = conn
	s.state = stateConnected
	return nil
}

// SocketPairUnix 创建互联的 socketpair(M03)。
func (n *Namespace) SocketPairUnix() (*UnixSocket, *UnixSocket) {
	client, server := SocketpairUnix()
	a := &UnixSocket{ns: n, state: stateConnected}
	b := &UnixSocket{ns: n, state: stateConnected}
	a.conn = client
	b.conn = server
	a.conn.blockCheck = a.base.blocked
	b.conn.blockCheck = b.base.blocked
	a.watchConn(a.conn)
	b.watchConn(b.conn)
	return a, b
}

// Read 读流。
func (s *UnixSocket) Read(p []byte) (int, error) {
	return s.ReadContext(context.Background(), p)
}

func (s *UnixSocket) ReadContext(ctx context.Context, p []byte) (int, error) {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected {
		return 0, errno.NotConn
	}
	return conn.ReadContext(ctx, p)
}

// Write 写流。
func (s *UnixSocket) Write(p []byte) (int, error) {
	return s.WriteContext(context.Background(), p)
}

func (s *UnixSocket) WriteContext(ctx context.Context, p []byte) (int, error) {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected {
		return 0, errno.NotConn
	}
	return conn.WriteContext(ctx, p)
}

// SendMsg 发送消息(M08):拼接写;SCM_RIGHTS 随数据交付,接收方
// 获得独立描述符引用。
func (s *UnixSocket) SendMsg(ctx context.Context, buffers [][]byte, rights *ControlRights) (int, error) {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected || conn == nil {
		return 0, errno.NotConn
	}
	return conn.WriteMsg(ctx, buffers, rights)
}

// RecvMsg 接收消息(M08)。
func (s *UnixSocket) RecvMsg(ctx context.Context, buffer []byte) (int, *ControlRights, error) {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected || conn == nil {
		return 0, nil, errno.NotConn
	}
	return conn.ReadMsg(ctx, buffer)
}

// Shutdown 半关闭(M03)。
func (s *UnixSocket) Shutdown(how ShutdownHow) error {
	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state != stateConnected {
		return errno.NotConn
	}
	return conn.Shutdown(how)
}

// Close 关闭 socket 与端点。
func (s *UnixSocket) Close() error {
	if !s.base.markClosed() {
		return errno.BadHandle
	}
	s.mu.Lock()
	listener := s.listener
	conn := s.conn
	s.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if s.state == stateConnected {
		_ = conn.Close()
	}
	s.base.notify()
	return nil
}

// Ready 实现 poll 就绪。
func (s *UnixSocket) Ready() poll.Readiness {
	s.mu.Lock()
	listener := s.listener
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if state == stateListening && listener != nil {
		return listener.Ready()
	}
	if state == stateConnected {
		return conn.Ready()
	}
	return s.base.Ready(0)
}

// LocalAddr 返回绑定地址。
func (s *UnixSocket) LocalAddr() (Addr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.local, true
}

// PeerAddr 返回对端地址(连接形态)。
func (s *UnixSocket) PeerAddr() (Addr, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateConnected {
		return s.conn.remote, true
	}
	return Addr{}, false
}
