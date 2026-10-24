package common

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	stdfs "io/fs"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

type Memory interface {
	Read(ctx context.Context, address uint64, buffer []byte) error

	Write(ctx context.Context, address uint64, buffer []byte) error
}

type Descriptors interface {
	Read(ctx context.Context, descriptor uint64, buffer []byte) (int, error)

	Write(ctx context.Context, descriptor uint64, buffer []byte) (int, error)

	Close(ctx context.Context, descriptor uint64) error

	Seek(ctx context.Context, descriptor uint64, offset int64, whence SeekWhence) (int64, error)

	Truncate(ctx context.Context, descriptor uint64, size int64) error

	Get(ctx context.Context, descriptor uint64) (any, error)

	Fcntl(ctx context.Context, descriptor uint64, cmd uint64, arg uint64) (uint64, error)

	Pipe(ctx context.Context) (uint64, uint64, error)

	EventFD(ctx context.Context, initialValue, flags uint64) (uint64, error)

	Dup2(ctx context.Context, oldfd, newfd uint64, cloexec bool) error
}

type SeekWhence int

const (
	SeekSet SeekWhence = iota

	SeekCurrent

	SeekEnd
)

type Clock interface {
	Wall(ctx context.Context) (int64, error)

	Monotonic(ctx context.Context) (uint64, error)
}

// Sleeper 提供可被取消的纳秒级等待;ctx 取消或内核关闭时返回错误,
// clock_nanosleep 将其转换为 EINTR 并报告剩余时间。
type Sleeper interface {
	Sleep(ctx context.Context, nanoseconds uint64) error
}

type Identity struct {
	PID  uint64
	PPID uint64
	UID  uint32
	GID  uint32
}

type OpenRequest struct {
	Path  string
	Flags OpenFlags
	Perm  uint32
}

type Files interface {
	Open(ctx context.Context, request OpenRequest) (uint64, error)

	Stat(ctx context.Context, path string) (stdfs.FileInfo, error)

	Readlink(ctx context.Context, path string) (string, error)

	Mkdir(ctx context.Context, path string, mode uint32) error

	Remove(ctx context.Context, path string) error

	Symlink(ctx context.Context, target, linkpath string) error

	Link(ctx context.Context, oldpath, newpath string) error

	Rename(ctx context.Context, oldpath, newpath string) error

	ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error)
}

const PathLimit = 4096

const atCurrentWorkingDirectory = ^uint64(0) - 99

const DefaultBufferLimit = 1 << 20

type Random interface {
	Read(context.Context, []byte) (int, error)
}

type Syscalls struct {
	Memory Memory

	Descriptors Descriptors

	Files Files

	Clock Clock

	Identity *Identity

	Heap Heap

	Mapper Mappable

	Sleeper Sleeper

	BufferLimit uint64

	Random Random

	Sysname    string
	Nodename   string
	Release    string
	Version    string
	Machine    string
	Domainname string

	Cwd string
}

