package net

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/wait"
)

// streamBufCapacity 是回环流式连接每方向的字节容量:写入在容量满时
// 阻塞(非阻塞返回 EAGAIN),与管道一致的背压模型。
const streamBufCapacity = 64 << 10

// backlogLimit 是监听队列上限(连接数);超过后 connect 报
// ECONNREFUSED(回环撮合的既定行为)。
const backlogLimit = 128

// streamCore 是互联两端共享方向的一条字节流:一端写入,另一端读取。
// eofRead 置位后本地读在排空后返回 EOF(对端 shutdown(WR) 或关闭);
// noReader 置位后对端写入报 EPIPE(本地关闭);discarding 置位后对端
// 写入成功但数据丢弃(本地 shutdown(RD),Linux 语义:数据被确认
// 后丢弃,对端不阻塞)。
type streamCore struct {
	mu         sync.Mutex
	wake       wait.Queue
	buf        []byte
	eofRead    bool
	noReader   bool
	discarding bool

	// watchers 是就绪变化订阅(M07):互联两端的 socket 各自订阅
	// 共享核心,数据到达/对端关闭经此接入各自的 poll/epoll 等待图。
	watchers []*coreWatcher

	// written/consumed 是流位置;controls 携带 SCM_RIGHTS 控制消息,
	// 附着于发送时的流位置,读取覆盖该位置时交付(M08)。
	written  uint64
	consumed uint64
	controls []attachedControl
}

// attachedControl 是附着于流位置的控制消息。
type attachedControl struct {
	position uint64
	rights   ControlRights
}

// ControlRights 是 SCM_RIGHTS 的传递载荷:句柄对象由系统调用层换算
// fd 编号,net 层只承载对象引用(接收方获得独立描述符引用)。Release
// 在控制消息被丢弃(未安装)时归还发送侧取得的引用,可为空。
type ControlRights struct {
	Handles []any
	Release func()
}

// releaseRights 归还未交付的控制引用。
func releaseRights(rights *ControlRights) {
	if rights != nil && rights.Release != nil {
		rights.Release()
	}
}

type coreWatcher struct{ fn func() }

// onChange 订阅核心就绪变化。
func (c *streamCore) onChange(fn func()) (cancel func()) {
	c.mu.Lock()
	w := &coreWatcher{fn: fn}
	c.watchers = append(c.watchers, w)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for index, registered := range c.watchers {
			if registered == w {
				c.watchers = append(c.watchers[:index], c.watchers[index+1:]...)
				return
			}
		}
	}
}

func (c *streamCore) notify() {
	c.wake.WakeAll(wait.Data)
	c.mu.Lock()
	watchers := make([]*coreWatcher, len(c.watchers))
	copy(watchers, c.watchers)
	c.mu.Unlock()
	for _, w := range watchers {
		w.fn()
	}
}

// dropControlsLocked 取出并清空尚未交付的控制消息;调用方持有核心
// 锁,须在锁外 releaseControls 归还传递引用。
func (c *streamCore) dropControlsLocked() []ControlRights {
	if len(c.controls) == 0 {
		return nil
	}
	dropped := make([]ControlRights, 0, len(c.controls))
	for _, control := range c.controls {
		dropped = append(dropped, control.rights)
	}
	c.controls = nil
	return dropped
}

// releaseControls 归还被丢弃控制消息的 SCM_RIGHTS 引用。
func releaseControls(controls []ControlRights) {
	for index := range controls {
		releaseRights(&controls[index])
	}
}

// streamConn 是流式 socket 连接的一端(TCP 与 AF_UNIX 共用):rx 从
// 对端接收,tx 向对端发送。关闭与半关闭按 Linux 语义落账到共享核心。
type streamConn struct {
	base endpointBase

	rx *streamCore // 本端读(对端写)
	tx *streamCore // 本端写(对端读)

	local  Addr
	remote Addr

	mu      sync.Mutex
	closed  bool
	readOff bool // SHUT_RD:本地读返回 EOF,入站数据丢弃

	// blockCheck 覆盖非阻塞判定:socket 包装层把自身 O_NONBLOCK 旗标
	// 接入连接等待(缺省用本端 base)。
	blockCheck func() bool
}

