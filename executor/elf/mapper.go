package elf

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/abi/linux/common"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/loader/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

// mmapFloor 以下的地址留给程序段与堆;匿名映射自 floor 起向上选址,
// 上界为初始栈下沿。
const mmapFloor = 1 << 32

type mapper struct {
	mu sync.Mutex

	table   *memory.Table
	meter   resources.Meter
	pid     uint64
	ceiling uint64
}

func newMapper(table *memory.Table, process proc.Proc, plan *elf.Plan) *mapper {
	if table == nil {
		return nil
	}
	return &mapper{
		table:   table,
		meter:   metersOf(process),
		pid:     uint64(process.ID),
		ceiling: plan.Stack.Region.Start,
	}
}

func (m *mapper) Anonymous(ctx context.Context, request common.MapRequest) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if request.Length == 0 {
		return 0, errno.Invalid
	}
	prot, err := protOf(request.Prot)
	if err != nil {
		return 0, err
	}
	var start uint64
	if request.Fixed {
		if request.Hint+request.Length <= request.Hint {
			return 0, errno.Overflow
		}
		start = request.Hint
	} else {
		if start, err = m.place(ctx, request.Length, request.Hint); err != nil {
			return 0, err
		}
	}
	end := start + request.Length
	replaced := m.overlapBytes(ctx, start, end)
	charged := end - start - replaced
	if err := m.charge(ctx, charged); err != nil {
		return 0, err
	}
	if err := m.table.Replace(ctx, memory.Region{Start: start, End: end, Prot: prot, Name: "mmap"}, request.NoReplace); err != nil {
		m.release(ctx, charged)
		return 0, err
	}
	return start, nil
}

// fileBackend 把常规文件句柄接到地址空间后备:共享映射直读直写
// 底层数据(经由文件本身,跨地址空间一致),越文件尾读取零填充。
type fileBackend struct{ file common.RandomFile }

// FutexSalt 是共享映射 futex 键控的盐(K10):以底层句柄指针为身份
// ——fork 克隆共享同一后备实例(键相同,父子互唤醒);无关打开各得
// 独立实例(键不同,互不影响)。
func (b fileBackend) FutexSalt() uint64 {
	return uint64(reflect.ValueOf(b.file).Pointer())
}

func (b fileBackend) ReadAt(offset uint64, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	n, err := b.file.ReadAt(buffer, int64(offset))
	if err != nil && n == 0 {
		return err
	}
	// 短读(越过文件尾):剩余部分保持零(mmap 零页语义)。
	return nil
}

func (b fileBackend) WriteAt(offset uint64, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	n, err := b.file.WriteAt(buffer, int64(offset))
	if err != nil {
		return err
	}
	if n != len(buffer) {
		return errno.NoSpace
	}
	return nil
}

// snapshotChunk 是私有文件映射快照装载的单次读取粒度。
const snapshotChunk = 64 << 10

// File 实现文件映射(E08 私有/E09 共享):共享映射挂直通后备,
// 私有映射把文件内容装载为私有页(此后修改不写回)。
func (m *mapper) File(ctx context.Context, request common.FileMapRequest) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	prot, err := protOf(request.Prot)
	if err != nil {
		return 0, err
	}
	var start uint64
	if request.Fixed {
		if request.Hint+request.Length <= request.Hint {
			return 0, errno.Overflow
		}
		start = request.Hint
	} else {
		if start, err = m.place(ctx, request.Length, request.Hint); err != nil {
			return 0, err
		}
	}
	end := start + request.Length
	region := memory.Region{
		Start: start, End: end, Prot: prot, Name: "mmap-file",
		FileBase: request.Offset,
	}
	if !request.Shared {
		// 私有映射:快照文件内容为私有页(后备留空)。预算在建立
		// 映射前扣减,失败不留半初始化区间(O04)。
		replaced := m.overlapBytes(ctx, start, end)
		charged := request.Length - replaced
		if err := m.charge(ctx, charged); err != nil {
			return 0, err
		}
		if err := m.table.Replace(ctx, region, request.NoReplace); err != nil {
			m.release(ctx, charged)
			return 0, err
		}
		backend := fileBackend{file: request.File}
		for offset := uint64(0); offset < request.Length; {
			size := min(snapshotChunk, request.Length-offset)
			buffer := make([]byte, size)
			if err := backend.ReadAt(request.Offset+offset, buffer); err != nil {
				// 回滚已建立的映射,不留半初始化区间。
				_ = m.table.Unmap(ctx, start, end)
				return 0, err
			}
			if err := m.table.Load(ctx, start+offset, buffer); err != nil {
				_ = m.table.Unmap(ctx, start, end)
				return 0, err
			}
			offset += size
		}
		return start, nil
	}
	region.Backend = fileBackend{file: request.File}
	replaced := m.overlapBytes(ctx, start, end)
	charged := request.Length - replaced
	if err := m.charge(ctx, charged); err != nil {
		return 0, err
	}
	if err := m.table.Replace(ctx, region, request.NoReplace); err != nil {
		m.release(ctx, charged)
		return 0, err
	}
	return start, nil
}

