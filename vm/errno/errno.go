package errno

import (
	"fmt"
	"slices"
)

type Code string

const (
	NotImplemented     Code = "ENOSYS：接口尚未实现"
	NotSupported       Code = "ENOTSUP：能力明确不支持"
	NetworkUnreachable Code = "ENETUNREACH：网络不可达"
	Invalid            Code = "EINVAL：参数无效"
	NoProcess          Code = "ESRCH：进程不存在"
	NoChild            Code = "ECHILD：没有对应子进程"
	Busy               Code = "EBUSY：状态不允许此操作"
	Limit              Code = "EAGAIN：资源预算已耗尽"
	NotFound           Code = "ENOENT：可执行程序不存在"

	Permission   Code = "EACCES：权限不足"
	NotPermitted Code = "EPERM：操作不被允许"
	Exist        Code = "EEXIST：目标已存在"
	NotDirectory Code = "ENOTDIR：路径前缀不是目录"
	IsDirectory  Code = "EISDIR：目标是目录"
	NotEmpty     Code = "ENOTEMPTY：目录非空"
	BadHandle    Code = "EBADF：描述符无效"
	BrokenPipe   Code = "EPIPE：管道对端已关闭"
	NameTooLong  Code = "ENAMETOOLONG：客体路径超长"
	TooBig       Code = "E2BIG：参数或缓冲区列表过长"
	ReadOnlyFS   Code = "EROFS：存储只读"
	LinkLoop     Code = "ELOOP：符号链接解析过深"
	NoDevice     Code = "ENXIO：设备不存在或没有控制终端"
	NoSpace      Code = "ENOSPC：客体存储预算耗尽"
	NoMemory     Code = "ENOMEM：客体地址空间或内存预算耗尽"

	CrossDevice Code = "EXDEV：跨挂载操作不被支持"
	Cancelled   Code = "ECANCELED：操作被取消"
	Interrupted Code = "EINTR：等待被信号打断"
	WouldBlock  Code = "EWOULDBLOCK：非阻塞操作暂不可用"
	Range       Code = "ERANGE：结果超出客体表示范围"
	Overflow    Code = "EOVERFLOW：数值溢出"
	TimedOut    Code = "ETIMEDOUT：等待超时"
	NoExec      Code = "ENOEXEC：文件不是可执行格式"

	Version Code = "EPROTO：版本不匹配或数据校验失败"

	Fault Code = "EFAULT：客体地址或长度非法"

	IO Code = "EIO：描述符读写失败"

	Seek Code = "ESPIPE：描述符不支持定位"

	// 网络族(M 组):asm-generic 数值即 Linux 数值。
	AddressInUse    Code = "EADDRINUSE：地址已被占用"
	AddressNotAvail Code = "EADDRNOTAVAIL：地址不可用"
	IsConn          Code = "EISCONN：套接字已连接"
	NotSock         Code = "ENOTSOCK：描述符不是套接字"
	NotConn         Code = "ENOTCONN：套接字未连接"
	ConnRefused     Code = "ECONNREFUSED：目标拒绝连接"
	ConnReset       Code = "ECONNRESET：连接被对端重置"
	ConnAborted     Code = "ECONNABORTED：软件导致的连接中止"
	Already         Code = "EALREADY：操作已在进行"
	InProgress      Code = "EINPROGRESS：操作正在进行"
	MessageSize     Code = "EMSGSIZE：报文过长"
	HostUnreachable Code = "EHOSTUNREACH：主机不可达"
	DestAddrReq     Code = "EDESTADDRREQ：缺少目标地址"
)

var declared = []Code{
	NotImplemented, NotSupported, NetworkUnreachable, Invalid, NoProcess, NoChild, Busy, Limit, NotFound,
	Permission, NotPermitted, Exist, NotDirectory, IsDirectory, NotEmpty, BadHandle, BrokenPipe, NameTooLong, ReadOnlyFS,
	LinkLoop, NoDevice, NoSpace, NoMemory, CrossDevice, Cancelled, Interrupted, WouldBlock, Range, Overflow,
	TimedOut, NoExec, Version, Fault, IO, Seek, TooBig,
	AddressInUse, AddressNotAvail, IsConn, NotConn, NotSock, ConnRefused, ConnReset, ConnAborted, Already,
	InProgress, MessageSize, HostUnreachable, DestAddrReq,
}

