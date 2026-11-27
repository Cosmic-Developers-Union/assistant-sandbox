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
	decoded []decodedBatchInstruction
	valid   bool
}

func decodedPageSlot(page uint64) uint64 {
	return (page ^ (page >> 7)) & 4095
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
	reservation := [4]uint64{c.lr.address, uint64(c.lr.width), c.lrValue, 0}
	if c.lr.active {
		reservation[3] = 1
	}
	fcsr := uint64(c.machine.FCSR)
	newPC, steps := func() (uint64, uint64) {
		defer memoryView.ReleaseMemoryView()
		return runHotBatch(c.machine.Registers, pc, c.batchBase, c.jitIdentity, limit,
			c.retired,
			regions, pageNumbers, pageAddresses, pageSize,
			c.batchBases[:], c.batchDecodedAt[:], c.batchVersions[:],
			c.batchStartSlots[:], instructionPageSize,
			reservation[:], c.machine.Floats, &fcsr)
	}()
	c.machine.FCSR = uint32(fcsr)
	c.lr.active = reservation[3] != 0
	c.lr.address = reservation[0]
	c.lr.width = int(reservation[1])
	c.lrValue = reservation[2]
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
		clear(c.batchDecodedAt[:])
		clear(c.batchVersions[:])
		clear(c.batchStartSlots[:])
	}
	slot := decodedPageSlot(base / instructionPageSize)
	if cached := &c.batchPages[slot]; cached.valid && cached.base == base && cached.version == version {
		c.batchBase = base
		c.batchVersion = version
		c.batchValid = true
		return true
	}
	slots := len(data) / 2
	decoded := make([]decodedBatchInstruction, slots)
	startSlot := uint64(0)
	if base >= instructionPageSize && c.fetcher != nil {
		if _, length, err := c.fetcher.FetchInstruction(ctx, base-2); err == nil && length == 4 {
			startSlot = 1
		}
	}
	// 每个 2 字节边界都独立译码。RISC-V 允许在压缩指令粒度上
	// 间接跳入代码页的任意偏移;若只按页首线性步进，跨页指令会使
	// 后续所有偏移错位，JIT 会把大量合法 computed-goto 目标判为
	// 不可执行。
	for offset := 0; offset+2 <= len(data); offset += 2 {
		if offset+4 <= len(data) {
			instruction := binary.LittleEndian.Uint32(data[offset:])
			if instruction&0x3 == 0x3 {
				decoded[offset/2] = decodeBatchInstruction(instruction, 4)
				continue
			}
		}
		if offset+4 > len(data) {
			if c.fetcher != nil {
				instruction, length, err := c.fetcher.FetchInstruction(
					ctx, base+uint64(offset),
				)
				if err == nil {
					switch length {
					case 4:
						decoded[offset/2] = decodeBatchInstruction(instruction, 4)
					case 2:
						expanded, status := expandCompressed(uint16(instruction))
						if status == compressedOK {
							decoded[offset/2] = decodeBatchInstruction(expanded, 2)
						}
					}
				}
			}
			// 无法跨页取指时保持 unsupported，由单步路径处理。
			continue
		}
		raw := binary.LittleEndian.Uint16(data[offset:])
		expanded, status := expandCompressed(raw)
		if status == compressedOK {
			decoded[offset/2] = decodeBatchInstruction(expanded, 2)
		}
	}
	c.batchPages[slot] = decodedInstructionPage{
		base: base, version: version, decoded: decoded, valid: true,
	}
	c.batchBases[slot] = base
	c.batchVersions[slot] = version
	c.batchStartSlots[slot] = startSlot
	// #nosec G103 -- 译码页在 C++ 调用期间由 batchPages 持有，仅暴露只读地址。
	c.batchDecodedAt[slot] = uintptr(unsafe.Pointer(unsafe.SliceData(decoded)))
	c.batchBase = base
	c.batchVersion = version
	c.batchValid = true
	return true
}

var _ cpu.BatchStepper = (*Core)(nil)
