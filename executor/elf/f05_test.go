package elf

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// buildInterpImage 构造带 PT_INTERP 的 ET_DYN 主映像:解释器路径
// 放入数据段,程序头 programInterp(3) 指向 [offset, offset+len)。
func buildInterpImage(t *testing.T, program []uint32, interp string) []byte {
	t.Helper()
	raw := make([]byte, imageDataOffset+0x100)
	copy(raw[0:4], "\x7fELF")
	raw[4], raw[5], raw[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(raw[16:], 3) // ET_DYN
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], imageEntry)
	binary.LittleEndian.PutUint64(raw[32:], imagePhdrOffset)
	binary.LittleEndian.PutUint16(raw[52:], imageHeaderSize)
	binary.LittleEndian.PutUint16(raw[54:], imagePhdrSize)
	binary.LittleEndian.PutUint16(raw[56:], 3)
	putProgramHeader(raw[imagePhdrOffset:], 1, 5, 0, imageCodeBase, imageCodeSize, imageCodeSize, 0x1000)
	putProgramHeader(raw[imagePhdrOffset+imagePhdrSize:], 1, 6, imageDataOffset, imageDataBase, 0x100, 0x100, 0x1000)
	// PT_INTERP:路径内容位于数据段偏移 0,文件偏移 imageDataOffset。
	copy(raw[imageDataOffset:], interp+"\x00")
	putProgramHeader(raw[imagePhdrOffset+imagePhdrSize*2:], 3, 4, imageDataOffset, imageDataBase, uint64(len(interp)+1), uint64(len(interp)+1), 1)
	for index, instruction := range program {
		binary.LittleEndian.PutUint32(raw[imageProgramAt+index*4:], instruction)
	}
	return raw
}

// interpreterBody 是伪动态解释器:打印 "INTERP-OK\n" 后以 0 退出,
// 证明执行流从解释器映像启动(F05/F06 装载语义可观察)。
var interpreterBody = []uint32{
	0x00000597, // auipc a1, 0
	0x02858593, // addi a1, a1, 40 → 消息(第 11 条指令起)
	0x00A00613, // li a2, 10
	0x04000893, // li a7, 64
	0x00100513, // li a0, 1
	0x00000073, // ecall
	0x00000513, // li a0, 0
	0x05D00893, // li a7, 93
	0x00000073, // ecall
	0x00000013, // nop(对齐占位)
	0x45544E49, // "INTE"
	0x4F2D5052, // "RP-O"
	0x00000A4B, // "K\n\0"
}

// TestInterpreterMissingFailsClosed(F05):PT_INTERP 指向客体文件系统
// 不存在的路径时,装载返回明确 ENOENT(携带客体路径),不回退、
// 不触碰宿主文件系统(harness 的 memfs 即全部可见范围)。
func TestInterpreterMissingFailsClosed(t *testing.T) {
	image := buildInterpImage(t, interpreterBody[:2], "/lib/ld-musl-riscv64-sf1.so")
	harness := newHarness(t, image, nil)
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := program.Run(context.Background(), harness.process)
	if runErr == nil {
		t.Fatal("解释器缺失应失败")
	}
	if !errors.Is(runErr, errno.NotFound) {
		t.Fatalf("错误=%v want ENOENT", runErr)
	}
	if !strings.Contains(runErr.Error(), "/lib/ld-musl-riscv64-sf1.so") {
		t.Fatalf("错误应携带客体解释器路径: %v", runErr)
	}
}

// TestInterpreterNonELFIsRejected(F05):解释器存在但不是 ELF →
// 明确装载错误(ENOEXEC 族),不静默继续。
func TestInterpreterNonELFIsRejected(t *testing.T) {
	image := buildInterpImage(t, interpreterBody[:2], "/lib/fake-ld")
	harness := newHarness(t, image, nil)
	if err := harness.system.Mkdir(context.Background(), "/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	writeGuestFile(t, harness.system, "/lib/fake-ld", []byte("#!/bin/sh\n"))
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := program.Run(context.Background(), harness.process)
	if runErr == nil {
		t.Fatal("非 ELF 解释器应失败")
	}
}

// TestInterpreterLoadsFromGuestFS(F05/F06):解释器从客体文件系统
// 装载于固定槽位,执行流自解释器入口启动,输出可观察。
func TestInterpreterLoadsFromGuestFS(t *testing.T) {
	image := buildInterpImage(t, interpreterBody[:2], "/lib/fake-ld")
	harness := newHarness(t, image, nil)
	if err := harness.system.Mkdir(context.Background(), "/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	// 伪解释器按真实 ld.so 形态以 ET_DYN 装载于解释器槽位。
	fakeLoader := buildImageWith(t, interpreterBody, "")
	binary.LittleEndian.PutUint16(fakeLoader[16:], 3)
	writeGuestFile(t, harness.system, "/lib/fake-ld", fakeLoader)
	program, _, err := Factory{}.New("/program")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := program.Run(context.Background(), harness.process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if got := harness.stdout.output.String(); !strings.HasPrefix(got, "INTERP-OK") {
		t.Fatalf("输出=%q(解释器入口未启动或消息编码错误), 退出码=%d", got, code)
	}
}
