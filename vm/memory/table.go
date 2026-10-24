package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Table struct {
	mu      sync.Mutex
	regions []Region

	pages map[uint64][]byte

	limit uint64
}

func New(limitBytes uint64) *Table { return &Table{limit: limitBytes} }

func (t *Table) Map(ctx context.Context, region Region) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRegion(region); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.checkOverlapLocked(region); err != nil {
		return err
	}
	if err := t.checkBudgetLocked(region.Size()); err != nil {
		return err
	}
	t.insertLocked(region)
	return nil
}

func (t *Table) Unmap(ctx context.Context, start, end uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRange(start, end); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.coversLocked(start, end) {
		return faultError(FaultUnmapped, start, "解除映射的范围未被完整映射")
	}
	updated := make([]Region, 0, len(t.regions)+2)
	for _, region := range t.regions {
		if region.End <= start || region.Start >= end {
			updated = append(updated, region)
			continue
		}
		if region.Start < start {
			left := region
			left.End = start
			updated = append(updated, left)
		}
		if end < region.End {
			right := region
			right.Start = end
			updated = append(updated, right)
		}
	}
	t.regions = updated

	t.dropPagesLocked(start, end)
	return nil
}

// coversLocked 要求 [start,end) 被既有映射无缝覆盖;存在空洞时整体失败,
// 与 Linux munmap 返回 ENOMEM 且不改动任何映射的语义一致。
func (t *Table) coversLocked(start, end uint64) bool {
	cursor := start
	for _, region := range t.regions {
		if region.End <= cursor {
			continue
		}
		if region.Start >= end {
			break
		}
		if region.Start > cursor {
			return false
		}
		cursor = min(region.End, end)
		if cursor >= end {
			return true
		}
	}
	return cursor >= end
}

func (t *Table) Protect(ctx context.Context, start, end uint64, prot Prot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRange(start, end); err != nil {
		return err
	}
	if err := validateProt(prot); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	index := t.findCoveringLocked(start, end)
	if index < 0 {
		return faultError(FaultUnmapped, start, "修改保护位的区间未被完整映射")
	}
	original := t.regions[index]
	replacement := make([]Region, 0, 3)

	if original.Start < start {
		left := original
		left.End = start
		replacement = append(replacement, left)
	}

	target := original
	target.Start, target.End, target.Prot = start, end, prot
	replacement = append(replacement, target)

	if end < original.End {
		right := original
		right.Start = end
		replacement = append(replacement, right)
	}
	updated := make([]Region, 0, len(t.regions)+2)
	updated = append(updated, t.regions[:index]...)
	updated = append(updated, replacement...)
	updated = append(updated, t.regions[index+1:]...)
	t.regions = updated
	return nil
}

func (t *Table) Check(ctx context.Context, address, length uint64, prot Prot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.checkLocked(address, length, prot)
}

func (t *Table) checkLocked(address, length uint64, prot Prot) error {
	if length == 0 {
		return nil
	}
	end, err := addNoOverflow(address, length)
	if err != nil {
		return faultError(FaultOverflow, address, "访问区间溢出客体地址空间")
	}

	index := t.findContainingLocked(address)
	if index < 0 {
		return faultError(FaultUnmapped, address, "地址未映射")
	}
	region := t.regions[index]

	if end > region.End {
		return faultError(FaultUnmapped, address, "访问跨越映射边界")
	}
	if err := requireProt(region.Prot, prot); err != nil {
		return faultError(FaultProtection, address,
			"访问需要 "+prot.String()+"，该区间为 "+region.Prot.String())
	}
	return nil
}

func (t *Table) Read(ctx context.Context, address uint64, buffer []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(buffer) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	length := uint64(len(buffer))
	if err := t.checkLocked(address, length, ProtRead); err != nil {
		return err
	}
	clear(buffer)
	for page := address / PageSize; page <= (address+length-1)/PageSize; page++ {
		stored := t.pages[page]
		if stored == nil {
			continue
		}
		base := page * PageSize
		start := max(address, base)
		stop := min(address+length, base+PageSize)
		copy(buffer[start-address:stop-address], stored[start-base:stop-base])
	}
	return nil
}