// Sync 实现 msync:共享映射写入即时直通文件,只需校验范围完整映射。
func (m *mapper) Sync(ctx context.Context, start, end uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.table.Sync(ctx, start, end)
}

func (m *mapper) Unmap(ctx context.Context, start, end uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	freed := m.overlapBytes(ctx, start, end)
	if err := m.table.Unmap(ctx, start, end); err != nil {
		return err
	}
	m.release(ctx, freed)
	return nil
}

func (m *mapper) Protect(ctx context.Context, start, end uint64, bits uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	prot, err := protOf(bits)
	if err != nil {
		return err
	}
	return m.table.Protect(ctx, start, end, prot)
}

// place 优先采用非零 hint,被占用或越界时在 [mmapFloor, ceiling) 内
// 自下而上寻找第一个足够大的空隙,与 Linux 旧式自底向上布局一致。
func (m *mapper) place(ctx context.Context, length, hint uint64) (uint64, error) {
	regions, err := m.table.Regions(ctx)
	if err != nil {
		return 0, err
	}
	if hint >= mmapFloor && hint+length > hint && hint+length <= m.ceiling && !overlapsAny(regions, hint, hint+length) {
		return hint, nil
	}
	slices.SortFunc(regions, func(a, b memory.Region) int { return cmp.Compare(a.Start, b.Start) })
	candidate := uint64(mmapFloor)
	for _, region := range regions {
		if region.End <= candidate {
			continue
		}
		if region.Start >= m.ceiling {
			break
		}
		if region.Start > candidate && region.Start-candidate >= length {
			return candidate, nil
		}
		if region.End > candidate {
			candidate = region.End
		}
		if candidate >= m.ceiling {
			break
		}
	}
	if m.ceiling-candidate >= length {
		return candidate, nil
	}
	return 0, errno.NoMemory
}

func (m *mapper) overlapBytes(ctx context.Context, start, end uint64) uint64 {
	regions, err := m.table.Regions(ctx)
	if err != nil {
		return 0
	}
	var total uint64
	for _, region := range regions {
		if !region.Overlaps(memory.Region{Start: start, End: end}) {
			continue
		}
		overlapStart := max(region.Start, start)
		overlapEnd := min(region.End, end)
		total += overlapEnd - overlapStart
	}
	return total
}

func overlapsAny(regions []memory.Region, start, end uint64) bool {
	target := memory.Region{Start: start, End: end}
	for _, region := range regions {
		if region.Overlaps(target) {
			return true
		}
	}
	return false
}

func protOf(bits uint64) (memory.Prot, error) {
	if bits&^(common.ProtRead|common.ProtWrite|common.ProtExec) != 0 {
		return 0, errno.Invalid
	}
	var out memory.Prot
	if bits&common.ProtRead != 0 {
		out |= memory.ProtRead
	}
	if bits&common.ProtWrite != 0 {
		out |= memory.ProtWrite
	}
	if bits&common.ProtExec != 0 {
		out |= memory.ProtExec
	}
	return out, nil
}

func (m *mapper) charge(ctx context.Context, size uint64) error {
	if m.meter == nil || size == 0 {
		return nil
	}
	return m.meter.ChargeKind(ctx, m.pid, resources.KindMemory, size)
}