func Codes() []Code { return slices.Clone(declared) }

func (c Code) Error() string { return string(c) }

const wasiABIVersion = "abi-wasi-1"

var wasiNumbers = map[Code]uint16{
	Invalid:            28,
	NoProcess:          71,
	NoChild:            12,
	Busy:               10,
	Limit:              6,
	NotFound:           44,
	NotImplemented:     52,
	NotSupported:       58,
	NetworkUnreachable: 40,
	Permission:         2,
	NotPermitted:       63,
	Exist:              20,
	NotDirectory:       54,
	IsDirectory:        31,
	NotEmpty:           55,
	CrossDevice:        18,

	Fault:       21,
	IO:          29,
	BadHandle:   8,
	BrokenPipe:  64,
	NameTooLong: 37,
	ReadOnlyFS:  69,
	LinkLoop:    32,
	NoDevice:    60,
	NoSpace:     51,
	NoMemory:    48,
	Seek:        70,
	Cancelled:   11,
	Interrupted: 27,
	WouldBlock:  6,
	Range:       68,
	Overflow:    61,
	TimedOut:    73,
	NoExec:      45,
	TooBig:      57,
	Version:     75,
	// 网络族(M 组;Go wasip1 表数值)。
	AddressInUse:    3,
	AddressNotAvail: 4,
	IsConn:          30,
	NotConn:         53,
	NotSock:         57,
	ConnRefused:     14,
	ConnReset:       15,
	ConnAborted:     13,
	Already:         7,
	InProgress:      26,
	MessageSize:     35,
	HostUnreachable: 23,
	DestAddrReq:     17,
}

func WASI(code Code) (uint16, string) {
	if n, ok := wasiNumbers[code]; ok {
		return n, wasiABIVersion
	}
	return wasiNumbers[NotImplemented], wasiABIVersion
}

const linuxABIVersion = "abi-linux-asm-generic"

var linuxNumbers = map[Code]int32{
	Fault:              14,
	Permission:         13,
	NotPermitted:       1,
	NotFound:           2,
	NoProcess:          3,
	Interrupted:        4,
	IO:                 5,
	Seek:               29,
	NoDevice:           6,
	NoExec:             8,
	BadHandle:          9,
	NoChild:            10,
	Limit:              11,
	WouldBlock:         11,
	Busy:               16,
	Exist:              17,
	CrossDevice:        18,
	NotDirectory:       20,
	IsDirectory:        21,
	Invalid:            22,
	NoSpace:            28,
	NoMemory:           12,
	TooBig:             7,
	ReadOnlyFS:         30,
	BrokenPipe:         32,
	Range:              34,
	NameTooLong:        36,
	NotImplemented:     38,
	NotEmpty:           39,
	LinkLoop:           40,
	Version:            71,
	Overflow:           75,
	NotSupported:       95,
	NetworkUnreachable: 101,
	TimedOut:           110,
	Cancelled:          125,
	AddressInUse:       98,
	AddressNotAvail:    99,
	IsConn:             106,
	NotSock:            88,
	NotConn:            107,
	ConnRefused:        111,
	ConnReset:          104,
	ConnAborted:        103,
	Already:            114,
	InProgress:         115,
	MessageSize:        90,
	HostUnreachable:    113,
	DestAddrReq:        89,
}

func Linux(code Code) (int32, string) {
	if number, ok := linuxNumbers[code]; ok {
		return number, linuxABIVersion
	}
	return linuxNumbers[NotImplemented], linuxABIVersion
}

func Wrap(err error, context string) error { return fmt.Errorf("%s: %w", context, err) }