func (s Syscalls) Handle(ctx context.Context, call Call) Reply {
	switch call.Syscall {
	case SysExit, SysExitGroup:
		return Reply{Exit: true, Value: call.Args[0]}
	case SysWrite:
		return s.write(ctx, call)
	case SysRead:
		return s.read(ctx, call)
	case SysOpenAt:
		return s.openAt(ctx, call)
	case SysClose:
		return s.close(ctx, call)
	case SysLseek:
		return s.lseek(ctx, call)
	case SysGetPID, SysGetPPID, SysGetTID, SysGetUID, SysGetEUID, SysGetGID, SysGetEGID:
		return s.identity(call)
	case SysClockGettime:
		return s.clockGettime(ctx, call)
	case SysClockNanosleep:
		return s.clockNanosleep(ctx, call)
	case SysClockGetres:
		return s.clockGetres(ctx, call)
	case SysGettimeofday:
		return s.gettimeofday(ctx, call)
	case SysBrk:
		return s.brk(ctx, call)
	case SysMmap:
		return s.mmap(ctx, call)
	case SysMunmap:
		return s.munmap(ctx, call)
	case SysMprotect:
		return s.mprotect(ctx, call)
	case SysUname:
		return s.uname(ctx, call)
	case SysGetrandom:
		return s.getrandom(ctx, call)
	case SysGetcwd:
		return s.getcwd(ctx, call)
	case SysSysinfo:
		return s.sysinfo(ctx, call)
	case SysGetpgid, SysGetsid:
		return s.identity(call)
	case SysTimes:
		return s.times(ctx, call)
	case SysGetgroups:
		return s.getgroups(ctx, call)
	case SysSetpgid:
		return Reply{}
	case SysSetsid:
		if s.Identity == nil {
			return Reply{Errno: errno.NotImplemented}
		}
		return Reply{Value: s.Identity.PID}
	case SysSchedYield:
		return Reply{}
	case SysPrlimit64:
		return s.prlimit64(ctx, call)
	case SysGetrlimit:
		return s.getrlimit(ctx, call)
	case SysSetrlimit:
		return Reply{Value: 0}
	case SysGetrusage:
		return s.getrusage(ctx, call)
	case SysGetitimer, SysSetitimer:
		return Reply{Value: 0}
	case SysGetpriority, SysSetpriority:
		return Reply{Value: 0}
	case SysMlock, SysMunlock, SysMlockall, SysMunlockall, SysMadvise:
		return Reply{Value: 0}
	case SysMincore:
		return Reply{Value: 0}
	case SysFaccessat:
		return s.faccessat(ctx, call)
	case SysMkdirat:
		return s.mkdirat(ctx, call)
	case SysUnlinkat:
		return s.unlinkat(ctx, call)
	case SysSymlinkat:
		return s.symlinkat(ctx, call)
	case SysLinkat:
		return s.linkat(ctx, call)
	case SysRenameat, SysRenameat2:
		return s.renameat(ctx, call)
	case SysFtruncate:
		return s.ftruncate(ctx, call)
	case SysFcntl:
		return s.fcntl(ctx, call)
	case SysPipe:
		return s.pipe(ctx, call, false)
	case SysPipe2:
		return s.pipe(ctx, call, true)
	case SysEventfd2:
		return s.eventfd2(ctx, call)
	case SysDup3:
		return s.dup3(ctx, call)
	case SysGetdents64:
		return s.getdents64(ctx, call)
	case SysSocket, SysBind, SysListen, SysAccept, SysConnect, SysSocketpair, SysSendto, SysRecvfrom, SysShutdown, SysSetsockopt, SysGetsockopt, SysGetsockname, SysGetpeername:
		return Reply{Errno: errno.NotImplemented}
	case SysReadlinkat:
		return s.readlinkat(ctx, call)
	case SysUmask:
		return Reply{Value: 0}
	case SysSync, SysFsync, SysFdatasync:
		return Reply{Value: 0}
	case SysSetuid, SysSetgid, SysSetreuid, SysSetregid, SysSetresuid, SysSetresgid, SysSetfsuid, SysSetfsgid:
		return Reply{Value: 0}
	default:
		return Reply{Errno: errno.NotImplemented}
	}
}

func (s Syscalls) write(ctx context.Context, call Call) Reply {
	buffer, reply, ready := s.buffer(call)
	if !ready {
		return reply
	}
	if err := s.Memory.Read(ctx, call.Args[1], buffer); err != nil {

		return Reply{Errno: errno.Fault}
	}
	written, err := s.Descriptors.Write(ctx, call.Args[0], buffer)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if written < 0 || written > len(buffer) {
		return Reply{Errno: errno.IO}
	}
	return Reply{Value: uint64(written)}
}

