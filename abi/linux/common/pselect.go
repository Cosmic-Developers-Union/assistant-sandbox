package common

import (
	"context"
	"encoding/binary"
	"errors"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/poll"
)

// pselect6(L06):asm-generic 无独立 select(72 = pselect6,glibc 经
// pselect6 实现 select 语义)。
//
//   - fd_set 是 1024 位位图(128 字节);nfds 上限 1024,超出 EINVAL;
//   - 集合中关闭的描述符立即 EBADF(select(2) 语义),不进入等待,
//     不会以忙循环重试坏描述符;
//   - 普通文件与目录恒就绪,管道/eventfd/signalfd 按就绪能力判定
//     (与 poll 共用等待图);
//   - 三个集合都为空时按超时纯睡眠,不忙循环;
//   - 超时是 timespec 且不被修改;sigmask 参数是指向
//     {const sigset_t *, size_t} 的结构,等待期间替换屏蔽集
//     (与 ppoll 同语义);
//   - 例外集不接受(socket 紧急数据不在兼容配置),置位报 EINVAL。

// fdSetBytes 是 fd_set 的字节数(1024 位)。
const fdSetBytes = 128

// fdSetLimit 是单个 fd_set 能表达的描述符上限。
const fdSetLimit = fdSetBytes * 8

// pselect6_sigmask 参数块:指向 sigset 的指针与长度。
const pselectSigmaskSize = 16