// nonblocked 报告非阻塞状态(优先包装层旗标)。
func (c *streamConn) nonblocked() bool {
	if c.blockCheck != nil {
		return c.blockCheck()
	}
	return c.base.blocked()
}

// TCPConn 是回环 TCP 连接的一端(M04)。
type TCPConn = streamConn

// UnixConn 是 AF_UNIX 流式连接的一端(M03)。
type UnixConn = streamConn

// newStreamPair 建立一对互联连接:(client 写 → server 读,server 写 →
// client 读)。
func newStreamPair(clientAddr, serverAddr Addr) (*streamConn, *streamConn) {
	forward := &streamCore{} // client 写,server 读
	back := &streamCore{}    // server 写,client 读
	client := &streamConn{rx: back, tx: forward, local: clientAddr, remote: serverAddr}
	server := &streamConn{rx: forward, tx: back, local: serverAddr, remote: clientAddr}
	return client, server
}

// Read 读接收方向的数据;对端 shutdown(WR) 或关闭后,排空即 EOF。
func (c *streamConn) Read(p []byte) (int, error) {
	return c.ReadContext(context.Background(), p)
}

func (c *streamConn) ReadContext(ctx context.Context, p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed || c.readOff
	c.mu.Unlock()
	if closed {
		return 0, errno.BadHandle
	}
	if len(p) == 0 {
		return 0, nil
	}
	for {
		c.rx.mu.Lock()
		if len(c.rx.buf) > 0 {
			n := copy(p, c.rx.buf)
			c.rx.buf = c.rx.buf[n:]
			c.rx.consumed += uint64(n)
			c.rx.mu.Unlock()
			c.activity()
			return n, nil
		}
		if c.rx.eofRead {
			c.rx.mu.Unlock()
			return 0, io.EOF
		}
		c.rx.mu.Unlock()
		if err := waitOn(ctx, &c.rx.wake, c.nonblocked, func() (bool, error) {
			c.rx.mu.Lock()
			defer c.rx.mu.Unlock()
			return len(c.rx.buf) > 0 || c.rx.eofRead, nil
		}); err != nil {
			return 0, err
		}
	}
}

// Write 发送;容量满时阻塞等待(非阻塞 EAGAIN),对端关闭后报 EPIPE
// (部分完成时返回已写字节数)。
func (c *streamConn) Write(p []byte) (int, error) {
	return c.WriteContext(context.Background(), p)
}

func (c *streamConn) WriteContext(ctx context.Context, p []byte) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, errno.BadHandle
	}
	written := 0
	for written < len(p) {
		c.tx.mu.Lock()
		if c.tx.noReader {
			c.tx.mu.Unlock()
			return written, errno.BrokenPipe
		}
		free := streamBufCapacity - len(c.tx.buf)
		if free > 0 {
			n := min(free, len(p)-written)
			c.tx.buf = append(c.tx.buf, p[written:written+n]...)
			written += n
			c.tx.written += uint64(n)
			c.tx.mu.Unlock()
			c.activity()
			continue
		}
		c.tx.mu.Unlock()
		if err := waitOn(ctx, &c.tx.wake, c.nonblocked, func() (bool, error) {
			c.tx.mu.Lock()
			defer c.tx.mu.Unlock()
			if c.tx.noReader {
				return false, errno.BrokenPipe
			}
			return len(c.tx.buf) < streamBufCapacity, nil
		}); err != nil {
			if written > 0 {
				return written, nil
			}
			return 0, err
		}
	}
	return written, nil
}

// Shutdown 按 Linux 语义半关闭:SHUT_WR 使对端排空后读到 EOF;
// SHUT_RD 使本地读返回 EOF 且入站数据丢弃(对端写不阻塞);
// SHUT_ALL 兼施。
func (c *streamConn) Shutdown(how ShutdownHow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errno.BadHandle
	}
	switch how {
	case ShutdownRead:
		c.readOff = true
		c.rx.mu.Lock()
		c.rx.discarding = true
		c.rx.buf = nil
		dropped := c.rx.dropControlsLocked()
		c.rx.mu.Unlock()
		releaseControls(dropped)
		c.activity()
	case ShutdownWrite:
		c.tx.mu.Lock()
		c.tx.eofRead = true
		c.tx.mu.Unlock()
		c.activity()
	case ShutdownAll:
		c.readOff = true
		c.rx.mu.Lock()
		c.rx.discarding = true
		c.rx.buf = nil
		dropped := c.rx.dropControlsLocked()
		c.rx.mu.Unlock()
		releaseControls(dropped)
		c.tx.mu.Lock()
		c.tx.eofRead = true
		c.tx.mu.Unlock()
		c.activity()
	default:
		return errno.Invalid
	}
	return nil
}

