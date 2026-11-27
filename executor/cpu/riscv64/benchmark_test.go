package riscv64

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/cpu"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/memory"
)

func benchmarkLoopCore(b *testing.B) (*Core, *Core, cpu.Machine, int) {
	b.Helper()
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		b.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[0:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[4:], encodeBranch(0x1, 5, 6, -4))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		b.Fatal(err)
	}
	clone, err := table.Clone()
	if err != nil {
		b.Fatal(err)
	}
	batchCore, err := New(cpu.Config{Memory: table, Entry: batchCodeBase})
	if err != nil {
		b.Fatal(err)
	}
	stepCore, err := New(cpu.Config{Memory: clone, Entry: batchCodeBase})
	if err != nil {
		b.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[6] = ^uint64(0)
	if err := batchCore.Reset(ctx, state); err != nil {
		b.Fatal(err)
	}
	if err := stepCore.Reset(ctx, state); err != nil {
		b.Fatal(err)
	}
	return batchCore, stepCore, state, 8192
}

func BenchmarkStepBatch(b *testing.B) {
	if !hotBatchSupported {
		b.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	batchCore, _, _, limit := benchmarkLoopCore(b)
	b.ReportAllocs()
	b.ResetTimer()
	var steps uint64
	for b.Loop() {
		ran, _, err := batchCore.StepBatch(ctx, uint64(limit))
		if err != nil {
			b.Fatal(err)
		}
		steps += ran
	}
	b.StopTimer()
	b.ReportMetric(float64(steps)/b.Elapsed().Seconds()/1e6, "MIPS")
}

func BenchmarkStepSingle(b *testing.B) {
	ctx := context.Background()
	_, stepCore, _, limit := benchmarkLoopCore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for range limit {
			outcome, err := stepCore.Step(ctx)
			if err != nil {
				b.Fatal(err)
			}
			if outcome != cpu.Continued {
				b.Fatalf("outcome=%v", outcome)
			}
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(limit)*float64(b.N)/b.Elapsed().Seconds()/1e6, "MIPS")
}

func benchmarkCrossPageCore(b *testing.B, jump uint32, target uint64) (*Core, uint64) {
	b.Helper()
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + 2*memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		b.Fatal(err)
	}
	program := make([]byte, 2*memory.PageSize)
	binary.LittleEndian.PutUint32(program[4:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[8:], jump)
	binary.LittleEndian.PutUint32(program[memory.PageSize+4:], encodeBranch(0x6, 5, 6, -4096))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		b.Fatal(err)
	}
	core, err := New(cpu.Config{Memory: table, Entry: batchCodeBase + 4})
	if err != nil {
		b.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase + 4,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[6] = ^uint64(0)
	if target != 0 {
		state.Registers[7] = target
	}
	if err := core.Reset(ctx, state); err != nil {
		b.Fatal(err)
	}
	return core, 8192
}

func BenchmarkStepBatchCrossPageJAL(b *testing.B) {
	if !hotBatchSupported {
		b.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	core, limit := benchmarkCrossPageCore(b, encodeJAL(0, int32(memory.PageSize-4)), 0)
	b.ReportAllocs()
	b.ResetTimer()
	var steps uint64
	for b.Loop() {
		ran, _, err := core.StepBatch(ctx, limit)
		if err != nil {
			b.Fatal(err)
		}
		steps += ran
	}
	b.StopTimer()
	b.ReportMetric(float64(steps)/b.Elapsed().Seconds()/1e6, "MIPS")
}

func BenchmarkStepBatchCrossPageJALR(b *testing.B) {
	if !hotBatchSupported {
		b.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	core, limit := benchmarkCrossPageCore(b, encodeJALR(0, 7, 0),
		batchCodeBase+memory.PageSize+4)
	b.ReportAllocs()
	b.ResetTimer()
	var steps uint64
	for b.Loop() {
		ran, _, err := core.StepBatch(ctx, limit)
		if err != nil {
			b.Fatal(err)
		}
		steps += ran
	}
	b.StopTimer()
	b.ReportMetric(float64(steps)/b.Elapsed().Seconds()/1e6, "MIPS")
}

func BenchmarkStepBatchInPageJAL(b *testing.B) {
	if !hotBatchSupported {
		b.Skip("当前构建未启用 C++ 批量执行")
	}
	ctx := context.Background()
	table := memory.New(0)
	if err := table.Map(ctx, memory.Region{
		Start: batchCodeBase, End: batchCodeBase + memory.PageSize,
		Prot: memory.ProtRead | memory.ProtExec, Name: "code",
	}); err != nil {
		b.Fatal(err)
	}
	program := make([]byte, memory.PageSize)
	binary.LittleEndian.PutUint32(program[4:], encodeOpImm(0x0, 5, 5, 1))
	binary.LittleEndian.PutUint32(program[8:], encodeJAL(0, 4))
	binary.LittleEndian.PutUint32(program[12:], encodeBranch(0x6, 5, 6, -8))
	if err := table.Load(ctx, batchCodeBase, program); err != nil {
		b.Fatal(err)
	}
	core, err := New(cpu.Config{Memory: table, Entry: batchCodeBase + 4})
	if err != nil {
		b.Fatal(err)
	}
	state := cpu.Machine{
		PC:        batchCodeBase + 4,
		Registers: make([]uint64, numRegs),
		Floats:    make([]uint64, numFPRegs),
	}
	state.Registers[6] = ^uint64(0)
	if err := core.Reset(ctx, state); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var steps uint64
	for b.Loop() {
		ran, _, err := core.StepBatch(ctx, 8192)
		if err != nil {
			b.Fatal(err)
		}
		steps += ran
	}
	b.StopTimer()
	b.ReportMetric(float64(steps)/b.Elapsed().Seconds()/1e6, "MIPS")
}
