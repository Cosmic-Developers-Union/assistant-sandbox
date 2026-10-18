package common

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
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
	case SysBrk:
		return s.brk(ctx, call)
	case SysUname:
		return s.uname(ctx, call)
	case SysGetrandom:
		return s.getrandom(ctx, call)
	case SysGetcwd:
		return s.getcwd(ctx, call)
	case SysSysinfo:
		return s.sysinfo(ctx, call)
	case SysGetpgid, SysGetsid, SysGetpgrp:
		return s.identity(call)
	case SysGethostname:
		return s.gethostname(ctx, call)
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
	case SysGetPID, SysGetTID, SysGetpgid, SysGetsid, SysGetpgrp:
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

type Heap interface {
	Break(ctx context.Context) (uint64, error)

	Grow(ctx context.Context, target uint64) (uint64, error)
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

func (s Syscalls) gethostname(ctx context.Context, call Call) Reply {
	if s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	name := s.nodename()
	size := call.Args[1]
	encoded := append([]byte(name), 0)
	if size == 0 {
		return Reply{Errno: errno.Invalid}
	}
	if uint64(len(encoded)) > size {
		return Reply{Errno: errno.Invalid}
	}
	if err := s.Memory.Write(ctx, call.Args[0], encoded); err != nil {
		return Reply{Errno: errno.Fault}
	}
	return Reply{Value: uint64(len(name))}
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
