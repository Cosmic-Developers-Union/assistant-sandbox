package elf

import (
	"context"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vfd "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// processSockets 把内核网络命名空间与描述符表适配为 socket 系统调用
// 端口(M01–M05):端点创建经命名空间(VM 独立,端口互不冲突),
// 描述符安装经 vfd.Installer 能力,既有 fd 经描述符表回查端点。
type processSockets struct {
	ns          *net.Namespace
	installer   vfd.Installer
	descriptors func(ctx context.Context, fd uint64) (any, error)
	pid         uint64
}

// netlinkEndpoint 是受控网络命名空间的最小 AF_NETLINK 占位端点。
// Node/libuv 在枚举本机接口时会先创建一个 NETLINK_ROUTE 套接字;本
// VM 不转发宿主路由消息,空端点使探测得到"无额外接口"而不是 ENOTSUP。
type netlinkEndpoint struct{}

func (netlinkEndpoint) Read([]byte) (int, error)    { return 0, io.EOF }
func (netlinkEndpoint) Write(p []byte) (int, error) { return len(p), nil }
func (netlinkEndpoint) Close() error                { return nil }

func (p processSockets) lookup(ctx context.Context, fd uint64) (any, error) {
	return p.descriptors(ctx, fd)
}

// install 安装端点为新描述符。
func (p processSockets) install(handle vfd.Stream, nonblock, cloexec bool) (uint64, error) {
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
		// Linux 允许显式指定与类型匹配的协议号:IPPROTO_TCP=6、
		// IPPROTO_UDP=17。getaddrinfo/asyncio 会携带该协议号。
		matched := (typ == uint64(net.SockStream) && protocol == 6) ||
			(typ == uint64(net.SockDatagram) && protocol == 17)
		if !matched {
			return 0, errno.NotSupported
		}
	}
	switch domain {
	case uint64(net.AFInet), uint64(net.AFInet6):
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
	case uint64(net.AFNetlink):
		return p.install(netlinkEndpoint{}, nonblock, cloexec)
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
	case netlinkEndpoint:
		return nil
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
	case netlinkEndpoint:
		return nil
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
	case netlinkEndpoint:
		return 0, errno.NotSupported
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
	case netlinkEndpoint:
		return nil
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
	case netlinkEndpoint:
		return len(data), nil
	case *net.INETSocket:
		return socket.Send(ctx, data, to)
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
	case netlinkEndpoint:
		return 0, net.Addr{Domain: net.AFNetlink}, nil
	case *net.INETSocket:
		return socket.Recv(ctx, buffer)
	default:
		return 0, net.Addr{}, errno.NotSock
	}
}

func (p processSockets) SendMsg(ctx context.Context, fd uint64, buffers [][]byte, to *net.Addr, handles []any) (int, error) {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return 0, err
	}
	// SCM_RIGHTS 传入的是客体 fd 编号:换成保留引用随流传递,接收侧
	// 安装为独立描述符槽(共享同一打开文件描述,M08)。
	var rights *net.ControlRights
	if handles != nil {
		transfer, ok := p.installer.(vfd.RightsTransfer)
		if !ok {
			return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 SCM_RIGHTS 传递")
		}
		objects := make([]any, 0, len(handles))
		release := func() {
			for _, object := range objects {
				if passed, ok := object.(*vfd.Passed); ok {
					_ = passed.Release()
				}
			}
		}
		for _, candidate := range handles {
			number, ok := candidate.(uint64)
			if !ok {
				release()
				return 0, errno.BadHandle
			}
			passed, err := transfer.Retain(p.pid, uint32(number))
			if err != nil {
				release()
				return 0, err
			}
			objects = append(objects, passed)
		}
		rights = &net.ControlRights{Handles: objects, Release: release}
	}
	switch socket := handle.(type) {
	case netlinkEndpoint:
		total := 0
		for _, buffer := range buffers {
			total += len(buffer)
		}
		releaseRights(rights)
		return total, nil
	case *net.INETSocket:
		return socket.SendMsg(ctx, buffers, to, rights)
	case *net.UnixSocket:
		if to != nil {
			releaseRights(rights)
			return 0, errno.NotSupported
		}
		return socket.SendMsg(ctx, buffers, rights)
	default:
		releaseRights(rights)
		return 0, errno.NotSock
	}
}

// releaseRights 归还未交付的 SCM_RIGHTS 引用(M08)。
func releaseRights(rights *net.ControlRights) {
	if rights != nil && rights.Release != nil {
		rights.Release()
	}
}

func (p processSockets) RecvMsg(ctx context.Context, fd uint64, buffer []byte) (int, *net.Addr, []any, error) {
	handle, err := p.lookup(ctx, fd)
	if err != nil {
		return 0, nil, nil, err
	}
	switch socket := handle.(type) {
	case netlinkEndpoint:
		return 0, nil, nil, nil
	case *net.INETSocket:
		n, from, rights, err := socket.RecvMsg(ctx, buffer)
		if rights != nil {
			return n, from, rights.Handles, err
		}
		return n, from, nil, err
	case *net.UnixSocket:
		n, rights, err := socket.RecvMsg(ctx, buffer)
		if rights != nil {
			return n, nil, rights.Handles, err
		}
		return n, nil, nil, err
	default:
		return 0, nil, nil, errno.NotSock
	}
}

// InstallPassed 把传递来的引用安装为当前进程的新描述符(M08):共享
// 同一打开文件描述(偏移与状态一致)。同一引用不能安装两次。
func (p processSockets) InstallPassed(handle any) (uint64, error) {
	passed, ok := handle.(*vfd.Passed)
	if !ok {
		return 0, errno.BadHandle
	}
	transfer, ok := p.installer.(vfd.RightsTransfer)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "描述符表不支持 SCM_RIGHTS 接收")
	}
	installed, err := transfer.InstallPassed(p.pid, passed)
	if err != nil {
		return 0, err
	}
	return uint64(installed), nil
}

func (p processSockets) Shutdown(fd uint64, how net.ShutdownHow) error {
	handle, err := p.lookup(context.Background(), fd)
	if err != nil {
		return err
	}
	switch socket := handle.(type) {
	case netlinkEndpoint:
		return nil
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
	case netlinkEndpoint:
		return net.Addr{Domain: net.AFNetlink}, nil
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
	case netlinkEndpoint:
		return net.Addr{Domain: net.AFNetlink}, nil
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
			case netlinkEndpoint:
				return 3, nil
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
		if _, ok := handle.(netlinkEndpoint); ok {
			return nil
		}
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
