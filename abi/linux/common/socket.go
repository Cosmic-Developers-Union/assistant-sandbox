// socket.go 实现 socket 系统调用族(M02–M08):sockaddr 编解码与
// 全部 socket 调用的端口分派。端点与命名空间由 Sockets 端口实现
// (executor 侧绑定内核网络命名空间与描述符表)。
package common

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// SOCK_* 标志(SOCK_NONBLOCK/SOCK_CLOEXEC 与 eventfd 同值)。
const (
	sockNonBlock = 0o4000
	sockCloexec  = 0o2000000

	sockTypeMask = 0xff
)

// MSG_* 标志子集:MSG_NOSIGNAL 忽略(本 VM 无 SIGPIPE 网络路径),
// 其余未声明标志 EINVAL。
const msgNosignal = 0x4000

// msgCtrunc 是 recvmsg 的控制消息截断标志(Linux MSG_CTRUNC)。
const msgCtrunc = 0x8

// sockaddrInetSize/sockaddrInet6Size 是 sockaddr_in/sockaddr_in6 的
// asm-generic 字节数。
const (
	sockaddrInetSize  = 16
	sockaddrInet6Size = 28
)

// sockaddrUnixSize 是 sockaddr_un 的字节数(family + 108 路径字节)。
const sockaddrUnixSize = 110

// Sockets 是 socket 系统调用的端口(M02+):实现端点创建、描述符
// 安装与 socket 操作。未装配时 socket 族报 NotImplemented 族。
type Sockets interface {
	Socket(domain, typ, protocol uint64, nonblock, cloexec bool) (uint64, error)

	SocketPair(domain, typ, protocol uint64, nonblock, cloexec bool) (uint64, uint64, error)

	Bind(ctx context.Context, fd uint64, addr net.Addr) error

	Listen(fd uint64, backlog int) error

	Accept(ctx context.Context, fd uint64, nonblock, cloexec bool) (uint64, error)

	Connect(ctx context.Context, fd uint64, addr net.Addr) error

	SendTo(ctx context.Context, fd uint64, data []byte, to *net.Addr) (int, error)

	RecvFrom(ctx context.Context, fd uint64, buffer []byte) (int, net.Addr, error)

	// SendMsg/RecvMsg 是 sendmsg/recvmsg 的端口(M08):buffers 为
	// 分散缓冲,handles 为 SCM_RIGHTS 传递的句柄对象(AF_UNIX)。
	SendMsg(ctx context.Context, fd uint64, buffers [][]byte, to *net.Addr, handles []any) (int, error)

	RecvMsg(ctx context.Context, fd uint64, buffer []byte) (int, *net.Addr, []any, error)

	Shutdown(fd uint64, how net.ShutdownHow) error

	SockName(fd uint64) (net.Addr, error)

	PeerName(fd uint64) (net.Addr, error)

	GetOption(fd uint64, level, name uint64) (uint64, error)

	SetOption(fd uint64, level, name, value uint64) error

	// InstallPassed 把 SCM_RIGHTS 传递来的句柄对象安装为当前进程的
	// 新描述符(接收侧所有权:独立引用,编号由本调用分配)。
	InstallPassed(handle any) (uint64, error)
}

