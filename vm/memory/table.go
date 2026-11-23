package memory

import (
	"cmp"
	"context"
	"encoding/binary"
	"slices"
	"sort"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Table struct {
	mu      sync.Mutex
	regions []Region

	pages map[uint64][]byte

	limit uint64

	codePage       uint64
	codePageBytes  []byte
	codePageCached bool

	// annotation 允许装载层在同组线程共享的地址空间上挂每地址空间
	// 状态(brk/mmap 游标等);内核不解释其内容(K02)。
	annotation any
}

func New(limitBytes uint64) *Table { return &Table{limit: limitBytes} }

// Limit 返回表的总字节上限(0=不限);换绑重建(H12)沿用同一上限。
func (t *Table) Limit() uint64 { return t.limit }

// SetAnnotation 挂载每地址空间装载状态。
func (t *Table) SetAnnotation(value any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.annotation = value
}

// Annotation 返回挂载的每地址空间状态;未挂载返回 nil。
func (t *Table) Annotation() any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.annotation
}

// Clone 复制整个地址空间(fork 语义):区域列表与全部私有页深拷贝,
// 父子此后对同一虚拟地址的写入互不可见;预算上限一并继承。
// 文件共享映射的区域持后备引用,浅拷贝后父子仍指向同一底层数据
// (E09:fork 后共享映射仍共享);私有页(含文件私有映射快照)深拷贝。
func (t *Table) Clone() (*Table, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	clone := New(t.limit)
	clone.regions = make([]Region, len(t.regions))
	copy(clone.regions, t.regions)
	if t.pages != nil {
		clone.pages = make(map[uint64][]byte, len(t.pages))
		for page, data := range t.pages {
			copied := make([]byte, len(data))
			copy(copied, data)
			clone.pages[page] = copied
		}
	}
	return clone, nil
}

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

// Protect 将 [start,end) 内的所有映射改为 prot,允许跨越多个区间;
// 范围存在空洞时报未映射且不改动,与 Linux mprotect 的 ENOMEM 语义一致。
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
	if !t.coversLocked(start, end) {
		return faultError(FaultUnmapped, start, "修改保护位的范围未被完整映射")
	}
	t.invalidateCodeLocked(start, end)
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
		target := region
		target.Start = max(region.Start, start)
		target.End = min(region.End, end)
		target.Prot = prot
		updated = append(updated, target)
		if end < region.End {
			right := region
			right.Start = end
			updated = append(updated, right)
		}
	}
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

// Executable 校验 [address,address+length) 具备执行权限,供取指路径调用。
func (t *Table) Executable(ctx context.Context, address, length uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.checkLocked(address, length, ProtExec); err != nil {
		return err
	}
	return nil
}