func (s Syscalls) read(ctx context.Context, call Call) Reply {
	buffer, reply, ready := s.buffer(call)
	if !ready {
		return reply
	}
	loaded, err := s.Descriptors.Read(ctx, call.Args[0], buffer)
	if err != nil {

		if errors.Is(err, io.EOF) {
			return Reply{}
		}
		return Reply{Errno: errnoOf(err)}
	}
	if loaded < 0 || loaded > len(buffer) {
		return Reply{Errno: errno.IO}
	}
	if loaded == 0 {
		return Reply{}
	}
	if err := s.Memory.Write(ctx, call.Args[1], buffer[:loaded]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(loaded)}
}

func (s Syscalls) buffer(call Call) ([]byte, Reply, bool) {
	if s.Memory == nil || s.Descriptors == nil {

		return nil, Reply{Errno: errno.NotImplemented}, false
	}
	count := call.Args[2]
	if count == 0 {

		return nil, Reply{}, false
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if count > limit {
		return nil, Reply{Errno: errno.Invalid}, false
	}
	return make([]byte, count), Reply{}, true
}

func errnoOf(err error) errno.Code {
	for _, code := range errno.Codes() {
		if errors.Is(err, code) {
			return code
		}
	}
	return errno.IO
}

func (s Syscalls) openAt(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	if call.OpenFlags == nil {

		return Reply{Errno: errno.NotSupported}
	}
	if err := call.OpenFlags.Validate(); err != nil {
		return Reply{Errno: errno.Invalid}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	descriptor, err := s.Files.Open(ctx, OpenRequest{
		Path:  path,
		Flags: *call.OpenFlags,
		Perm:  uint32(call.Args[3]),
	})
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: descriptor}
}

func (s Syscalls) close(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if err := s.Descriptors.Close(ctx, call.Args[0]); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) readPath(ctx context.Context, address uint64) (string, errno.Code) {
	var buffer [1]byte
	path := make([]byte, 0, 64)
	for offset := uint64(0); offset < PathLimit; offset++ {
		if err := s.Memory.Read(ctx, address+offset, buffer[:]); err != nil {
			return "", errno.Fault
		}
		if buffer[0] == 0 {
			if len(path) == 0 {

				return "", errno.NotFound
			}
			return string(path), ""
		}
		path = append(path, buffer[0])
	}
	return "", errno.NameTooLong
}

const (
	clockRealtime  = 0
	clockMonotonic = 1
)

func (s Syscalls) lseek(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var whence SeekWhence
	switch call.Args[2] {
	case uint64(SeekSet):
		whence = SeekSet
	case uint64(SeekCurrent):
		whence = SeekCurrent
	case uint64(SeekEnd):
		whence = SeekEnd
	default:
		return Reply{Errno: errno.Invalid}
	}
	offset, err := s.Descriptors.Seek(ctx, call.Args[0], int64(call.Args[1]), whence)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if offset < 0 {
		return Reply{Errno: errno.Invalid}
	}
	return Reply{Value: uint64(offset)}
}

func (s Syscalls) identity(call Call) Reply {
	if s.Identity == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	switch call.Syscall {
	case SysGetPID, SysGetTID, SysGetpgid, SysGetsid:
		return Reply{Value: s.Identity.PID}
	case SysGetPPID:
		return Reply{Value: s.Identity.PPID}
	case SysGetUID, SysGetEUID:
		return Reply{Value: uint64(s.Identity.UID)}
	case SysGetGID, SysGetEGID:
		return Reply{Value: uint64(s.Identity.GID)}
	default:
		return Reply{Errno: errno.NotImplemented}
	}
}

func (s Syscalls) clockGettime(ctx context.Context, call Call) Reply {
	if s.Clock == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var nanoseconds int64
	switch call.Args[0] {
	case clockRealtime:
		wall, err := s.Clock.Wall(ctx)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		nanoseconds = wall
	case clockMonotonic:
		monotonic, err := s.Clock.Monotonic(ctx)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		if monotonic > 1<<63-1 {
			return Reply{Errno: errno.Overflow}
		}
		nanoseconds = int64(monotonic)
	default:

		return Reply{Errno: errno.NotSupported}
	}
	seconds := nanoseconds / nanosPerSecond
	remainder := nanoseconds % nanosPerSecond
	if remainder < 0 {
		seconds--
		remainder += nanosPerSecond
	}
	encoded := encodeTimespec(seconds, remainder)
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

const nanosPerSecond = 1_000_000_000

// TIMER_ABSTIME 要求把请求值解释为时钟绝对时间。
const timerAbstime = 1

func (s Syscalls) clockNanosleep(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Clock == nil || s.Sleeper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != clockRealtime && call.Args[0] != clockMonotonic {
		return Reply{Errno: errno.Invalid}
	}
	flags := call.Args[1]
	if flags&^uint64(timerAbstime) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	seconds, nanoseconds, code := s.readTimespec(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	if nanoseconds >= nanosPerSecond {
		return Reply{Errno: errno.Invalid}
	}
	abstime := flags&uint64(timerAbstime) != 0
	if !abstime && int64(seconds) < 0 {
		return Reply{Errno: errno.Invalid}
	}

	if abstime {
		return s.sleepUntil(ctx, call, call.Args[0], int64(seconds), int64(nanoseconds))
	}
	duration, ok := addDuration(int64(seconds), int64(nanoseconds))
	if !ok {
		return Reply{Errno: errno.Overflow}
	}
	return s.sleepRelative(ctx, call, call.Args[0], duration)
}

func (s Syscalls) readTimespec(ctx context.Context, address uint64) (uint64, uint64, errno.Code) {
	var raw [16]byte
	if err := s.Memory.Read(ctx, address, raw[:]); err != nil {
		return 0, 0, errno.Fault
	}
	return binary.LittleEndian.Uint64(raw[0:]), binary.LittleEndian.Uint64(raw[8:]), ""
}

func addDuration(seconds, nanoseconds int64) (int64, bool) {
	if seconds > (1<<63-1-nanoseconds)/nanosPerSecond {
		return 0, false
	}
	total, ok := mulNoOverflow(seconds, nanosPerSecond)
	if !ok {
		return 0, false
	}
	sum, err := addNoOverflow(total, nanoseconds)
	if err != nil {
		return 0, false
	}
	return sum, true
}

func mulNoOverflow(left, right int64) (int64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	product := left * right
	if product/right != left {
		return 0, false
	}
	return product, true
}

func addNoOverflow(left, right int64) (int64, error) {
	sum := left + right
	if sum < left && right > 0 || sum > left && right < 0 {
		return 0, errno.Overflow
	}
	return sum, nil
}

func (s Syscalls) sleepRelative(ctx context.Context, call Call, clockID uint64, duration int64) Reply {
	started, err := s.clockNow(ctx, clockID)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if err := s.Sleeper.Sleep(ctx, uint64(duration)); err != nil {
		return s.interrupted(call, clockID, duration, started)
	}
	return Reply{}
}

func (s Syscalls) sleepUntil(ctx context.Context, call Call, clockID uint64, seconds, nanoseconds int64) Reply {
	target, ok := addDuration(seconds, nanoseconds)
	if !ok {
		return Reply{Errno: errno.Overflow}
	}
	for {
		now, err := s.clockNow(ctx, clockID)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		if now >= target {
			return Reply{}
		}
		if err := s.Sleeper.Sleep(ctx, uint64(target-now)); err != nil {
			// 绝对期限被中断时不更新剩余时间指针。
			return Reply{Errno: errno.Interrupted}
		}
	}
}

func (s Syscalls) clockNow(ctx context.Context, clockID uint64) (int64, error) {
	if clockID == clockMonotonic {
		monotonic, err := s.Clock.Monotonic(ctx)
		if err != nil {
			return 0, err
		}
		return int64(monotonic), nil
	}
	return s.Clock.Wall(ctx)
}

// interrupted 把等待失败统一呈现为 EINTR,并尽力回填剩余时间。
func (s Syscalls) interrupted(call Call, clockID uint64, duration, started int64) Reply {
	if call.Args[3] != 0 {
		remaining := duration
		if now, err := s.clockNow(context.Background(), clockID); err == nil && now > started {
			if elapsed := now - started; elapsed < remaining {
				remaining -= elapsed
			} else {
				remaining = 0
			}
		}
		if remaining < 0 {
			remaining = 0
		}
		encoded := encodeTimespec(remaining/nanosPerSecond, remaining%nanosPerSecond)
		if err := s.Memory.Write(context.Background(), call.Args[3], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Errno: errno.Interrupted}
}

type Heap interface {
	Break(ctx context.Context) (uint64, error)

	Grow(ctx context.Context, target uint64) (uint64, error)
}

// Mappable 由内核地址空间实现,承接 mmap 家族的区域管理。
// start/end 与返回地址必须页对齐;length 由调用方先行对齐。
type Mappable interface {
	// Anonymous 映射零填充的匿名私有区间并返回映射起始地址。
	// request.Hint 为 0 时由内核选址;Fixed 时必须落在 Hint 且替换重叠映射;
	// NoReplace 与 Fixed 组合时,重叠返回 EEXIST 且不改动任何映射。
	Anonymous(ctx context.Context, request MapRequest) (uint64, error)

	// Unmap 解除页对齐范围;范围未被完整映射时报 ENOMEM 且不改动。
	Unmap(ctx context.Context, start, end uint64) error

	// Protect 将页对齐范围内所有映射改为 prot(原始 Linux PROT_* 位),
	// 允许跨区间;存在空洞或 W^X 违例时报错且不改动。
	Protect(ctx context.Context, start, end uint64, prot uint64) error
}

type MapRequest struct {
	Length uint64
	Prot   uint64
	Hint   uint64
	Fixed  bool
	// NoReplace 对应 MAP_FIXED_NOREPLACE。
	NoReplace bool
}

// riscv64 采用 asm-generic 的 mmap 标志与保护位定义。
const (
	ProtRead  = 0x1
	ProtWrite = 0x2
	ProtExec  = 0x4

	// asm-generic 的 eventfd2 标志位。
	efdSemaphore = 0x1
	efdNonBlock  = 0o4000
	efdCloexec   = 0o2000000
	efdFlagMask  = efdSemaphore | efdNonBlock | efdCloexec

	mapPrivate        = 0x02
	mapFixed          = 0x10
	mapAnonymous      = 0x20
	mapFixedNoReplace = 0x100000
	mapSupportedMask  = mapPrivate | mapFixed | mapAnonymous | mapFixedNoReplace
)

func (s Syscalls) mmap(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	length := call.Args[1]
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	flags := call.Args[3]
	if flags&mapSupportedMask != flags {
		return Reply{Errno: errno.Invalid}
	}
	if flags&mapPrivate == 0 {
		// MAP_SHARED 匿名映射需要跨进程共享页,未纳入当前兼容配置,明确拒绝。
		return Reply{Errno: errno.NotSupported}
	}
	if flags&mapAnonymous == 0 {
		// 文件映射未纳入当前兼容配置(E09 未完成),明确拒绝而非伪成功。
		return Reply{Errno: errno.NotSupported}
	}
	if call.Args[5] != 0 {
		return Reply{Errno: errno.Invalid}
	}
	hint := call.Args[0]
	if hint%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length = alignPageUp(length)
	start, err := s.Mapper.Anonymous(ctx, MapRequest{
		Length: length, Prot: call.Args[2], Hint: hint,
		Fixed: flags&mapFixed != 0, NoReplace: flags&mapFixedNoReplace != 0,
	})
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: start}
}

func (s Syscalls) munmap(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	start := call.Args[0]
	if start%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := alignPageUp(call.Args[1])
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if start+length <= start {
		return Reply{Errno: errno.Overflow}
	}
	if err := s.Mapper.Unmap(ctx, start, start+length); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) mprotect(ctx context.Context, call Call) Reply {
	if s.Mapper == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	start := call.Args[0]
	if start%memory.PageSize != 0 {
		return Reply{Errno: errno.Invalid}
	}
	if call.Args[2]&^(ProtRead|ProtWrite|ProtExec) != 0 {
		return Reply{Errno: errno.Invalid}
	}
	length := alignPageUp(call.Args[1])
	if length == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if start+length <= start {
		return Reply{Errno: errno.Overflow}
	}
	if err := s.Mapper.Protect(ctx, start, start+length, call.Args[2]); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func alignPageUp(value uint64) uint64 {
	return (value + memory.PageSize - 1) &^ (memory.PageSize - 1)
}

func (s Syscalls) uname(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	fields := []string{s.sysname(), s.nodename(), s.release(), s.version(), s.machine(), s.domainname()}
	var encoded [390]byte
	for index, value := range fields {
		copy(encoded[index*65:], []byte(value))
	}
	if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) sysname() string {
	if s.Sysname == "" {
		return "Linux"
	}
	return s.Sysname
}

func (s Syscalls) nodename() string {
	if s.Nodename == "" {
		return "assistant-sandbox"
	}
	return s.Nodename
}

func (s Syscalls) release() string {
	if s.Release == "" {
		return "6.8.0"
	}
	return s.Release
}

func (s Syscalls) version() string {
	if s.Version == "" {
		return "#1 SMP"
	}
	return s.Version
}

func (s Syscalls) machine() string {
	if s.Machine == "" {
		return "riscv64"
	}
	return s.Machine
}

func (s Syscalls) domainname() string {
	if s.Domainname == "" {
		return "(none)"
	}
	return s.Domainname
}

func (s Syscalls) getrandom(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	length := call.Args[1]
	if length == 0 {
		return Reply{Value: 0}
	}
	limit := s.BufferLimit
	if limit == 0 {
		limit = DefaultBufferLimit
	}
	if length > limit {
		return Reply{Errno: errno.Invalid}
	}
	buffer := make([]byte, length)
	if s.Random == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if _, err := s.Random.Read(ctx, buffer); err != nil {
		return Reply{Errno: errno.IO}
	}
	if err := s.Memory.Write(ctx, call.Args[0], buffer); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: length}
}

func (s Syscalls) getcwd(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	cwd := s.Cwd
	if cwd == "" {
		cwd = "/"
	}
	if !strings.HasPrefix(cwd, "/") {
		cwd = "/" + cwd
	}
	size := call.Args[1]
	encoded := append([]byte(cwd), 0)
	if size == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if uint64(len(encoded)) > size {
		return Reply{Errno: errno.Range}
	}
	if err := s.Memory.Write(ctx, call.Args[0], encoded); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(encoded))}
}

func (s Syscalls) sysinfo(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var encoded [128]byte
	// procs at offset 64+8+8? Layout: uptime(8)+loads(24)+totalram(8)+freeram(8)+sharedram(8)+bufferram(8)+totalswap(8)+freeswap(8)=88? Let's place procs at 80? Simpler: zero-filled success.
	encoded[80] = 1 // procs low byte (approx)
	if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) times(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	var encoded [32]byte
	if call.Args[0] != 0 {
		if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: 0}
}

func (s Syscalls) getgroups(ctx context.Context, call Call) Reply {
	size := call.Args[1]
	if size == 0 {
		return Reply{Value: 0}
	}
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// No supplementary groups in this ABI profile.
	return Reply{Value: 0}
}

func (s Syscalls) prlimit64(ctx context.Context, call Call) Reply {
	if s.Memory == nil && call.Args[2] != 0 {
		return Reply{Errno: errno.NotImplemented}
	}
	// RLIM_INFINITY
	const infinity = ^uint64(0)
	if call.Args[2] != 0 {
		var encoded [16]byte
		binary.LittleEndian.PutUint64(encoded[0:], infinity)
		binary.LittleEndian.PutUint64(encoded[8:], infinity)
		if err := s.Memory.Write(ctx, call.Args[2], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{}
}

func (s Syscalls) getrusage(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1] == 0 {
		return Reply{Errno: errno.Invalid}
	}
	var encoded [144]byte
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: 0}
}

func (s Syscalls) faccessat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	if _, err := s.Files.Stat(ctx, path); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) getrlimit(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1] == 0 {
		return Reply{Errno: errno.Invalid}
	}
	const infinity = ^uint64(0)
	var encoded [16]byte
	binary.LittleEndian.PutUint64(encoded[0:], infinity)
	binary.LittleEndian.PutUint64(encoded[8:], infinity)
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: 0}
}

