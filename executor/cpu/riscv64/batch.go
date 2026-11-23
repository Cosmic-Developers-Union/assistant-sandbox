package riscv64

import (
	"context"
	"encoding/binary"
	"unsafe"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const instructionPageSize = 4096

type memoryViewProvider interface {
	AcquireMemoryView(context.Context) ([]uint64, []uint64, []uintptr, uint64, bool, error)
	ReleaseMemoryView()
}

type decodedInstructionPage struct {
	base    uint64
	version uint64
	words   []uint32
	lengths []uint8
	valid   bool
}

func decodedPageSlot(page uint64) uint64 {
	return (page ^ (page >> 7)) & 255
}

// BatchEnabled 报告当前配置能否进入 C++ 批量整数执行路径。
func (c *Core) BatchEnabled() bool {
	if !hotBatchSupported || c.pageFetcher == nil {
		return false
	}
	if _, ok := c.memory.(memoryViewProvider); !ok {
		return false
	}
	return c.batchAllowed
}

// StepBatch 执行至多 limit 条整数、分支和常规访存指令。遇到系统调用、
// 浮点、原子或 CSR 指令时返回已退休条数;调用方随后用 Step 单步处理。
func (c *Core) StepBatch(ctx context.Context, limit uint64) (uint64, cpu.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if c.cancel != nil && c.cancel.Cancelled() {
		return 0, 0, errno.Wrap(errno.Cancelled, "执行被取消")
	}
	if c.lr.active {
		outcome, err := c.Step(ctx)
		return 1, outcome, err
	}
	if !c.BatchEnabled() || limit == 0 {
		outcome, err := c.Step(ctx)
		return 1, outcome, err
	}
	pc := c.machine.PC
	if !c.prepareBatchPage(ctx, pc) {
		outcome, err := c.Step(ctx)
		return 1, outcome, err
	}
	memoryView, ok := c.memory.(memoryViewProvider)
	if !ok {
		outcome, err := c.Step(ctx)
		return 1, outcome, err
	}
	regions, pageNumbers, pageAddresses, pageSize, ok, err := memoryView.AcquireMemoryView(ctx)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		outcome, err := c.Step(ctx)
		return 1, outcome, err
	}
	newPC, steps := func() (uint64, uint64) {
		defer memoryView.ReleaseMemoryView()
		return runHotBatch(c.machine.Registers, pc, c.batchBase, limit,
			regions, pageNumbers, pageAddresses, pageSize,
			c.batchBases[:], c.batchWordsAt[:], c.batchBytesAt[:], instructionPageSize)
	}()
	if steps == 0 {
		outcome, err := c.Step(ctx)
		return 1, outcome, err
	}
	c.machine.PC = newPC
	c.retired += steps
	if c.meter != nil {
		if err := c.meter.Charge(steps); err != nil {
			return steps, cpu.Continued, err
		}
	}
	return steps, cpu.Continued, nil
}

func (c *Core) prepareBatchPage(ctx context.Context, pc uint64) bool {
	base, data, version, ok, err := c.pageFetcher.FetchInstructionPage(ctx, pc)
	if err != nil || !ok || len(data) < 2 {
		return false
	}
	if c.batchValid && base == c.batchBase && version == c.batchVersion {
		return true
	}
	if c.batchValid && version != c.batchVersion {
		clear(c.batchPages[:])
		clear(c.batchBases[:])
		clear(c.batchWordsAt[:])
		clear(c.batchBytesAt[:])
	}
	slot := decodedPageSlot(base / instructionPageSize)
	if cached := &c.batchPages[slot]; cached.valid && cached.base == base && cached.version == version {
		c.batchBase = base
		c.batchVersion = version
		c.batchValid = true
		return true
	}
	slots := len(data) / 2
	words := make([]uint32, slots)
	lengths := make([]uint8, slots)
	for offset := 0; offset+2 <= len(data); {
		if offset%2 != 0 {
			break
		}
		if offset+4 > len(data) {
			// 页尾指令可能跨页，交给单步取指（可跨相邻映射）处理。
			break
		}
		instruction := binary.LittleEndian.Uint32(data[offset:])
		if instruction&0x3 == 0x3 {
			words[offset/2] = instruction
			lengths[offset/2] = 4
			offset += 4
			continue
		}
		raw := binary.LittleEndian.Uint16(data[offset:])
		expanded, status := expandCompressed(raw)
		if status == compressedOK {
			words[offset/2] = expanded
			lengths[offset/2] = 2
		}
		offset += 2
	}
	c.batchPages[slot] = decodedInstructionPage{
		base: base, version: version, words: words, lengths: lengths, valid: true,
	}
	c.batchBases[slot] = base
	// #nosec G103 -- 译码页在 C++ 调用期间由 batchPages 持有，仅暴露只读地址。
	c.batchWordsAt[slot] = uintptr(unsafe.Pointer(unsafe.SliceData(words)))
	// #nosec G103 -- 同上；lengths 由 Core 持有并在调用期间保持存活。
	c.batchBytesAt[slot] = uintptr(unsafe.Pointer(unsafe.SliceData(lengths)))
	c.batchBase = base
	c.batchVersion = version
	c.batchValid = true
	return true
}

var _ cpu.BatchStepper = (*Core)(nil)
