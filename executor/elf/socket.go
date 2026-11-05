package elf

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// processSockets 把内核网络命名空间与描述符表适配为 socket 系统调用
// 端口(M01–M05):端点创建经命名空间(VM 独立,端口互不冲突),
// 描述符安装经 fd.Installer 能力,既有 fd 经描述符表回查端点。
type processSockets struct {
	ns          *net.Namespace
	installer   fd.Installer
	descriptors func(ctx context.Context, fd uint64) (any, error)
	pid         uint64
}

func (p processSockets) lookup(ctx context.Context, fd uint64) (any, error) {
	return p.descriptors(ctx, fd)
}

// install 安装端点为新描述符。
func (p processSockets) install(handle fd.Stream, nonblock, cloexec bool) (uint64, error) {
	if p.installer == nil {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持安装 socket")
	}
	id, err := p.installer.InstallNext(p.pid, handle)
	if err != nil {
		return 0, err
	}
	if cloexec {
		if err := p.installer.SetCloexec(p.pid, id, true); err != nil {
			return uint64(id), err
		}
	}
	if nonblock {
		if setter, ok := handle.(interface{ SetNonBlock(bool) }); ok {
			setter.SetNonBlock(true)
		}
	}
	return uint64(id), nil
}

func (p processSockets) Socket(domain, typ, protocol uint64, nonblock, cloexec bool) (uint64, error) {
	if p.ns == nil {
		return 0, errno.Wrap(errno.NotSupported, "网络命名空间未装配")
	}
	if protocol != 0 {
		return 0, errno.NotSupported
	}
	switch domain {
	case uint64(net.AFInet):
		socket, err := p.ns.NewINETSocket(net.SockType(typ))
		if err != nil {
			return 0, err
		}
		return p.install(socket, nonblock, cloexec)
	case uint64(net.AFUnix):
		socket, err := p.ns.NewUnixSocket(net.SockType(typ))
		if err != nil {
			return 0, err
		}
		return p.install(socket, nonblock, cloexec)
	default:
		return 0, errno.NotSupported
	}
}

func (p processSockets) SocketPair(domain, typ, protocol uint64, nonblock, cloexec bool) (uint64, uint64, error) {
	if p.ns == nil {
		return 0, 0, errno.Wrap(errno.NotSupported, "网络命名空间未装配")
	}
	if domain != uint64(net.AFUnix) || typ != uint64(net.SockStream) || protocol != 0 {
		return 0, 0, errno.NotSupported
	}
	first, second := p.ns.SocketPairUnix()
	a, err := p.install(first, nonblock, cloexec)
	if err != nil {
		return 0, 0, err
	}
	b, err := p.install(second, nonblock, cloexec)
	if err != nil {
		return a, 0, err
	}
	return a, b, nil
}

func (p processSockets) Bind(ctx context.Context, fd uint64, addr net.Addr) error {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		return socket.Bind(addr)
	case *net.UnixSocket:
		if addr.Domain != net.AFUnix {
			return errno.Invalid
		}
		return socket.Bind(addr.Path)
	default:
		return errno.NotSock
	}
}

func (p processSockets) Listen(fd uint64, backlog int) error {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		return socket.Listen(backlog)
	case *net.UnixSocket:
		return socket.Listen(backlog)
	default:
		return errno.NotSock
	}
}

func (p processSockets) Accept(ctx context.Context, fd uint64, nonblock, cloexec bool) (uint64, error) {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return 0, err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		accepted, err := socket.Accept(ctx)
		if err != nil {
			return 0, err
		}
		_ = nonblock
		return p.install(accepted, nonblock, cloexec)
	case *net.UnixSocket:
		accepted, err := socket.Accept(ctx)
		if err != nil {
			return 0, err
		}
		_ = nonblock
		return p.install(accepted, nonblock, cloexec)
	default:
		return 0, errno.NotSock
	}
}