func (m *mapper) release(ctx context.Context, size uint64) {
	if m.meter == nil || size == 0 {
		return
	}
	_ = m.meter.ReleaseKind(ctx, m.pid, resources.KindMemory, size)
}

var _ common.Mappable = (*mapper)(nil)

// limiter 把会话计量器的描述符预算接到 rlimit 查询;
// 进程运行期内的 setrlimit 只能在真实预算内下调。
type limiter struct {
	meters   resources.Meter
	pid      uint64
	override uint64
	has      bool
}

func (l *limiter) DescriptorLimit(ctx context.Context) (uint64, bool) {
	if l.has {
		return l.override, true
	}
	if l.meters == nil {
		return 0, false
	}
	view, err := l.meters.View(ctx, l.pid)
	if err != nil || view.DescriptorLimit == 0 {
		return 0, false
	}
	return view.DescriptorLimit, true
}

func (l *limiter) SetDescriptorLimit(ctx context.Context, value uint64) error {
	if l.meters != nil {
		view, err := l.meters.View(ctx, l.pid)
		if err == nil && view.DescriptorLimit != 0 && value > view.DescriptorLimit {
			return errno.Permission
		}
	}
	l.override, l.has = value, true
	return nil
}

var _ common.Limiter = (*limiter)(nil)

// Mremap 实现 mremap(2):收缩原地、扩张尽量原地、尾部被占用时按 MAYMOVE 迁移。
// 旧范围必须恰好是一个映射(与真实分配器的整段重映射用法一致)。
func (m *mapper) Mremap(ctx context.Context, oldStart, oldEnd, newEnd uint64, mayMove, fixed bool, fixedStart uint64) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	regions, err := m.table.Regions(ctx)
	if err != nil {
		return 0, err
	}
	var region *memory.Region
	for i := range regions {
		if regions[i].Start == oldStart && regions[i].End == oldEnd {
			region = &regions[i]
			break
		}
	}
	if region == nil {
		return 0, errno.NoMemory
	}

	if newEnd <= oldEnd {
		if err := m.table.Resize(ctx, oldStart, oldEnd, newEnd); err != nil {
			return 0, err
		}
		m.release(ctx, oldEnd-newEnd)
		return oldStart, nil
	}
	grow := newEnd - oldEnd
	if newEnd <= m.ceiling && !overlapsAny(regions, oldEnd, newEnd) {
		if err := m.charge(ctx, grow); err != nil {
			return 0, err
		}
		if err := m.table.Resize(ctx, oldStart, oldEnd, newEnd); err != nil {
			m.release(ctx, grow)
			return 0, err
		}
		return oldStart, nil
	}
	if !mayMove {
		return 0, errno.NoMemory
	}
	grown := newEnd - oldStart
	var newStart uint64
	if fixed {
		if overlapsAny(regions, fixedStart, fixedStart+grown) {
			return 0, errno.Exist
		}
		if fixedStart+grown > m.ceiling {
			return 0, errno.NoMemory
		}
		newStart = fixedStart
	} else {
		if newStart, err = m.place(ctx, grown, 0); err != nil {
			return 0, err
		}
	}
	if err := m.charge(ctx, grow); err != nil {
		return 0, err
	}
	if err := m.table.Move(ctx, oldStart, oldEnd, newStart); err != nil {
		m.release(ctx, grow)
		return 0, err
	}
	// 迁移到 grown 大小的空隙后,在新位置原地扩张到目标大小。
	if err := m.table.Resize(ctx, newStart, newStart+oldEnd-oldStart, newStart+grown); err != nil {
		m.release(ctx, grow)
		return 0, err
	}
	return newStart, nil
}

// Madvise 实现有明确语义的 advice 子集:DONTNEED 丢弃页内容,
// NORMAL/RANDOM/SEQUENTIAL/WILLNEED 在本模型中无可观察差异,如实返回成功。
func (m *mapper) Madvise(ctx context.Context, start, end uint64, advice uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	switch advice {
	case common.MadvNormal, common.MadvRandom, common.MadvSequential, common.MadvWillneed:
		return nil
	case common.MadvDontneed:
		return m.table.DropPages(ctx, start, end)
	default:
		return errno.Invalid
	}
}