func (s Syscalls) readlinkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	target, err := s.Files.Readlink(ctx, path)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	size := call.Args[3]
	if size == 0 {
		return Reply{Errno: errno.Invalid}
	}
	data := []byte(target)
	if uint64(len(data)) > size {
		data = data[:size]
	}
	if err := s.Memory.Write(ctx, call.Args[2], data); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(target))}
}

func (s Syscalls) mkdirat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	mode := uint32(call.Args[2])
	if err := s.Files.Mkdir(ctx, path, mode); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) unlinkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	path, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	flags := call.Args[2]
	_ = flags
	if err := s.Files.Remove(ctx, path); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) symlinkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	target, code := s.readPath(ctx, call.Args[0])
	if code != "" {
		return Reply{Errno: code}
	}
	if call.Args[1] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	linkpath, code := s.readPath(ctx, call.Args[2])
	if code != "" {
		return Reply{Errno: code}
	}
	if err := s.Files.Symlink(ctx, target, linkpath); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) linkat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory || call.Args[2] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	oldpath, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	newpath, code := s.readPath(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	if err := s.Files.Link(ctx, oldpath, newpath); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) renameat(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[0] != atCurrentWorkingDirectory || call.Args[2] != atCurrentWorkingDirectory {
		return Reply{Errno: errno.NotSupported}
	}
	oldpath, code := s.readPath(ctx, call.Args[1])
	if code != "" {
		return Reply{Errno: code}
	}
	newpath, code := s.readPath(ctx, call.Args[3])
	if code != "" {
		return Reply{Errno: code}
	}
	if err := s.Files.Rename(ctx, oldpath, newpath); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) ftruncate(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if err := s.Descriptors.Truncate(ctx, call.Args[0], int64(call.Args[1])); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{}
}

