package elf

import (
	"cmp"
	"context"
	"slices"

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
	if err := m.table.Replace(ctx, memory.Region{Start: start, End: end, Prot: prot, Name: "mmap"}, request.NoReplace); err != nil {
		return 0, err
	}
	m.charge(ctx, end-start-replaced)
	return start, nil
}

func (m *mapper) Unmap(ctx context.Context, start, end uint64) error {
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

func (m *mapper) charge(ctx context.Context, size uint64) {
	if m.meter == nil || size == 0 {
		return
	}
	_ = m.meter.ChargeKind(ctx, m.pid, resources.KindMemory, size)
}

func (m *mapper) release(ctx context.Context, size uint64) {
	if m.meter == nil || size == 0 {
		return
	}
	_ = m.meter.ReleaseKind(ctx, m.pid, resources.KindMemory, size)
}

var _ common.Mappable = (*mapper)(nil)
