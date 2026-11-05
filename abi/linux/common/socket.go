// socket.go 实现 socket 系统调用族(M02–M08):sockaddr 编解码与
// 全部 socket 调用的端口分派。端点与命名空间由 Sockets 端口实现
// (executor 侧绑定内核网络命名空间与描述符表)。
package common

import (
	"context"
	"encoding/binary"

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

// sockaddrInetSize 是 sockaddr_in 的字节数(asm-generic)。
const sockaddrInetSize = 16

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

	Shutdown(fd uint64, how net.ShutdownHow) error

	SockName(fd uint64) (net.Addr, error)

	PeerName(fd uint64) (net.Addr, error)

	GetOption(fd uint64, level, name uint64) (uint64, error)

	SetOption(fd uint64, level, name, value uint64) error
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
	default:
		return net.Addr{}, errno.NotSupported
	}
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

// bufferLimit 返回缓冲上限(与 G03/G04 一致)。
func (s Syscalls) bufferLimit() uint64 {
	if s.BufferLimit != 0 {
		return s.BufferLimit
	}
	return DefaultBufferLimit
}