// parseSockaddr 解码客体 sockaddr(按域校验长度与格式)。
func parseSockaddr(ctx context.Context, s Syscalls, addr uint64, length uint64) (net.Addr, error) {
	if addr == 0 {
		return net.Addr{}, errno.Invalid
	}
	var head [2]byte
	if err := s.Memory.Read(ctx, addr, head[:]); err != nil {
		return net.Addr{}, errno.Fault
	}
	family := binary.LittleEndian.Uint16(head[:])
	switch family {
	case uint16(net.AFUnix):
		if length < 2 {
			return net.Addr{}, errno.Invalid
		}
		pathBytes := length - 2
		if pathBytes > 108 {
			pathBytes = 108
		}
		raw := make([]byte, pathBytes)
		if err := s.Memory.Read(ctx, addr+2, raw); err != nil {
			return net.Addr{}, errno.Fault
		}
		if end := indexByteNul(raw); end >= 0 {
			raw = raw[:end]
		}
		return net.Addr{Domain: net.AFUnix, Path: string(raw)}, nil
	case uint16(net.AFInet):
		if length < sockaddrInetSize {
			return net.Addr{}, errno.Invalid
		}
		var raw [sockaddrInetSize]byte
		if err := s.Memory.Read(ctx, addr, raw[:]); err != nil {
			return net.Addr{}, errno.Fault
		}
		port := binary.BigEndian.Uint16(raw[2:4])
		ip := net.AddrIPv4FromBytes(raw[4:8])
		return net.Addr{Domain: net.AFInet, IP: ip, Port: port}, nil
	case uint16(net.AFInet6):
		if length < sockaddrInet6Size {
			return net.Addr{}, errno.Invalid
		}
		var raw [sockaddrInet6Size]byte
		if err := s.Memory.Read(ctx, addr, raw[:]); err != nil {
			return net.Addr{}, errno.Fault
		}
		var packed [16]byte
		copy(packed[:], raw[8:24])
		ip := netip.AddrFrom16(packed)
		switch {
		case ip.Is4In6():
			ip = ip.Unmap()
		case ip == netip.IPv6Loopback():
			ip = net.LoopbackIPv4
		case isIPv4Compatible(packed):
			ip = netip.AddrFrom4([4]byte{packed[12], packed[13], packed[14], packed[15]})
		default:
			// 当前兼容配置只提供 IPv4 回环与出口,非映射 IPv6 不得
			// 被静默当作回环。
			return net.Addr{}, errno.NotSupported
		}
		return net.Addr{Domain: net.AFInet, IP: ip, Port: binary.BigEndian.Uint16(raw[2:4])}, nil
	case uint16(net.AFNetlink):
		// 本 VM 不提供 netlink 路由消息;用空端点满足工具链的
		// 接口探测,具体 socket 操作在 executor 侧无操作处理。
		return net.Addr{Domain: net.AFNetlink}, nil
	default:
		return net.Addr{}, errno.NotSupported
	}
}

func isIPv4Compatible(raw [16]byte) bool {
	for _, value := range raw[:12] {
		if value != 0 {
			return false
		}
	}
	return true
}

// encodeSockaddr 把地址编码为客体 sockaddr 并回写;返回编码长度。
func encodeSockaddr(ctx context.Context, s Syscalls, out uint64, outLen *uint64, addr net.Addr) error {
	switch addr.Domain {
	case net.AFUnix:
		buffer := make([]byte, sockaddrUnixSize)
		binary.LittleEndian.PutUint16(buffer[0:2], uint16(net.AFUnix))
		path := addr.Path
		if len(path) > 107 {
			return errno.NameTooLong
		}
		copy(buffer[2:], path)
		return writeSockaddr(ctx, s, out, outLen, buffer, uint64(2+len(path)))
	case net.AFInet:
		var buffer [sockaddrInetSize]byte
		binary.LittleEndian.PutUint16(buffer[0:2], uint16(net.AFInet))
		binary.BigEndian.PutUint16(buffer[2:4], addr.Port)
		if !addr.IP.IsValid() {
			addr.IP = net.LoopbackIPv4
		}
		copy(buffer[4:8], net.AddrIPv4Bytes(addr.IP))
		return writeSockaddr(ctx, s, out, outLen, buffer[:], sockaddrInetSize)
	default:
		return errno.NotSupported
	}
}

func writeSockaddr(ctx context.Context, s Syscalls, out uint64, outLen *uint64, buffer []byte, length uint64) error {
	if outLen != nil {
		var raw [4]byte
		if err := s.Memory.Read(ctx, *outLen, raw[:]); err != nil {
			return errno.Fault
		}
		declared := binary.LittleEndian.Uint32(raw[:])
		if uint64(declared) < length {
			// 客体缓冲不足:Linux 按声明长度截断写回;此处拒绝更
			// 明确——地址被完整截断时调用方无法使用。
			return errno.Invalid
		}
		binary.LittleEndian.PutUint32(raw[:], uint32(length))
		if err := s.Memory.Write(ctx, *outLen, raw[:]); err != nil {
			return errno.Fault
		}
	}
	if out != 0 {
		if err := s.Memory.Write(ctx, out, buffer); err != nil {
			return errno.Fault
		}
	}
	return nil
}