func (p processSockets) Connect(ctx context.Context, fd uint64, addr net.Addr) error {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		return socket.Connect(ctx, addr)
	case *net.UnixSocket:
		if addr.Domain != net.AFUnix {
			return errno.Invalid
		}
		return socket.Connect(ctx, addr.Path)
	default:
		return errno.NotSock
	}
}

func (p processSockets) SendTo(ctx context.Context, fd uint64, data []byte, to *net.Addr) (int, error) {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return 0, err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		target := net.Addr{Domain: net.AFInet, IP: net.LoopbackIPv4}
		if to != nil {
			target = *to
		}
		return socket.SendTo(ctx, data, target)
	default:
		return 0, errno.NotSock
	}
}

func (p processSockets) RecvFrom(ctx context.Context, fd uint64, buffer []byte) (int, net.Addr, error) {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return 0, net.Addr{}, err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		return socket.RecvFrom(ctx, buffer)
	default:
		return 0, net.Addr{}, errno.NotSock
	}
}

func (p processSockets) Shutdown(fd uint64, how net.ShutdownHow) error {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		return socket.Shutdown(how)
	case *net.UnixSocket:
		return socket.Shutdown(how)
	default:
		return errno.NotSock
	}
}

func (p processSockets) SockName(fd uint64) (net.Addr, error) {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return net.Addr{}, err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		if addr, ok := socket.LocalAddr(); ok {
			return addr, nil
		}
		return net.Addr{}, errno.NotConn
	case *net.UnixSocket:
		if addr, ok := socket.LocalAddr(); ok {
			return addr, nil
		}
		return net.Addr{}, errno.NotConn
	default:
		return net.Addr{}, errno.NotSock
	}
}

func (p processSockets) PeerName(fd uint64) (net.Addr, error) {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return net.Addr{}, err
	}
	switch socket := handle.(type) {
	case *net.INETSocket:
		if addr, ok := socket.PeerAddr(); ok {
			return addr, nil
		}
		return net.Addr{}, errno.NotConn
	case *net.UnixSocket:
		if addr, ok := socket.PeerAddr(); ok {
			return addr, nil
		}
		return net.Addr{}, errno.NotConn
	default:
		return net.Addr{}, errno.NotSock
	}
}

// socket 选项层级(SOL_SOCKET/IPPROTO_TCP)与运行时必需项(M06)。
const (
	solSocket   = 1
	solTCP      = 6
	soReuseAddr = 2
	soType      = 3
	soError     = 4
	soKeepAlive = 9
	tcpNoDelay  = 1
)

func (p processSockets) GetOption(fd uint64, level, name uint64) (uint64, error) {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return 0, err
	}
	switch level {
	case solSocket:
		switch name {
		case soType:
			switch socket := handle.(type) {
			case *net.INETSocket:
				return uint64(socket.Type()), nil
			case *net.UnixSocket:
				return uint64(net.SockStream), nil
			default:
				return 0, errno.NotSock
			}
		case soError:
			return 0, nil // 未排队错误(回环立即失败语义)
		case soKeepAlive:
			if socket, ok := handle.(*net.INETSocket); ok {
				if socket.KeepAlive() {
					return 1, nil
				}
			}
			return 0, nil
		default:
			return 0, errno.NotSupported
		}
	default:
		return 0, errno.NotSupported
	}
}

func (p processSockets) SetOption(fd uint64, level, name, value uint64) error {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return err
	}
	switch level {
	case solSocket:
		switch name {
		case soReuseAddr:
			if socket, ok := handle.(*net.INETSocket); ok {
				socket.SetReuseAddr(value != 0)
				return nil
			}
			return errno.NotSock
		case soKeepAlive:
			if socket, ok := handle.(*net.INETSocket); ok {
				socket.SetKeepAlive(value != 0)
				return nil
			}
			return errno.NotSock
		default:
			return errno.NotSupported
		}
	case solTCP:
		switch name {
		case tcpNoDelay:
			// 回环撮合无 Nagle 聚合,记录并接受(Node/Go 运行时
			// 常设)。
			return nil
		default:
			return errno.NotSupported
		}
	default:
		return errno.NotSupported
	}
}