func (t *Table) checkLocked(address, length uint64, prot Prot) error {
	if length == 0 {
		return nil
	}
	end, err := addNoOverflow(address, length)
	if err != nil {
		return faultError(FaultOverflow, address, "访问区间溢出客体地址空间")
	}

	// 快速路径:绝大多数访问落在单个映射内,二分查找即可。
	if index := t.findContainingLocked(address); index >= 0 {
		region := t.regions[index]
		if end <= region.End {
			if err := requireProt(region.Prot, prot); err != nil {
				return faultError(FaultProtection, address,
					"访问需要 "+prot.String()+"，该区间为 "+region.Prot.String())
			}
			return nil
		}
	}
	// 访问可以跨越相邻映射(Linux 语义):只要整段被映射无缝覆盖,
	// 且每一段都具备所需保护位即可。动态装载器常在数据段尾部
	// 紧邻匿名 BSS 页,跨页写入不能被误判为越界。
	if !t.coversLocked(address, end) {
		return faultError(FaultUnmapped, address, "访问跨越未映射区间")
	}
	for _, region := range t.regions {
		if region.End <= address || region.Start >= end {
			continue
		}
		if err := requireProt(region.Prot, prot); err != nil {
			return faultError(FaultProtection, max(address, region.Start),
				"访问需要 "+prot.String()+"，该区间为 "+region.Prot.String())
		}
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
	return t.readLocked(address, buffer, ProtRead)
}

// FetchInstruction 在同一锁内校验执行权限并取回一条 RISC-V 指令的
// 原始编码和长度。执行核心按 2/4 字节编码直接展开,避免每次取指
// 都分别走 Read 和 Executable 两次映射查找。
func (t *Table) FetchInstruction(ctx context.Context, address uint64) (uint32, uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fetchInstructionLocked(address)
}

func (t *Table) fetchInstructionLocked(address uint64) (uint32, uint64, error) {
	if instruction, length, ok := t.cachedInstructionLocked(address); ok {
		return instruction, length, nil
	}
	// 常见路径:整条 32 位指令落在同一个映射区间内。
	if index := t.findContainingLocked(address); index >= 0 {
		region := t.regions[index]
		if end, err := addNoOverflow(address, 4); err == nil &&
			end <= region.End && address/PageSize == (end-1)/PageSize {
			if err := requireProt(region.Prot, ProtExec); err != nil {
				return 0, 0, err
			}
			var encoded [4]byte
			if err := t.readRegionLocked(region, address, encoded[:]); err != nil {
				return 0, 0, err
			}
			instruction := binary.LittleEndian.Uint32(encoded[:])
			t.cacheInstructionPageLocked(address)
			if instruction&0x3 != 0x3 {
				return instruction & 0xFFFF, 2, nil
			}
			return instruction, 4, nil
		}
	}

	// 边界路径:区间尾部只剩压缩指令的两个字节。
	if err := t.checkLocked(address, 2, ProtExec); err != nil {
		return 0, 0, err
	}
	var first [2]byte
	if err := t.readMappedLocked(address, first[:]); err != nil {
		return 0, 0, err
	}
	raw := binary.LittleEndian.Uint16(first[:])
	if raw&0x3 != 0x3 {
		return uint32(raw), 2, nil
	}
	if err := t.checkLocked(address, 4, ProtExec); err != nil {
		return 0, 0, err
	}
	var encoded [4]byte
	if err := t.readMappedLocked(address, encoded[:]); err != nil {
		return 0, 0, err
	}
	t.cacheInstructionPageLocked(address)
	return binary.LittleEndian.Uint32(encoded[:]), 4, nil
}

func (t *Table) cachedInstructionLocked(address uint64) (uint32, uint64, bool) {
	page := address / PageSize
	if !t.codePageCached || t.codePage != page {
		return 0, 0, false
	}
	offset := address % PageSize
	if offset+4 <= PageSize {
		instruction := binary.LittleEndian.Uint32(t.codePageBytes[offset : offset+4])
		if instruction&0x3 != 0x3 {
			return instruction & 0xFFFF, 2, true
		}
		return instruction, 4, true
	}
	raw := binary.LittleEndian.Uint16(t.codePageBytes[offset : offset+2])
	if raw&0x3 == 0x3 {
		return 0, 0, false
	}
	return uint32(raw), 2, true
}

func (t *Table) cacheInstructionPageLocked(address uint64) {
	page := address / PageSize
	base := page * PageSize
	if err := t.checkLocked(base, PageSize, ProtExec); err != nil {
		return
	}
	if t.codePageBytes == nil {
		t.codePageBytes = make([]byte, PageSize)
	}
	if err := t.readMappedLocked(base, t.codePageBytes); err != nil {
		t.invalidateCodeLocked(base, base+PageSize)
		return
	}
	t.codePage = page
	t.codePageCached = true
}

func (t *Table) invalidateCodeLocked(start, end uint64) {
	if !t.codePageCached {
		return
	}
	pageStart := t.codePage * PageSize
	if start < pageStart+PageSize && end > pageStart {
		t.codePageCached = false
	}
}

// readLocked 在已持锁状态下读取;调用方负责长度非零与上下文检查。
func (t *Table) readLocked(address uint64, buffer []byte, prot Prot) error {
	length := uint64(len(buffer))
	if err := t.checkLocked(address, length, prot); err != nil {
		return err
	}
	return t.readMappedLocked(address, buffer)
}

// readMappedLocked 在已验证映射与权限后复制字节。
func (t *Table) readMappedLocked(address uint64, buffer []byte) error {
	length := uint64(len(buffer))
	clear(buffer)
	for page := address / PageSize; page <= (address+length-1)/PageSize; page++ {
		base := page * PageSize
		start := max(address, base)
		stop := min(address+length, base+PageSize)
		slice := buffer[start-address : stop-address]
		if region := t.regionAtLocked(start); region != nil && region.Backend != nil {
			if err := region.Backend.ReadAt(region.FileBase+(start-region.Start), slice); err != nil {
				return err
			}
			continue
		}
		stored := t.pages[page]
		if stored == nil {
			continue
		}
		copy(slice, stored[start-base:stop-base])
	}
	return nil
}

func (t *Table) readRegionLocked(region Region, address uint64, buffer []byte) error {
	if region.Backend != nil {
		return region.Backend.ReadAt(region.FileBase+(address-region.Start), buffer)
	}
	clear(buffer)
	stored := t.pages[address/PageSize]
	if stored != nil {
		base := address / PageSize * PageSize
		copy(buffer, stored[address-base:uint64(len(buffer))+address-base])
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
	return t.writeLocked(address, buffer)
}

// writeLocked 在已持锁状态下写入;调用方负责长度非零与上下文检查。
func (t *Table) writeLocked(address uint64, buffer []byte) error {
	length := uint64(len(buffer))
	if err := t.checkLocked(address, length, ProtWrite); err != nil {
		return err
	}
	if t.pages == nil {
		t.pages = make(map[uint64][]byte)
	}
	end := address + length
	t.invalidateCodeLocked(address, end)
	for page := address / PageSize; page <= (end-1)/PageSize; page++ {
		base := page * PageSize
		start := max(address, base)
		stop := min(end, base+PageSize)
		slice := buffer[start-address : stop-address]
		if region := t.regionAtLocked(start); region != nil && region.Backend != nil {
			if err := region.Backend.WriteAt(region.FileBase+(start-region.Start), slice); err != nil {
				return err
			}
			continue
		}
		stored := t.pages[page]
		if stored == nil {
			stored = make([]byte, PageSize)
			t.pages[page] = stored
		}
		copy(stored[start-base:stop-base], slice)
	}
	return nil
}

// AtomicModify 在同一内存锁内完成读改写(AMO/LR-SC):operation 返回
// 写回值,返回值为修改前的原始值。width 只接受 4 或 8。
func (t *Table) AtomicModify(ctx context.Context, address uint64, width int, operation func(uint64) uint64) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if width != 4 && width != 8 {
		return 0, errno.Wrap(errno.Invalid, "原子访问宽度必须为 4 或 8")
	}
	if address%uint64(width) != 0 {
		return 0, errno.Wrap(errno.Invalid, "原子访问地址未对齐")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	var raw [8]byte
	if err := t.readLocked(address, raw[:width], ProtRead); err != nil {
		return 0, err
	}
	var old uint64
	if width == 4 {
		old = uint64(binary.LittleEndian.Uint32(raw[:4]))
	} else {
		old = binary.LittleEndian.Uint64(raw[:8])
	}
	updated := operation(old)
	if width == 4 {
		binary.LittleEndian.PutUint32(raw[:4], uint32(updated))
	} else {
		binary.LittleEndian.PutUint64(raw[:8], updated)
	}
	if err := t.writeLocked(address, raw[:width]); err != nil {
		return 0, err
	}
	return old, nil
}

// regionAtLocked 返回包含地址的区间(区间按起始地址有序,二分);
// 调用方必须持有锁。
// SharedKeyAt 返回地址所在共享后备区间的键(文件共享映射,futex
// 键控加盐,K10):fork 父子共享同一后备实例,键相同;无关进程的
// 各自映射键不同。非共享区间返回 false。
func (t *Table) SharedKeyAt(address uint64) (uint64, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	region := t.regionAtLocked(address)
	if region == nil || region.Backend == nil {
		return 0, false
	}
	salter, ok := region.Backend.(interface{ FutexSalt() uint64 })
	if !ok {
		return 0, false
	}
	return salter.FutexSalt(), true
}

func (t *Table) regionAtLocked(address uint64) *Region {
	index := sort.Search(len(t.regions), func(i int) bool {
		return t.regions[i].End > address
	})
	if index < len(t.regions) && t.regions[index].Contains(address) {
		return &t.regions[index]
	}
	return nil
}

// Load 无视保护位把内容写入页表(装载语义:文件私有映射的初始
// 快照,E08);范围必须完整落在既有映射内。与 Write 不同,写只读
// 映射的快照内容不是客体可见的写访问,不受保护位约束。
func (t *Table) Load(ctx context.Context, address uint64, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	length := uint64(len(content))
	if !t.coversLocked(address, address+length) {
		return faultError(FaultUnmapped, address, "装载范围未完整映射")
	}
	if t.pages == nil {
		t.pages = make(map[uint64][]byte)
	}
	end := address + length
	t.invalidateCodeLocked(address, end)
	for page := address / PageSize; page <= (end-1)/PageSize; page++ {
		base := page * PageSize
		start := max(address, base)
		stop := min(end, base+PageSize)
		stored := t.pages[page]
		if stored == nil {
			stored = make([]byte, PageSize)
			t.pages[page] = stored
		}
		copy(stored[start-base:stop-base], content[start-address:stop-address])
	}
	return nil
}

// Sync 检查范围完整映射后返回(共享映射写入即时直通后备,没有
// 待冲刷的脏页;私有映射本就不写回)。范围有空洞报 ENOMEM。
func (t *Table) Sync(ctx context.Context, start, end uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRange(start, end); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.coversLocked(start, end) {
		return errno.Wrap(errno.NoMemory, "范围未完整映射")
	}
	return nil
}

func (t *Table) dropPagesLocked(start, end uint64) {
	t.invalidateCodeLocked(start, end)
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

// PrivateBytes 返回私有页字节总量(物理页计费口径,E15):内容落
// 本表页表的字节;文件共享映射区间直通后备不落页表,不计入——
// 共享数据只在创建映射的地址空间计费一次,fork 克隆不重复扣费。
func (t *Table) PrivateBytes() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return uint64(len(t.pages)) * PageSize
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

// faultWithKind 让调用方经 errors.Is 同时识别具体故障类别
// (SEGV 映射错/保护错的分类依据)与 errno 身份。
type faultWithKind struct {
	kind Fault
	body error
}

func (e faultWithKind) Error() string { return e.body.Error() }

func (e faultWithKind) Unwrap() []error { return []error{e.kind, e.body} }

func faultError(fault Fault, address uint64, detail string) error {
	identity := errno.Range
	switch fault {
	case FaultMisaligned, FaultOverflow:
		identity = errno.Invalid
	case FaultProtection:
		identity = errno.Permission
	}
	return faultWithKind{kind: fault,
		body: errno.Wrap(identity, fault.String()+"@"+utoa(address)+": "+detail)}
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
	t.invalidateCodeLocked(region.Start, region.End)
	index := sort.Search(len(t.regions), func(i int) bool {
		return t.regions[i].Start >= region.Start
	})
	t.regions = append(t.regions, Region{})
	copy(t.regions[index+1:], t.regions[index:])
	t.regions[index] = region
}

// Resize 原地缩放一个恰好匹配的映射:收缩丢弃尾部页,扩张要求
// [oldEnd,newEnd) 空闲;失败不改动任何映射。
func (t *Table) Resize(ctx context.Context, oldStart, oldEnd, newEnd uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRange(oldStart, oldEnd); err != nil {
		return err
	}
	if newEnd <= oldStart || newEnd%PageSize != 0 {
		return errno.Wrap(errno.Invalid, "新终点必须页对齐且大于起点")
	}
	if newEnd > maxAddress {
		return faultError(FaultOverflow, oldStart, "新终点超出客体地址空间")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	index := -1
	for i, region := range t.regions {
		if region.Start == oldStart && region.End == oldEnd {
			index = i
			break
		}
	}
	if index < 0 {
		return faultError(FaultUnmapped, oldStart, "缩放目标必须恰好是一个映射")
	}
	if newEnd == oldEnd {
		return nil
	}
	if newEnd < oldEnd {
		t.dropPagesLocked(newEnd, oldEnd)
		t.regions[index].End = newEnd
		return nil
	}
	tail := Region{Start: oldEnd, End: newEnd}
	for _, region := range t.regions {
		if region.Overlaps(tail) {
			return errno.Wrap(errno.Exist, "扩张区间与既有映射重叠: "+utoa(region.Start)+"-"+utoa(region.End))
		}
	}
	kept := make([]Region, 0, len(t.regions))
	kept = append(kept, t.regions...)
	kept[index].End = newEnd
	if err := t.checkBudgetAfterLocked(kept, 0); err != nil {
		return err
	}
	t.regions = kept
	return nil
}

// Move 把一个恰好匹配的映射连同页内容移动到 newStart;
// 目标范围必须空闲,保护位与数据保持不变。
func (t *Table) Move(ctx context.Context, oldStart, oldEnd, newStart uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRange(oldStart, oldEnd); err != nil {
		return err
	}
	size := oldEnd - oldStart
	if newStart%PageSize != 0 || newStart+size <= newStart {
		return errno.Wrap(errno.Invalid, "新起点必须页对齐")
	}
	if err := validateRange(newStart, newStart+size); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	index := -1
	for i, region := range t.regions {
		if region.Start == oldStart && region.End == oldEnd {
			index = i
			break
		}
	}
	if index < 0 {
		return faultError(FaultUnmapped, oldStart, "移动目标必须恰好是一个映射")
	}
	if newStart < oldEnd && newStart+size > oldStart {
		return errno.Wrap(errno.Invalid, "目标区间与源映射重叠")
	}
	target := t.regions[index]
	target.Start, target.End = newStart, newStart+size
	for _, region := range t.regions {
		if region.Start != oldStart && region.Overlaps(target) {
			return errno.Wrap(errno.Exist, "目标区间与既有映射重叠: "+utoa(region.Start)+"-"+utoa(region.End))
		}
	}
	base := oldStart / PageSize
	offset := newStart / PageSize
	if t.pages != nil {
		for page := base; page < oldEnd/PageSize; page++ {
			if stored, ok := t.pages[page]; ok {
				t.pages[offset+page-base] = stored
			}
		}
	}
	t.dropPagesLocked(oldStart, oldEnd)
	kept := make([]Region, 0, len(t.regions))
	kept = append(kept, t.regions[:index]...)
	kept = append(kept, t.regions[index+1:]...)
	t.regions = kept
	t.insertLocked(target)
	return nil
}

// DropPages 丢弃覆盖范围内的页内容(映射保留,匿名页回到零填充),
// 对应 MADV_DONTNEED;范围必须被完整映射。
func (t *Table) DropPages(ctx context.Context, start, end uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRange(start, end); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.coversLocked(start, end) {
		return faultError(FaultUnmapped, start, "丢弃范围未被完整映射")
	}
	t.dropPagesLocked(start, end)
	return nil
}

// Replace 映射 region 并替换与其重叠的既有映射,对应 Linux MAP_FIXED;
// noReplace 时遇到重叠返回 EEXIST 且不产生任何改动,失败不留半成品。
func (t *Table) Replace(ctx context.Context, region Region, noReplace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRegion(region); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := make([]Region, 0, len(t.regions)+1)
	for _, existing := range t.regions {
		if !existing.Overlaps(region) {
			kept = append(kept, existing)
			continue
		}
		if noReplace {
			return errno.Wrap(errno.Exist, "NOREPLACE 命中既有映射: "+utoa(existing.Start)+"-"+utoa(existing.End))
		}
		if existing.Start < region.Start {
			left := existing
			left.End = region.Start
			kept = append(kept, left)
		}
		if region.End < existing.End {
			right := existing
			right.Start = region.End
			kept = append(kept, right)
		}
	}
	if err := t.checkBudgetAfterLocked(kept, region.Size()); err != nil {
		return err
	}
	kept = append(kept, region)
	slices.SortFunc(kept, func(a, b Region) int { return cmp.Compare(a.Start, b.Start) })
	t.regions = kept
	t.dropPagesLocked(region.Start, region.End)
	return nil
}

func (t *Table) checkBudgetAfterLocked(kept []Region, size uint64) error {
	if t.limit == 0 {
		return nil
	}
	var mapped uint64
	for _, region := range kept {
		mapped += region.Size()
	}
	if mapped+size > t.limit {
		return errno.Wrap(errno.NoSpace, "映射超出地址空间预算")
	}
	return nil
}

func (t *Table) findContainingLocked(address uint64) int {
	index := sort.Search(len(t.regions), func(i int) bool {
		return t.regions[i].End > address
	})
	if index < len(t.regions) && t.regions[index].Contains(address) {
		return index
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
