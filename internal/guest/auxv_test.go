package guest_test

import (
	"context"
	"encoding/binary"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// asm 是测试内的两遍 RV64I 迷你汇编器(F03 探针用):
// 先按序发指令并登记标签,finish 时回填分支偏移,消除手写位模式错误。
type asm struct {
	ins    []uint32
	labels map[string]int
	fixups []struct {
		index  int
		funct3 uint32
		rs1    uint32
		rs2    uint32
		target string
	}
	patches []func(words []uint32)
}

func newAsm() *asm { return &asm{labels: map[string]int{}} }

func (a *asm) emit(word uint32) { a.ins = append(a.ins, word) }

func (a *asm) label(name string) { a.labels[name] = len(a.ins) }

// branch 登记 funct3/rs1/rs2 与目标标签,偏移在 finish 回填。
func (a *asm) branch(funct3, rs1, rs2 uint32, target string) {
	a.fixups = append(a.fixups, struct {
		index  int
		funct3 uint32
		rs1    uint32
		rs2    uint32
		target string
	}{len(a.ins), funct3, rs1, rs2, target})
	a.emit(0x63)
}

func (a *asm) finish() []uint32 {
	for _, fix := range a.fixups {
		target, ok := a.labels[fix.target]
		if !ok {
			panic("未定义标签: " + fix.target)
		}
		bits := uint32((target - fix.index) * 4)
		// B 型立即数:imm[4:1]→inst[11:8],imm[10:5]→inst[30:25],
		// imm[11]→inst[7],imm[12]→inst[31]。
		a.ins[fix.index] = 0x63 | fix.funct3<<12 | fix.rs1<<15 | fix.rs2<<20 |
			((bits>>1)&0xF)<<8 | ((bits>>5)&0x3F)<<25 |
			((bits>>11)&0x1)<<7 | ((bits>>12)&0x1)<<31
	}
	for _, patch := range a.patches {
		patch(a.ins)
	}
	return a.ins
}

func iIns(op, rd, funct3, rs1, imm uint32) uint32 {
	return op | rd<<7 | funct3<<12 | rs1<<15 | imm<<20
}

func li(rd, imm uint32) uint32 { return iIns(0x13, rd, 0, 0, imm) }

// loadImm 发射加载任意 uint32 立即数的指令序列(addi 只能编码
// ±2047,更大的值经 lui+addi 组合,addi 立即数按符号扩展补偿)。
func (a *asm) loadImm(rd, value uint32) {
	if value < 2048 {
		a.emit(li(rd, value))
		return
	}
	hi := (value + 0x800) >> 12
	lo := value & 0xFFF
	if lo >= 0x800 {
		lo |= ^uint32(0xFFF) // 符号扩展到 12 位
	}
	a.emit(0x37 | rd<<7 | (hi&0xFFFFF)<<12) // lui rd, hi
	a.emit(iIns(0x13, rd, 0, rd, lo))       // addi rd, rd, lo
}

// 寄存器编号。
const (
	x0, sp         = 0, 2
	t0, t1, t2     = 5, 6, 7
	a0, a1, a2, a7 = 10, 11, 12, 17
)

// auxvProbeProgram 生成 F03 探针指令与数据:验证 argc、argv[1]=="check"
// 前缀、envp[0]=='A' 前缀、sp 16 字节对齐、auxv 含 AT_PAGESZ=4096、
// AT_ENTRY==0x10078、AT_RANDOM 可读、以 AT_NULL 终止(≤32 项)。
// 全部通过:写出 "AUXV-OK\n" 并以 argc 退出;第 k 项失败以 100+k 退出。
func auxvProbeProgram() []uint32 {
	a := newAsm()
	fail := func(check uint32) {
		a.emit(li(a0, 100+check))
		a.emit(li(a7, 93))
		a.emit(0x00000073)
	}
	// 检查 1:sp 16 字节对齐。
	a.emit(iIns(0x13, t1, 7, sp, 15)) // andi t1, sp, 15
	a.branch(1, t1, x0, "fail1")
	// 检查 2:argc == 2。
	a.emit(iIns(0x03, a0, 3, sp, 0)) // ld a0, 0(sp)
	a.emit(li(t1, 2))
	a.branch(1, a0, t1, "fail2")
	// 检查 3:argv[1][0] == 'c'(argv[1] 为 "check")。
	a.emit(iIns(0x03, a1, 3, sp, 16)) // ld a1, 16(sp)
	a.emit(iIns(0x03, t1, 4, a1, 0))  // lbu t1, 0(a1)
	a.emit(li(t2, 'c'))
	a.branch(1, t1, t2, "fail3")
	// 检查 4:envp[0][0] == 'A'。envp 基址 = sp+8+8*(argc+1) = sp+0x20。
	a.emit(iIns(0x03, a1, 3, sp, 0x20)) // ld a1, 32(sp)
	a.emit(iIns(0x03, t1, 4, a1, 0))
	a.emit(li(t2, 'A'))
	a.branch(1, t1, t2, "fail4")
	// auxv 扫描:基址 = sp+0x20+8*(envc+1) = sp+0x30。
	a.emit(iIns(0x13, a1, 0, sp, 0x30)) // addi a1, sp, 48
	a.emit(li(t2, 0))
	a.label("loop")
	a.emit(iIns(0x03, t0, 3, a1, 0)) // ld t0, 0(a1)  key
	a.emit(iIns(0x03, t1, 3, a1, 8)) // ld t1, 8(a1)  val
	a.branch(0, t0, x0, "done")      // key==AT_NULL → 完成
	a.emit(li(a2, 6))
	a.branch(1, t0, a2, "not6")
	a.loadImm(a2, 4096)
	a.branch(1, t1, a2, "fail5")
	a.label("not6")
	a.emit(li(a2, 9))
	a.branch(1, t0, a2, "not9")
	a.loadImm(a2, 0x10078)
	a.branch(1, t1, a2, "fail6")
	a.label("not9")
	a.emit(li(a2, 25))
	a.branch(1, t0, a2, "next")
	a.emit(iIns(0x03, t1, 4, t1, 0)) // AT_RANDOM:读首字节验证可读
	a.label("next")
	a.emit(iIns(0x13, a1, 0, a1, 16)) // a1 += 16
	a.emit(iIns(0x13, t2, 0, t2, 1))  // i++
	a.emit(li(a2, 32))
	a.branch(5, a2, t2, "loop") // i<32 继续(bge a2,t2)
	a.emit(li(a0, 108))
	a.emit(li(a7, 93))
	a.emit(0x00000073)
	// done:write(1, msg, 9) + exit(argc)。
	a.label("done")
	a.emit(0x00000597) // auipc a1, 0
	addiAt := len(a.ins)
	a.emit(0x00000013) // addi a1,a1,imm(占位,finish 回填)
	a.patches = append(a.patches, func(words []uint32) {
		// auipc 基准是它自身 PC(位于 addi 的前一条),偏移须 +4。
		delta := (a.labels["msg"] - addiAt + 1) * 4
		words[addiAt] = iIns(0x13, a1, 0, a1, uint32(delta))
	})
	a.emit(li(a2, 8))
	a.emit(li(a7, 64))
	a.emit(li(a0, 1))
	a.emit(0x00000073)
	a.emit(iIns(0x03, a0, 3, sp, 0)) // exit(argc)
	a.emit(li(a7, 93))
	a.emit(0x00000073)
	// 消息 "AUXV-OK\n"(9 字节,小端打包为两条数据字)。
	a.label("msg")
	a.emit(0x56585541) // "AUXV"(小端:'A','U','X','V')
	a.emit(0x0A4B4F2D) // "-OK\n"
	// 失败块统一置于末尾:auipc a1,0 + addi(占位)不在失败路径中,
	// 各失败块独立 exit(100+k)。done 路径已内联,不再共享代码。
	for check := uint32(1); check <= 4; check++ {
		a.label("fail" + string(rune('0'+check)))
		fail(check)
	}
	a.label("fail5")
	fail(5)
	a.label("fail6")
	fail(6)
	return a.finish()
}

// TestInitialStackAuxvProbe(F03):真实 ELF 从初始栈读取 argc/argv/envp/
// auxv,逐项断言 libc 启动约定:对齐、AT_PAGESZ、AT_ENTRY、AT_RANDOM
// 可读、AT_NULL 终止,全部通过输出 AUXV-OK 并以 argc(=2)退出。
func TestInitialStackAuxvProbe(t *testing.T) {
	instructions := auxvProbeProgram()
	payload := make([]byte, 0, len(instructions)*4)
	for _, word := range instructions {
		var raw [4]byte
		binary.LittleEndian.PutUint32(raw[:], word)
		payload = append(payload, raw[:]...)
	}
	image := make([]byte, 0x78+len(payload))
	copy(image[0:4], "\x7fELF")
	image[4], image[5], image[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(image[16:], 2)
	binary.LittleEndian.PutUint16(image[18:], 243)
	binary.LittleEndian.PutUint32(image[20:], 1)
	binary.LittleEndian.PutUint64(image[24:], 0x10078)
	binary.LittleEndian.PutUint64(image[32:], 64)
	binary.LittleEndian.PutUint16(image[52:], 64)
	binary.LittleEndian.PutUint16(image[54:], 56)
	binary.LittleEndian.PutUint16(image[56:], 1)
	phdr := image[64:]
	binary.LittleEndian.PutUint32(phdr[0:], 1)
	binary.LittleEndian.PutUint32(phdr[4:], 5)
	binary.LittleEndian.PutUint64(phdr[8:], 0)
	binary.LittleEndian.PutUint64(phdr[16:], 0x10000)
	binary.LittleEndian.PutUint64(phdr[24:], 0x10000)
	binary.LittleEndian.PutUint64(phdr[32:], uint64(len(image)))
	binary.LittleEndian.PutUint64(phdr[40:], uint64(len(image)))
	binary.LittleEndian.PutUint64(phdr[48:], 0x1000)
	copy(image[0x78:], payload)

	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := system.Open(context.Background(), "/probe", fs.Create|fs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	const pid = 44
	table := fd.New()
	output := &strings.Builder{}
	table.Attach(pid, fd.Streams{Output: writerFunc(output.Write)})
	process := proc.Proc{
		ID: pid, Parent: 1, Program: "/probe",
		Args: []string{"/probe", "check"}, Dir: "/",
		Env: map[string]string{"A": "1"},
		Resources: capability.Set{
			FS: system, FD: table, Clock: &clock.Virtual{},
			Identity: identity.SingleUser{}, Entropy: sequenceEntropy{},
		},
	}
	program, _, err := elf.Factory{}.New("/probe")
	if err != nil {
		t.Fatal(err)
	}
	code, runErr := program.Run(context.Background(), process)
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if output.String() != "AUXV-OK\n" {
		t.Fatalf("输出=%q(检查失败码=%d)", output.String(), code)
	}
	if code != 2 {
		t.Fatalf("退出码=%d want argc=2", code)
	}
}

type writerFunc func([]byte) (int, error)

func (w writerFunc) Write(p []byte) (int, error) { return w(p) }
func (w writerFunc) Read([]byte) (int, error)    { return 0, io.EOF }
func (w writerFunc) Close() error                { return nil }

type sequenceEntropy struct{}

func (sequenceEntropy) Read(_ context.Context, buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(index)
	}
	return len(buffer), nil
}