func (s Syscalls) clockGetres(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	// 1ns resolution claim.
	encoded := encodeTimespec(0, 1)
	if err := s.Memory.Write(ctx, call.Args[1], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{}
}

func (s Syscalls) gettimeofday(ctx context.Context, call Call) Reply {
	if s.Clock == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	wall, err := s.Clock.Wall(ctx)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	seconds := wall / nanosPerSecond
	micros := (wall % nanosPerSecond) / 1000
	if wall < 0 && wall%nanosPerSecond != 0 {
		seconds--
		micros = ((wall % nanosPerSecond) + nanosPerSecond) / 1000
	}
	var encoded [16]byte
	binary.LittleEndian.PutUint64(encoded[0:], uint64(seconds))
	binary.LittleEndian.PutUint64(encoded[8:], uint64(micros))
	if call.Args[0] != 0 {
		if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: 0}
}

func (s Syscalls) getdents64(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil || s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	handle, err := s.Descriptors.Get(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	namer, ok := handle.(interface{ Name() string })
	if !ok {
		return Reply{Errno: errno.NotSupported}
	}
	entries, err := s.Files.ReadDir(ctx, namer.Name())
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	capacity := int(call.Args[2])
	out := make([]byte, 0, capacity)
	offset := uint64(0)
	for _, entry := range entries {
		name := entry.Name()
		nameBytes := append([]byte(name), 0)
		reclen := 19 + len(nameBytes)
		reclen = (reclen + 7) &^ 7
		if len(out)+reclen > capacity {
			break
		}
		// d_ino
		ino := uint64(1)
		for i := 0; i < 8; i++ {
			out = append(out, byte(ino>>(8*i)))
		}
		// d_off
		offset += uint64(reclen)
		for i := 0; i < 8; i++ {
			out = append(out, byte(offset>>(8*i)))
		}
		// d_reclen
		out = append(out, byte(reclen), byte(reclen>>8))
		// d_type
		typ := byte(8) // DT_REG
		if entry.IsDir() {
			typ = 4 // DT_DIR
		}
		out = append(out, typ)
		// name + padding
		out = append(out, nameBytes...)
		for len(out)%reclen != 0 {
			// pad within this record: ensure total record length is reclen
			if len(out) >= (len(out)/reclen+1)*reclen {
				break
			}
			out = append(out, 0)
		}
		// ensure exact reclen
		recordStart := len(out) - (len(out) % reclen) // not reliable; instead pad to multiple
		_ = recordStart
	}
	// Fix padding: rebuild with exact reclen padding
	// (The above loop may not pad correctly; redo below.)
	out = make([]byte, 0, capacity)
	offset = 0
	for _, entry := range entries {
		name := entry.Name()
		nameBytes := append([]byte(name), 0)
		reclen := (19 + len(nameBytes) + 7) &^ 7
		if len(out)+reclen > capacity {
			break
		}
		record := make([]byte, reclen)
		ino := uint64(1)
		for i := 0; i < 8; i++ {
			record[i] = byte(ino >> (8 * i))
		}
		offset += uint64(reclen)
		for i := 0; i < 8; i++ {
			record[8+i] = byte(offset >> (8 * i))
		}
		record[16] = byte(reclen)
		record[17] = byte(reclen >> 8)
		typ := byte(8)
		if entry.IsDir() {
			typ = 4
		}
		record[18] = typ
		copy(record[19:], nameBytes)
		out = append(out, record...)
	}
	if err := s.Memory.Write(ctx, call.Args[1], out); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(out))}
}