func (s Syscalls) pselect6(ctx context.Context, call Call) Reply {
	if s.Memory == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	nfds := call.Args[0]
	if nfds > fdSetLimit {
		return Reply{Errno: errno.Invalid}
	}
	// 读入三个集合(NULL 表示空集)。
	reads, code := s.readFDSet(ctx, call.Args[1], nfds)
	if code != "" {
		return Reply{Errno: code}
	}
	writes, code := s.readFDSet(ctx, call.Args[2], nfds)
	if code != "" {
		return Reply{Errno: code}
	}
	excepts, code := s.readFDSet(ctx, call.Args[3], nfds)
	if code != "" {
		return Reply{Errno: code}
	}
	if excepts.count() != 0 {
		// 例外条件(socket 紧急数据)不在首个兼容配置。
		return Reply{Errno: errno.Invalid}
	}
	// 超时:NULL 表示无限等待;timespec 非法值 EINVAL。
	timeout, forever, code := s.pselectTimeout(ctx, call.Args[4])
	if code != "" {
		return Reply{Errno: code}
	}
	// 逐个描述符构建等待项;关闭的描述符立即 EBADF。
	type want struct {
		read  bool
		write bool
	}
	wants := map[uint32]*want{}
	var order []uint32
	for fd := uint64(0); fd < nfds; fd++ {
		read := reads.test(fd)
		write := writes.test(fd)
		if !read && !write {
			continue
		}
		entry := &want{read: read, write: write}
		wants[uint32(fd)] = entry
		order = append(order, uint32(fd))
	}
	for _, fd := range order {
		if _, err := s.Descriptors.Get(ctx, uint64(fd)); err != nil {
			// select(2):集合中的坏描述符立即 EBADF,不进入等待。
			return Reply{Errno: errno.BadHandle}
		}
	}
	// 等待期间替换信号屏蔽集(pselect6 的 sigmask 指向 {指针,长度})。
	restore, err := s.applyPselectMask(ctx, call.Args[5])
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if restore != nil {
		defer func() { _, _ = s.Signals.ApplyMask(sigSetMask, *restore) }()
	}

	// 就绪判定:普通文件与目录恒就绪(立即返回);其余按就绪能力
	// 进入等待图,全部未就绪时等待变化或超时;没有任何等待源时按
	// 超时纯睡眠(空集合睡眠,不忙循环)。
	ready := map[uint32]poll.Readiness{}
	var sources []poll.Entry
	var waitOrder []uint32
	for _, fd := range order {
		handle, err := s.Descriptors.Get(ctx, uint64(fd))
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		source, ok := handle.(pollSource)
		if !ok {
			var bits poll.Readiness
			if wants[fd].read {
				bits |= poll.Readable
			}
			if wants[fd].write {
				bits |= poll.Writable
			}
			ready[fd] = bits
			continue
		}
		var wantBits poll.Readiness
		if wants[fd].read {
			wantBits |= poll.Readable
		}
		if wants[fd].write {
			wantBits |= poll.Writable
		}
		sources = append(sources, poll.Entry{Source: source, Want: wantBits})
		waitOrder = append(waitOrder, fd)
	}
	if len(ready) == 0 {
		if len(sources) > 0 {
			var clk Sleeper
			if s.Sleeper != nil {
				clk = s.Sleeper
			}
			results, err := poll.WaitFor(ctx, clk, sources, timeoutOf(forever, timeout))
			switch {
			case err == nil:
				for _, result := range results {
					ready[waitOrder[result.Index]] |= result.Received
				}
			case errors.Is(err, errno.TimedOut):
				// 超时:集合清空,返回 0。
			default:
				return Reply{Errno: errnoOf(err)}
			}
		} else if code := s.pselectSleep(ctx, timeout, forever); code != "" {
			return Reply{Errno: code}
		}
	}
	// 写回结果集合:清零后置就绪位(超时时集合清空,返回 0)。
	resultReads := newFDSet()
	resultWrites := newFDSet()
	count := uint64(0)
	for _, fd := range order {
		entry := wants[fd]
		waiting := s.waitsSource(ctx, fd)
		received := ready[fd]
		if entry.read && (!waiting || received&poll.Readable != 0) {
			resultReads.set(uint64(fd))
			count++
		}
		if entry.write && (!waiting || received&poll.Writable != 0) {
			resultWrites.set(uint64(fd))
			count++
		}
	}
	if call.Args[1] != 0 {
		if err := s.Memory.Write(ctx, call.Args[1], resultReads[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	if call.Args[2] != 0 {
		if err := s.Memory.Write(ctx, call.Args[2], resultWrites[:]); err != nil {
			return Reply{Errno: errno.Fault}
		}
	}
	return Reply{Value: count}
}

// waitsSource 报告描述符是否参与就绪等待图(普通文件恒就绪)。
func (s Syscalls) waitsSource(ctx context.Context, fd uint32) bool {
	handle, err := s.Descriptors.Get(ctx, uint64(fd))
	if err != nil {
		return false
	}
	_, ok := handle.(pollSource)
	return ok
}

// pselectTimeout 解析 timespec 超时:NULL 表示无限;负值或纳秒越界
// EINVAL;零时长表示立即返回。
func (s Syscalls) pselectTimeout(ctx context.Context, address uint64) (nanoseconds uint64, forever bool, code errno.Code) {
	if address == 0 {
		return 0, true, ""
	}
	var raw [16]byte
	if err := s.Memory.Read(ctx, address, raw[:]); err != nil {
		return 0, false, errno.Fault
	}
	seconds := int64(binary.LittleEndian.Uint64(raw[0:]))
	nsec := int64(binary.LittleEndian.Uint64(raw[8:]))
	if seconds < 0 || nsec < 0 || nsec >= 1_000_000_000 {
		return 0, false, errno.Invalid
	}
	total := uint64(seconds)*1_000_000_000 + uint64(nsec)
	return total, false, ""
}

func timeoutOf(forever bool, nanoseconds uint64) uint64 {
	if forever {
		return poll.PollInfinite
	}
	return nanoseconds
}

// applyPselectMask 在等待期间替换信号屏蔽集:pselect6 的 sigmask
// 参数指向 {const sigset_t *mask; size_t size;} 结构。
func (s Syscalls) applyPselectMask(ctx context.Context, address uint64) (*uint64, error) {
	if address == 0 {
		return nil, nil
	}
	var block [pselectSigmaskSize]byte
	if err := s.Memory.Read(ctx, address, block[:]); err != nil {
		return nil, errno.Fault
	}
	maskAddress := binary.LittleEndian.Uint64(block[0:])
	size := binary.LittleEndian.Uint64(block[8:])
	if maskAddress == 0 {
		return nil, nil
	}
	if size != sigsetSize {
		return nil, errno.Invalid
	}
	if s.Signals == nil {
		return nil, errno.Wrap(errno.NotImplemented, "信号端口未装配")
	}
	var set [8]byte
	if err := s.Memory.Read(ctx, maskAddress, set[:]); err != nil {
		return nil, errno.Fault
	}
	old, err := s.Signals.ApplyMask(sigSetMask, binary.LittleEndian.Uint64(set[:]))
	if err != nil {
		return nil, err
	}
	return &old, nil
}

// pselectSleep 空集合睡眠:有限超时按切片睡眠(可被待投递信号以
// EINTR 打断);无限等待阻塞到上下文结束,不忙循环。
func (s Syscalls) pselectSleep(ctx context.Context, timeout uint64, forever bool) errno.Code {
	if forever {
		if s.Interrupts == nil {
			<-ctx.Done()
			return errno.Interrupted
		}
		for {
			if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
				return errno.Interrupted
			}
			if code := s.sliceWait(ctx); code != "" {
				return code
			}
		}
	}
	if code := s.sleepInterruptible(ctx, timeout); code != "" {
		return code
	}
	return ""
}

// sliceWait 等待一个睡眠切片,返回空表示继续;睡眠端口未装配时
// 阻塞到上下文结束(不忙循环)。
func (s Syscalls) sliceWait(ctx context.Context) errno.Code {
	if s.Sleeper == nil {
		<-ctx.Done()
		return errno.Interrupted
	}
	if err := s.Sleeper.Sleep(ctx, sleepSlice); err != nil {
		return errno.Interrupted
	}
	return ""
}

// sleepInterruptible 有限时长睡眠:可被待投递信号打断(EINTR);
// 睡眠端口未装配时立即返回(调用方语义退化为非阻塞)。
func (s Syscalls) sleepInterruptible(ctx context.Context, nanoseconds uint64) errno.Code {
	if s.Sleeper == nil {
		return ""
	}
	if s.Interrupts == nil {
		if err := s.Sleeper.Sleep(ctx, nanoseconds); err != nil {
			return errno.Interrupted
		}
		return ""
	}
	for remaining := nanoseconds; remaining > 0; {
		step := remaining
		if step > sleepSlice {
			step = sleepSlice
		}
		if err := s.Sleeper.Sleep(ctx, step); err != nil {
			return errno.Interrupted
		}
		remaining -= step
		if _, _, ok := s.Interrupts.DeliverableSignal(); ok {
			return errno.Interrupted
		}
	}
	return ""
}

// fdSet 是 1024 位的描述符集合位图。
type fdSet [fdSetBytes]byte

func newFDSet() *fdSet { return &fdSet{} }

func (f *fdSet) test(fd uint64) bool {
	if fd >= fdSetLimit {
		return false
	}
	return f[fd/8]&(1<<(fd%8)) != 0
}

func (f *fdSet) set(fd uint64) {
	if fd >= fdSetLimit {
		return
	}
	f[fd/8] |= 1 << (fd % 8)
}

func (f *fdSet) count() int {
	total := 0
	for _, b := range f {
		for bits := b; bits != 0; bits &= bits - 1 {
			total++
		}
	}
	return total
}

// readFDSet 读入一个 fd_set;address 为 0 表示空集。
func (s Syscalls) readFDSet(ctx context.Context, address uint64, nfds uint64) (*fdSet, errno.Code) {
	set := newFDSet()
	if address == 0 {
		return set, ""
	}
	var raw [fdSetBytes]byte
	if err := s.Memory.Read(ctx, address, raw[:]); err != nil {
		return nil, errno.Fault
	}
	*set = raw
	// nfds 之外的位必须为零(Linux 语义:多余的位未定义,本实现
	// 忽略而不报错)。
	for fd := nfds; fd < fdSetLimit; fd++ {
		if set.test(fd) {
			set[fd/8] &^= 1 << (fd % 8)
		}
	}
	return set, ""
}