func indexByteNul(raw []byte) int {
	for index, b := range raw {
		if b == 0 {
			return index
		}
	}
	return -1
}

// socket(domain, type, protocol) 创建 socket(M02)。
func (s Syscalls) socket(ctx context.Context, call Call) Reply {
	if s.Sockets == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	typ := call.Args[1]
	fd, err := s.Sockets.Socket(call.Args[0], typ&sockTypeMask, call.Args[2],
		typ&sockNonBlock != 0, typ&sockCloexec != 0)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: fd}
}

// socketpair(domain, type, protocol, fds) 创建互联端点对(M03)。
func (s Syscalls) socketpair(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	typ := call.Args[1]
	first, second, err := s.Sockets.SocketPair(call.Args[0], typ&sockTypeMask, call.Args[2],
		typ&sockNonBlock != 0, typ&sockCloexec != 0)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	var encoded [8]byte
	binary.LittleEndian.PutUint32(encoded[0:4], uint32(first))
	binary.LittleEndian.PutUint32(encoded[4:8], uint32(second))
	if err := s.Memory.Write(ctx, call.Args[3], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

// bind(fd, addr, addrlen) 绑定(M03/M04/M05)。
func (s Syscalls) bind(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	addr, err := parseSockaddr(ctx, s, call.Args[1], call.Args[2])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := s.Sockets.Bind(ctx, call.Args[0], addr); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// listen(fd, backlog) 进入监听。
func (s Syscalls) listen(ctx context.Context, call Call) Reply {
	if s.Sockets == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	backlog := int(int32(call.Args[1]))
	if backlog < 0 {
		backlog = 0
	}
	if err := s.Sockets.Listen(call.Args[0], backlog); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// accept4(fd, addr, addrlen, flags) / accept(fd, addr, addrlen):
// 接受连接(M03/M04),可选回写对端地址。
func (s Syscalls) accept4(ctx context.Context, call Call) Reply {
	if s.Sockets == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[3]
	if flags&^(uint64(sockNonBlock|sockCloexec)) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	fd, err := s.Sockets.Accept(ctx, call.Args[0], flags&sockNonBlock != 0, flags&sockCloexec != 0)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if call.Args[1] != 0 && call.Args[2] != 0 {
		if addr, peerErr := s.Sockets.PeerName(fd); peerErr == nil {
			if encErr := encodeSockaddr(ctx, s, call.Args[1], &call.Args[2], addr); encErr != nil {
				// 地址回写失败不撤销已接受的连接(Linux 同语义:
				// accept 已完成)。
				_ = encErr
			}
		}
	}
	return Reply{Value: fd}
}

// connect(fd, addr, addrlen) 连接(M03/M04)。
func (s Syscalls) connect(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	addr, err := parseSockaddr(ctx, s, call.Args[1], call.Args[2])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := s.Sockets.Connect(ctx, call.Args[0], addr); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// sendto(fd, buf, len, flags, addr, addrlen) 投递(M05)。
func (s Syscalls) sendto(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[3]&^uint64(msgNosignal) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	count := call.Args[2]
	if count > s.bufferLimit() {
		return Reply{Errno: errno.Invalid}
	}
	data := make([]byte, count)
	if count > 0 {
		if err := s.Memory.Read(ctx, call.Args[1], data); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	var to *net.Addr
	if call.Args[4] != 0 {
		addr, err := parseSockaddr(ctx, s, call.Args[4], call.Args[5])
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		to = &addr
	}
	written, err := s.Sockets.SendTo(ctx, call.Args[0], data, to)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: uint64(written)}
}

// recvfrom(fd, buf, len, flags, srcaddr, addrlen) 接收(M05)。
func (s Syscalls) recvfrom(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[3]&^uint64(msgNosignal) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	count := call.Args[2]
	if count > s.bufferLimit() {
		return Reply{Errno: errno.Invalid}
	}
	buffer := make([]byte, count)
	loaded, from, err := s.Sockets.RecvFrom(ctx, call.Args[0], buffer)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if loaded > 0 {
		if err := s.Memory.Write(ctx, call.Args[1], buffer[:loaded]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	if call.Args[4] != 0 {
		if err := encodeSockaddr(ctx, s, call.Args[4], &call.Args[5], from); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	return Reply{Value: uint64(loaded)}
}

// shutdown(fd, how) 半关闭。
func (s Syscalls) shutdown(ctx context.Context, call Call) Reply {
	if s.Sockets == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if err := s.Sockets.Shutdown(call.Args[0], net.ShutdownHow(call.Args[1])); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// getsockname(fd, addr, addrlen) 读取本端地址。
func (s Syscalls) getsockname(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	addr, err := s.Sockets.SockName(call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := encodeSockaddr(ctx, s, call.Args[1], &call.Args[2], addr); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// getpeername(fd, addr, addrlen) 读取对端地址。
func (s Syscalls) getpeername(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	addr, err := s.Sockets.PeerName(call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := encodeSockaddr(ctx, s, call.Args[1], &call.Args[2], addr); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// getsockopt(fd, level, name, value, len) 读取选项(M06 子集)。
func (s Syscalls) getsockopt(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	value, err := s.Sockets.GetOption(call.Args[0], call.Args[1], call.Args[2])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(value))
	if call.Args[3] != 0 {
		if err := s.Memory.Write(ctx, call.Args[3], raw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	if call.Args[4] != 0 {
		var lenRaw [4]byte
		binary.LittleEndian.PutUint32(lenRaw[:], 4)
		if err := s.Memory.Write(ctx, call.Args[4], lenRaw[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

// setsockopt(fd, level, name, value, len) 设置选项(M06 子集)。
func (s Syscalls) setsockopt(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var raw [4]byte
	if call.Args[4] > 0 {
		if call.Args[4] > 4 {
			return Reply{Errno: errno.Invalid}
		}
		if err := s.Memory.Read(ctx, call.Args[3], raw[:call.Args[4]]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	if err := s.Sockets.SetOption(call.Args[0], call.Args[1], call.Args[2], uint64(binary.LittleEndian.Uint32(raw[:]))); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

// msghdr 是 asm-generic 布局(56 字节,7 个 8 字节字段)。
const msghdrSize = 56

// cmsg 头:size_t len + int level + int type,数据按 size_t 对齐。
const cmsghdrSize = 16

// SOL_SOCKET/SCM_RIGHTS。
const (
	solSocketLevel = 1
	scmRights      = 1
)

// sendmsg(fd, msghdr, flags) 发送(M08)。
func (s Syscalls) sendmsg(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[2]&^uint64(msgNosignal) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	header, buffers, incomingControl, code := s.sendMsghdr(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	var to *net.Addr
	if header.nameLen > 0 {
		addr, err := parseSockaddr(ctx, s, header.name, header.nameLen)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		to = &addr
	}
	handles, code := s.parseControl(ctx, incomingControl)
	if code != "" {
		return Reply{Errno: code}
	}
	written, err := s.Sockets.SendMsg(ctx, call.Args[0], buffers, to, handles)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: uint64(written)}
}

// recvmsg(fd, msghdr, flags) 接收(M08):数据按 iovec 分散展开,来源
// 地址与控制消息回写 msghdr。控制缓冲不足时按 Linux 置 MSG_CTRUNC
// 并归还未被接收方占有的 SCM_RIGHTS 引用。
func (s Syscalls) recvmsg(ctx context.Context, call Call) Reply {
	if s.Sockets == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	header, code := s.parseMsghdr(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	var iovecs []Iovec
	if header.iovLen > 0 {
		var iovCode errno.Code
		iovecs, iovCode = s.readIovecs(ctx, header.iov, header.iovLen)
		if iovCode != "" {
			return Reply{Errno: iovCode}
		}
	}
	length := uint64(0)
	for _, iov := range iovecs {
		length += iov.Length
	}
	if length > s.bufferLimit() {
		return Reply{Errno: errno.Invalid}
	}
	buffer := make([]byte, length)
	loaded, from, handles, err := s.Sockets.RecvMsg(ctx, call.Args[0], buffer)
	if err != nil && loaded == 0 {
		releaseHandles(handles)
		// 对端有序关闭:recvmsg 返回 0(与 read 的 EOF 一致)。
		if errors.Is(err, io.EOF) {
			return Reply{}
		}
		return Reply{Errno: errnoOf(err)}
	}
	// 分散写入各 iovec(stream 语义:读到的连续字节按段铺开)。
	offset := 0
	for _, iov := range iovecs {
		if offset >= loaded {
			break
		}
		count := min(int(iov.Length), loaded-offset)
		if count > 0 {
			if err := s.Memory.Write(ctx, iov.Base, buffer[offset:offset+count]); err != nil {
				releaseHandles(handles)
				return Reply{Errno: errno.Fault}
			}
			offset += count
		}
	}
	// 回写 msghdr:来源地址与控制消息。
	nameLen := uint64(0)
	if header.name != 0 && from != nil && from.Domain != 0 {
		if err := encodeSockaddr(ctx, s, header.name, nil, *from); err != nil {
			releaseHandles(handles)
			return Reply{Errno: errnoOf(err)}
		}
		if from.Domain == net.AFInet {
			nameLen = sockaddrInetSize
		} else {
			nameLen = uint64(2 + len(from.Path))
		}
	}
	controlLen := uint64(0)
	flags := header.flags
	if len(handles) > 0 {
		encoded, truncated, encErr := s.encodeRightsControl(ctx, header.controlSlot, header.controlLenDeclared, handles)
		if encErr != nil {
			releaseHandles(handles)
			return Reply{Errno: errnoOf(encErr)}
		}
		controlLen = encoded
		if truncated {
			flags |= msgCtrunc
		}
	}
	var out [msghdrSize]byte
	binary.LittleEndian.PutUint64(out[0:8], header.name)
	binary.LittleEndian.PutUint64(out[8:16], nameLen)
	binary.LittleEndian.PutUint64(out[16:24], header.iov)
	binary.LittleEndian.PutUint64(out[24:32], header.iovLen)
	binary.LittleEndian.PutUint64(out[32:40], header.controlSlot)
	binary.LittleEndian.PutUint64(out[40:48], controlLen)
	binary.LittleEndian.PutUint64(out[48:56], flags)
	if err := s.Memory.Write(ctx, call.Args[1], out[:]); err != nil {
		releaseHandles(handles)
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(loaded)}
}

// releaseHandles 归还未被安装的 SCM_RIGHTS 引用(M08)。
func releaseHandles(handles []any) {
	for _, handle := range handles {
		if released, ok := handle.(interface{ Release() error }); ok {
			_ = released.Release()
		}
	}
}

// msghdrView 是已解析的 msghdr 字段(nameLen/controlSlot/
// controlLenDeclared 供回写)。
type msghdrView struct {
	name    uint64
	nameLen uint64
	iov     uint64
	iovLen  uint64
	// controlSlot 是控制缓冲地址;controlLenDeclared 是调用方声明的
	// 容量(msg_controllen 字段值)。
	controlSlot        uint64
	controlLenDeclared uint64
	flags              uint64
}

// parseMsghdr 解析客体 msghdr(不读取 iovec 缓冲)。
func (s Syscalls) parseMsghdr(ctx context.Context, headerAddr uint64) (msghdrView, errno.Code) {
	var raw [msghdrSize]byte
	if err := s.Memory.Read(ctx, headerAddr, raw[:]); err != nil {
		return msghdrView{}, errno.Fault
	}
	view := msghdrView{
		name:               binary.LittleEndian.Uint64(raw[0:8]),
		nameLen:            binary.LittleEndian.Uint64(raw[8:16]),
		iov:                binary.LittleEndian.Uint64(raw[16:24]),
		iovLen:             binary.LittleEndian.Uint64(raw[24:32]),
		controlSlot:        binary.LittleEndian.Uint64(raw[32:40]),
		controlLenDeclared: binary.LittleEndian.Uint64(raw[40:48]),
		flags:              binary.LittleEndian.Uint64(raw[48:56]),
	}
	return view, ""
}

// sendMsghdr 解析客体 msghdr 与 iovec 向量(发送侧)。
func (s Syscalls) sendMsghdr(ctx context.Context, headerAddr uint64) (msghdrView, [][]byte, []byte, errno.Code) {
	view, code := s.parseMsghdr(ctx, headerAddr)
	if code != "" {
		return view, nil, nil, code
	}
	// 读取 iovec 向量与缓冲。
	var buffers [][]byte
	if view.iovLen > 0 {
		if view.iovLen > iovMax {
			return view, nil, nil, errno.Invalid
		}
		iovecs, code := s.readIovecs(ctx, view.iov, view.iovLen)
		if code != "" {
			return view, nil, nil, code
		}
		for _, iov := range iovecs {
			if iov.Length > s.bufferLimit() {
				return view, nil, nil, errno.Invalid
			}
			buffer := make([]byte, iov.Length)
			if iov.Length > 0 {
				if err := s.Memory.Read(ctx, iov.Base, buffer); err != nil {
					return view, nil, nil, errno.Fault
				}
			}
			buffers = append(buffers, buffer)
		}
	}
	// 读取控制缓冲。
	var control []byte
	if view.controlSlot != 0 && view.controlLenDeclared > 0 {
		if view.controlLenDeclared > s.bufferLimit() {
			return view, nil, nil, errno.Invalid
		}
		control = make([]byte, view.controlLenDeclared)
		if err := s.Memory.Read(ctx, view.controlSlot, control); err != nil {
			return view, nil, nil, errno.Fault
		}
	}
	return view, buffers, control, ""
}

// parseControl 解析 SCM_RIGHTS 控制消息为句柄请求(fd 编号)。
func (s Syscalls) parseControl(ctx context.Context, control []byte) ([]any, errno.Code) {
	if len(control) == 0 {
		return nil, ""
	}
	var handles []any
	offset := 0
	for offset+cmsghdrSize <= len(control) {
		length := binary.LittleEndian.Uint64(control[offset : offset+8])
		if length < cmsghdrSize || offset+int(length) > len(control) {
			return nil, errno.Invalid
		}
		level := binary.LittleEndian.Uint32(control[offset+8 : offset+12])
		kind := binary.LittleEndian.Uint32(control[offset+12 : offset+16])
		if level == solSocketLevel && kind == scmRights {
			payload := control[offset+cmsghdrSize : offset+int(length)]
			for i := 0; i+4 <= len(payload); i += 4 {
				handles = append(handles, uint64(binary.LittleEndian.Uint32(payload[i:i+4])))
			}
		} else {
			return nil, errno.NotSupported
		}
		offset += (int(length) + 7) &^ 7
	}
	return handles, ""
}

// encodeRightsControl 把句柄对象列表编码为 SCM_RIGHTS 控制消息写回
// 客体控制缓冲(msg_controllen 是 msghdr 内的字段值,容量即其当前
// 值;实际长度由 msghdr 整体回写);返回写回的 controllen 与是否
// 因容量不足截断。容量不足时按 Linux 语义丢弃控制消息(归还未安装
// 的引用),数据照常交付并由调用方置 MSG_CTRUNC。
func (s Syscalls) encodeRightsControl(ctx context.Context, controlSlot, declaredCapacity uint64, handles []any) (uint64, bool, error) {
	length := uint64(cmsghdrSize + 4*len(handles))
	if declaredCapacity < length || controlSlot == 0 {
		releaseHandles(handles)
		return 0, true, nil
	}
	payload := make([]byte, 4*len(handles))
	for index, handle := range handles {
		number, err := s.Sockets.InstallPassed(handle)
		if err != nil {
			// 安装失败:尚未安装的引用与已安装的槽位分别归位。
			releaseHandles(handles[index:])
			return 0, false, err
		}
		binary.LittleEndian.PutUint32(payload[index*4:], uint32(number))
	}
	padded := (length + 7) &^ 7
	buffer := make([]byte, padded)
	binary.LittleEndian.PutUint64(buffer[0:8], length)
	binary.LittleEndian.PutUint32(buffer[8:12], solSocketLevel)
	binary.LittleEndian.PutUint32(buffer[12:16], scmRights)
	copy(buffer[cmsghdrSize:], payload)
	if err := s.Memory.Write(ctx, controlSlot, buffer); err != nil {
		return 0, false, errno.Fault
	}
	return length, false, nil
}

// bufferLimit 返回缓冲上限(与 G03/G04 一致)。
func (s Syscalls) bufferLimit() uint64 {
	if s.BufferLimit != 0 {
		return s.BufferLimit
	}
	return DefaultBufferLimit
}