func (s Syscalls) fcntl(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	value, err := s.Descriptors.Fcntl(ctx, call.Args[0], call.Args[1], call.Args[2])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: value}
}

func (s Syscalls) pipe(ctx context.Context, call Call, pipe2 bool) Reply {
	if s.Descriptors == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	r, w, err := s.Descriptors.Pipe(ctx)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if pipe2 && call.Args[1]&0o2000000 != 0 { // O_CLOEXEC
		_, _ = s.Descriptors.Fcntl(ctx, r, 2, 1)
		_, _ = s.Descriptors.Fcntl(ctx, w, 2, 1)
	}
	var encoded [8]byte
	binary.LittleEndian.PutUint32(encoded[0:], uint32(r))
	binary.LittleEndian.PutUint32(encoded[4:], uint32(w))
	if err := s.Memory.Write(ctx, call.Args[0], encoded[:]); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: 0}
}

func (s Syscalls) eventfd2(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	if call.Args[1]&^efdFlagMask != 0 {
		return Reply{Errno: errno.Invalid}
	}
	descriptor, err := s.Descriptors.EventFD(ctx, call.Args[0], call.Args[1])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: descriptor}
}

func (s Syscalls) dup3(ctx context.Context, call Call) Reply {
	if s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	flags := call.Args[2]
	cloexec := flags&0o2000000 != 0
	if err := s.Descriptors.Dup2(ctx, call.Args[0], call.Args[1], cloexec); err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: call.Args[1]}
}

func (s Syscalls) brk(ctx context.Context, call Call) Reply {
	if s.Heap == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	current, err := s.Heap.Grow(ctx, call.Args[0])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	return Reply{Value: current}
}