func (t *Table) Write(ctx context.Context, address uint64, buffer []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(buffer) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	length := uint64(len(buffer))
	if err := t.checkLocked(address, length, ProtWrite); err != nil {
		return err
	}
	if t.pages == nil {
		t.pages = make(map[uint64][]byte)
	}
	end := address + length
	for page := address / PageSize; page <= (end-1)/PageSize; page++ {
		stored := t.pages[page]
		if stored == nil {
			stored = make([]byte, PageSize)
			t.pages[page] = stored
		}
		base := page * PageSize
		start := max(address, base)
		stop := min(end, base+PageSize)
		copy(stored[start-base:stop-base], buffer[start-address:stop-address])
	}
	return nil
}

func (t *Table) dropPagesLocked(start, end uint64) {
	for page := start / PageSize; page < end/PageSize; page++ {
		delete(t.pages, page)
	}
}

func (t *Table) Regions(ctx context.Context) ([]Region, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.copyLocked(), nil
}

func (t *Table) Account(ctx context.Context) (Account, error) {
	if err := ctx.Err(); err != nil {
		return Account{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var mapped uint64
	for _, region := range t.regions {
		mapped += region.Size()
	}
	return Account{Mapped: mapped, Regions: len(t.regions), Limit: t.limit}, nil
}

func validateRegion(region Region) error {
	if err := validateRange(region.Start, region.End); err != nil {
		return err
	}
	return validateProt(region.Prot)
}

func validateRange(start, end uint64) error {
	if start >= end {
		return errno.Wrap(errno.Invalid, "区间必须非空且 start < end")
	}
	if start%PageSize != 0 || end%PageSize != 0 {
		return faultError(FaultMisaligned, start, "起止地址必须按页对齐")
	}

	if end > maxAddress {
		return faultError(FaultOverflow, start, "区间超出客体地址空间")
	}
	return nil
}

func validateProt(prot Prot) error {
	if prot&^(ProtRead|ProtWrite|ProtExec) != 0 {
		return errno.Wrap(errno.Invalid, "存在未知保护位")
	}
	if prot.WritableAndExecutable() {
		return errno.Wrap(errno.Permission, "不允许同时授予写与执行（W^X）")
	}
	return nil
}

const maxAddress uint64 = 1 << 48

func addNoOverflow(left, right uint64) (uint64, error) {
	sum := left + right
	if sum < left {
		return 0, errno.Overflow
	}
	return sum, nil
}

func faultError(fault Fault, address uint64, detail string) error {
	identity := errno.Range
	switch fault {
	case FaultMisaligned, FaultOverflow:
		identity = errno.Invalid
	case FaultProtection:
		identity = errno.Permission
	}
	return errno.Wrap(identity, fault.String()+"@"+utoa(address)+": "+detail)
}

func (t *Table) checkOverlapLocked(region Region) error {
	for _, existing := range t.regions {
		if existing.Overlaps(region) {
			return errno.Wrap(errno.Exist,
				"区间与既有映射重叠: "+utoa(existing.Start)+"-"+utoa(existing.End))
		}
	}
	return nil
}

func (t *Table) checkBudgetLocked(size uint64) error {
	if t.limit == 0 {
		return nil
	}
	var mapped uint64
	for _, region := range t.regions {
		mapped += region.Size()
	}
	if mapped+size > t.limit {
		return errno.Wrap(errno.NoSpace, "映射超出地址空间预算")
	}
	return nil
}

func (t *Table) insertLocked(region Region) {
	index := sort.Search(len(t.regions), func(i int) bool {
		return t.regions[i].Start >= region.Start
	})
	t.regions = append(t.regions, Region{})
	copy(t.regions[index+1:], t.regions[index:])
	t.regions[index] = region
}

func (t *Table) findCoveringLocked(start, end uint64) int {
	for index, region := range t.regions {
		if region.Start <= start && end <= region.End {
			return index
		}
	}
	return -1
}

func (t *Table) findContainingLocked(address uint64) int {
	for index, region := range t.regions {
		if region.Contains(address) {
			return index
		}
	}
	return -1
}

func (t *Table) copyLocked() []Region {
	if len(t.regions) == 0 {
		return nil
	}
	out := make([]Region, len(t.regions))
	copy(out, t.regions)
	return out
}

func requireProt(granted, wanted Prot) error {
	if wanted == 0 {
		return nil
	}
	if granted&wanted != wanted {
		return errno.Permission
	}
	return nil
}

var _ Space = (*Table)(nil)