// Close 关闭本端:对端读排空后 EOF,对端写报 EPIPE;对端不再因本端
// 阻塞。双端关闭后缓冲随核心释放。
func (c *streamConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errno.BadHandle
	}
	c.closed = true
	c.mu.Unlock()
	c.tx.mu.Lock()
	c.tx.eofRead = true
	c.tx.mu.Unlock()
	c.rx.mu.Lock()
	c.rx.noReader = true
	c.rx.buf = nil
	dropped := c.rx.dropControlsLocked()
	c.rx.mu.Unlock()
	releaseControls(dropped)
	c.activity()
	c.base.markClosed()
	c.base.notify()
	return nil
}

// reset 由监听关闭时重置排队中的连接:对端读写立即不可用(回环撮合
// 的既定行为,等价 RST)。
func (c *streamConn) reset() {
	c.rx.mu.Lock()
	c.rx.noReader = true
	c.rx.eofRead = true
	c.rx.buf = nil
	dropped := c.rx.dropControlsLocked()
	c.rx.mu.Unlock()
	releaseControls(dropped)
	c.activity()
	c.base.markClosed()
	c.base.notify()
}

// activity 通知核心等待者与就绪订阅者(两端 socket 各自被唤醒);
// 调用方不得持有核心或端点锁。
func (c *streamConn) activity() {
	c.rx.notify()
	c.tx.notify()
}

// Ready 实现 poll 就绪(M07 前置):有数据或对端已关闭时可读;
// 容量未满且对端仍读时可写。
func (c *streamConn) Ready() poll.Readiness {
	c.rx.mu.Lock()
	readable := len(c.rx.buf) > 0 || (c.rx.eofRead && !c.readOff)
	rxEof := c.rx.eofRead
	c.rx.mu.Unlock()
	c.tx.mu.Lock()
	writable := len(c.tx.buf) < streamBufCapacity && !c.tx.noReader && !c.closed
	c.tx.mu.Unlock()
	var bits poll.Readiness
	if readable {
		bits |= poll.Readable
	}
	if rxEof {
		bits |= poll.Hangup
	}
	if writable {
		bits |= poll.Writable
	}
	return c.base.Ready(bits)
}

// LocalAddr/PeerAddr 返回本端与对端地址(getsockname/getpeername 用)。
func (c *streamConn) LocalAddr() (Addr, bool) { return c.local, true }
func (c *streamConn) PeerAddr() (Addr, bool)  { return c.remote, true }

// TCPListener 是回环 TCP 监听(M04)。
type TCPListener struct {
	ns    *Namespace
	base  endpointBase
	local Addr

	mu     sync.Mutex
	queue  []*TCPConn
	closed bool
}

// BindTCPListener 在命名空间登记回环监听;端口已占用报 EADDRINUSE。
// reuse 为 SO_REUSEADDR:本模型无 TIME_WAIT,重复绑定仍报占用。
func (n *Namespace) BindTCPListener(addr Addr, reuse bool) (*TCPListener, error) {
	_ = reuse
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return nil, errno.BadHandle
	}
	key := listenKey{addr: addr.IP, port: addr.Port}
	if _, ok := n.tcpListen[key]; ok {
		return nil, errno.Wrap(errno.AddressInUse, "端口已占用")
	}
	listener := &TCPListener{ns: n, local: addr}
	n.tcpListen[key] = listener
	return listener, nil
}

// LookupTCPListener 查找回环监听(connect 用)。
func (n *Namespace) LookupTCPListener(addr Addr) (*TCPListener, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	listener, ok := n.tcpListen[listenKey{addr: addr.IP, port: addr.Port}]
	return listener, ok
}

// Listen 把端点标记为可接受连接;队列容量固定为 backlogLimit。
func (l *TCPListener) Listen(backlog int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errno.BadHandle
	}
	_ = backlog // 容量固定,backlog 仅记录上限语义
	return nil
}

// Accept 取出一个已建立的连接;阻塞模式等待,非阻塞(由调用方以
// AcceptNonBlock 进入)返回 EAGAIN。
func (l *TCPListener) Accept(ctx context.Context) (*TCPConn, error) {
	for {
		conn, err := l.AcceptNonBlock()
		if err == nil {
			return conn, nil
		}
		if !errors.Is(err, errno.WouldBlock) {
			return nil, err
		}
		if lerr := l.base.waitBlocked(ctx, func() (bool, error) {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.closed {
				return false, errno.BadHandle
			}
			return len(l.queue) > 0, nil
		}); lerr != nil {
			return nil, lerr
		}
	}
}

// AcceptNonBlock 在无待接受连接时立即返回 EAGAIN。
func (l *TCPListener) AcceptNonBlock() (*TCPConn, error) {
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

// Close 关闭监听:停止接受,排队中的连接被重置(对端读写立即失败)。
func (l *TCPListener) Close() error {
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
		ns.removeTCPListener(l)
	}
	for _, conn := range pending {
		conn.reset()
	}
	l.base.notify()
	return nil
}

func (n *Namespace) removeTCPListener(listener *TCPListener) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for key, registered := range n.tcpListen {
		if registered == listener {
			delete(n.tcpListen, key)
		}
	}
}

func (l *TCPListener) closeByNamespace() { _ = l.Close() }

// OnChange 订阅就绪变化(poll/epoll 等待图,M07)。
func (l *TCPListener) OnChange(fn func()) (cancel func()) { return l.base.OnChange(fn) }

// Ready 实现 poll 就绪:有待接受连接时可读。
func (l *TCPListener) Ready() poll.Readiness {
	l.mu.Lock()
	pending := len(l.queue) > 0
	l.mu.Unlock()
	if pending {
		return l.base.Ready(poll.Readable | poll.Writable)
	}
	return l.base.Ready(poll.Writable)
}

// Read/Write:监听套接字不可读写(Linux:ENOTCONN)。
func (l *TCPListener) Read(p []byte) (int, error) {
	return 0, errno.NotConn
}

func (l *TCPListener) Write(p []byte) (int, error) {
	return 0, errno.NotConn
}

// ConnectTCP 发起回环连接:无监听或队列满报 ECONNREFUSED;成功返回
// 客户端端连接(可立即读写,数据在 accept 前已缓冲,与 TCP 一致)。
// 未绑定的 local 由命名空间分配临时端口。
func (n *Namespace) ConnectTCP(ctx context.Context, local Addr, remote Addr) (*TCPConn, error) {
	// 非回环目标走宿主出口策略(M09);回环仍只指向本命名空间。
	if remote.IP.IsValid() && remote.IP != LoopbackIPv4 {
		return n.dialEgress(ctx, local, remote)
	}
	listener, ok := n.LookupTCPListener(remote)
	if !ok {
		return nil, errno.Wrap(errno.ConnRefused, "回环无监听")
	}
	listener.mu.Lock()
	defer listener.mu.Unlock()
	if listener.closed || listener.ns.Closed() {
		return nil, errno.Wrap(errno.ConnRefused, "监听已关闭")
	}
	if len(listener.queue) >= backlogLimit {
		return nil, errno.Wrap(errno.ConnRefused, "监听队列已满")
	}
	client, server := newStreamPair(local, remote)
	listener.queue = append(listener.queue, server)
	listener.base.notify()
	return client, nil
}

// EphemeralIPv4 返回带临时端口的回环地址(未绑定发送/连接用)。
func (n *Namespace) EphemeralIPv4() Addr {
	n.mu.Lock()
	n.nextCookie++
	cookie := n.nextCookie
	n.mu.Unlock()
	return Addr{Domain: AFInet, IP: LoopbackIPv4, Port: uint16(32768 + cookie%16384)}
}

// LocalAddr 返回监听地址。
func (l *TCPListener) LocalAddr() (Addr, bool) { return l.local, true }

// PeerAddr:监听套接字无对端。
func (l *TCPListener) PeerAddr() (Addr, bool) { return Addr{}, false }

// Shutdown:监听套接字不支持半关闭(Linux:ENOTCONN)。
func (l *TCPListener) Shutdown(ShutdownHow) error { return errno.NotConn }
