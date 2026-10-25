package guest_test

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// ---- 汇编器扩展(D02 差分向量用)。 ----

func sIns(funct3, rs1, rs2, imm uint32) uint32 {
	return 0x23 | (imm&0x1F)<<7 | funct3<<12 | rs1<<15 | rs2<<20 | ((imm>>5)&0x7F)<<25
}

func rIns(op, rd, funct3, rs1, funct7, rs2 uint32) uint32 {
	return op | rd<<7 | funct3<<12 | rs1<<15 | funct7<<25 | rs2<<20
}

// loadImm64 生成加载任意 uint64 的指令序列。RV64 中 lui/addi 的
// 立即数符号扩展到 64 位,不能按 32 位语义拼装;改为从高位到低位
// 的分段霍纳构造:[63:55][54:44][43:33][32:22][21:11][10:0],每段
// ≤0x7FF(addi 正立即数天然零扩展),先 slli 上一段宽再 or。
func (a *asm) loadImm64(rdi, value uint64) {
	rd := uint32(rdi)
	segments := []struct {
		shamt uint
		width uint32
	}{{55, 9}, {44, 11}, {33, 11}, {22, 11}, {11, 11}, {0, 11}}
	for index, segment := range segments {
		seg := uint32((value >> segment.shamt) & ((1 << segment.width) - 1))
		if index == 0 {
			a.emit(li(rd, seg)) // rd = 最高段
			continue
		}
		// 为当前段腾位:slli 量 = 当前段宽(此前内容的总位移恰为
		// 64 − 上一段低边界 − 当前段宽,与霍纳一致)。
		a.emit(iIns(0x13, rd, 1, rd, segment.width))
		if seg != 0 {
			a.emit(li(5, seg))
			a.emit(rIns(0x33, rd, 6, rd, 0x00, 5)) // or rd, rd, t0
		}
	}
}

// d02Vector 是一条整数指令差分向量:setup 设置 x1/x2,ins 为目标指令
// (rd=x3),want 为 x3 的规范期望值。
type d02Vector struct {
	label string
	a, b  uint64 // x8(s0)、x9(s1) 初值;不得使用 x1(ra)/x2(sp)
	ins   uint32
	want  uint64
}

// d02Branch 是分支向量:模式 li x3,1; b?? x8,x9,+8(跳过 li x3,0),
// 结果 1=taken/0=not-taken。
type d02Branch struct {
	label  string
	a, b   uint64
	funct3 uint32
	want   uint64
}

// d02Vectors:立即数、符号扩展、溢出、分支相关算术与寄存器零值的
// 逐条向量(分支指令本身由 taken 布尔向量单独覆盖)。
func d02Vectors() []d02Vector {
	const inA, inB = 8, 9 // s0、s1
	imm := func(funct3, imm uint32) uint32 { return iIns(0x13, 3, funct3, inA, imm) }
	op := func(funct3, funct7 uint32) uint32 { return rIns(0x33, 3, funct3, inA, funct7, inB) }
	opw := func(funct3, funct7 uint32) uint32 { return rIns(0x3B, 3, funct3, inA, funct7, inB) }
	const maxI64 = uint64(0x7FFFFFFFFFFFFFFF)
	return []d02Vector{
		{"addi 最小立即数", 0, 0, imm(0, 0x800), 0xFFFFFFFFFFFFF800},
		{"addi 最大立即数", 0, 0, imm(0, 0x7FF), 0x7FF},
		{"addi 寄存器零值", 0, 0, imm(0, 0), 0},
		{"add 有符号溢出回绕", maxI64, 1, op(0, 0x00), 0x8000000000000000},
		{"sub 下溢回绕", 0, 1, op(0, 0x20), 0xFFFFFFFFFFFFFFFF},
		{"slt max<-1 为假", maxI64, 0xFFFFFFFFFFFFFFFF, op(2, 0x00), 0},
		{"sltu max<max 为假", maxI64, maxI64, op(3, 0x00), 0},
		{"sltiu 全 1<1 为假", 0xFFFFFFFFFFFFFFFF, 0, imm(3, 1), 0},
		{"slti -2048<-1 为真", 0xFFFFFFFFFFFFF800, 0, imm(2, 0xFFF), 1},
		{"srai -1>>63 全 1", 0xFFFFFFFFFFFFFFFF, 0, imm(5, 0x400|63), 0xFFFFFFFFFFFFFFFF},
		{"srli 全 1>>63 得 1", 0xFFFFFFFFFFFFFFFF, 0, imm(5, 0x3F), 1},
		{"slli 1<<63", 1, 0, imm(1, 63), 0x8000000000000000},
		{"addw 0x7FFFFFFF+1 符号扩展", 0x7FFFFFFF, 1, opw(0, 0x00), 0xFFFFFFFF80000000},
		{"addiw 0x80000000 符号扩展", 0x80000000, 0, iIns(0x1B, 3, 0, inA, 0), 0xFFFFFFFF80000000},
		{"sltiu imm -1(=0xFFF…FF)", 5, 0, imm(3, 0xFFF), 1},
		{"slliw 1<<31 回绕符号扩展", 1, 0, iIns(0x1B, 3, 1, inA, 31), 0xFFFFFFFF80000000},
		{"sraiw 0x80000000>>31 为 -1", 0x80000000, 0, iIns(0x1B, 3, 5, inA, (0x20<<5)|31), 0xFFFFFFFFFFFFFFFF},
		{"xor 全 1^0xFF", 0xFFFFFFFFFFFFFFFF, 0xFF, op(4, 0x00), 0xFFFFFFFFFFFFFF00},
		{"and 负值", 0xF0F0F0F0F0F0F0F0, 0x0FF00FF00FF00FF0, op(7, 0x00), 0x00F000F000F000F0},
		{"or 稀疏位", 0xF0F0F0F0F0F0F0F0, 0x0F0F0F0F0F0F0F0F, op(6, 0x00), 0xFFFFFFFFFFFFFFFF},
		{"lui x3,0xFFFFF 负高位", 0, 0, 0xFFFFF000 | 0x37 | 3<<7, 0xFFFFFFFFFFFFF000},
	}
}

// d02Branches:beq/blt/bltu 的 taken 与 not-taken 落点值。
func d02Branches() []d02Branch {
	branchVec := func(label string, a, b uint64, funct3, want uint64) d02Branch {
		return d02Branch{label: label, a: a, b: b, funct3: uint32(funct3), want: want}
	}
	return []d02Branch{
		branchVec("beq 相等真", 0x1234, 0x1234, 0, 1),
		branchVec("beq 不等假", 0x1234, 0x4321, 0, 0),
		branchVec("blt 负<正 真", 0x8000000000000000, 0x7FFFFFFFFFFFFFFF, 4, 1),
		branchVec("blt 正<负 假", 0x7FFFFFFFFFFFFFFF, 0x8000000000000000, 4, 0),
		branchVec("bltu max<大 真", 0x7FFFFFFFFFFFFFFF, 0x8000000000000000, 6, 1),
		branchVec("bltu 大<max 假", 0x8000000000000000, 0x7FFFFFFFFFFFFFFF, 6, 0),
	}
}

// buildD02Probe 生成逐条整数向量探针:每条向量设置 x8/x9、执行目标
// 指令(rd=x3)、sd 结果到 sp 下方;分支向量模式 li x3,1; b?? +8;
// li x3,0。末尾 write(1, sp-8N, 8N) 后 exit(0)。
func buildD02Probe(vectors []d02Vector, branches []d02Branch) []uint32 {
	a := newAsm()
	n := len(vectors) + len(branches)
	for index, vector := range vectors {
		a.loadImm64(8, vector.a)
		a.loadImm64(9, vector.b)
		a.emit(vector.ins)
		a.emit(sIns(3, sp, 3, uint32(-int32(8*(n-index)))))
	}
	for index, vector := range branches {
		a.loadImm64(8, vector.a)
		a.loadImm64(9, vector.b)
		// li x3,1;b?? x8,x9,+8(跳过 li x3,0)。
		a.emit(li(3, 1))
		// B 型偏移 8:imm[4:1]=0100 → inst[11:8];imm[11]/imm[12]=0。
		a.emit(0x63 | vector.funct3<<12 | 8<<15 | 9<<20 | ((8>>1)&0xF)<<8)
		a.emit(li(3, 0))
		a.emit(sIns(3, sp, 3, uint32(-int32(8*(n-len(vectors)-index)))))
	}
	a.emit(iIns(0x13, 11, 0, sp, uint32(-int32(8*n)))) // addi a1, sp, -8n
	a.emit(li(a2, uint32(8*n)))
	a.emit(li(a7, 64))
	a.emit(li(a0, 1))
	a.emit(0x00000073)
	a.emit(li(a7, 93))
	a.emit(li(a0, 0))
	a.emit(0x00000073)
	return a.finish()
}

// wrapStaticELF 把指令序列包成与 guest.Probe 同布局的静态 ELF。
func wrapStaticELF(instructions []uint32) []byte {
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
	return image
}

// runProbeWithExit 执行并返回 stdout 与退出码。
func runProbeWithExit(t *testing.T, image []byte, args []string, env map[string]string) (string, uint8) {
	t.Helper()
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
	const pid = 56
	table := fd.New()
	output := &strings.Builder{}
	table.Attach(pid, fd.Streams{Output: writerFunc(output.Write)})
	process := proc.Proc{
		ID: pid, Parent: 1, Program: "/probe",
		Args: args, Dir: "/", Env: env,
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
	return output.String(), code
}

// runProbeOnVM 在 VM 内执行映像并收集 stdout。
func runProbeOnVM(t *testing.T, image []byte, args []string, env map[string]string) string {
	t.Helper()
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
	const pid = 55
	table := fd.New()
	output := &strings.Builder{}
	table.Attach(pid, fd.Streams{Output: writerFunc(output.Write)})
	process := proc.Proc{
		ID: pid, Parent: 1, Program: "/probe",
		Args: args, Dir: "/", Env: env,
		Resources: capability.Set{
			FS: system, FD: table, Clock: &clock.Virtual{},
			Identity: identity.SingleUser{}, Entropy: sequenceEntropy{},
		},
	}
	program, _, err := elf.Factory{}.New("/probe")
	if err != nil {
		t.Fatal(err)
	}
	if _, runErr := program.Run(context.Background(), process); runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	return output.String()
}

// TestIntegerInstructionVectorsAgainstSpec(D02):逐条整数与分支向量
// 在 VM 内执行,结果与规范期望值逐位比对。
func TestIntegerInstructionVectorsAgainstSpec(t *testing.T) {
	vectors := d02Vectors()
	branches := d02Branches()
	image := wrapStaticELF(buildD02Probe(vectors, branches))
	output := runProbeOnVM(t, image, []string{"/probe"}, nil)
	if len(output) != 8*(len(vectors)+len(branches)) {
		t.Fatalf("输出 %d 字节 want %d", len(output), 8*(len(vectors)+len(branches)))
	}
	for index, vector := range vectors {
		got := binary.LittleEndian.Uint64([]byte(output[index*8 : index*8+8]))
		if got != vector.want {
			t.Errorf("%s: x3=%#016x want %#016x", vector.label, got, vector.want)
		}
	}
	base := len(vectors) * 8
	for index, vector := range branches {
		got := binary.LittleEndian.Uint64([]byte(output[base+index*8 : base+index*8+8]))
		if got != vector.want {
			t.Errorf("%s: 落点=%d want %d", vector.label, got, vector.want)
		}
	}
}

// TestIntegerInstructionVectorsMatchReferenceLinux(D02 差分):同一份
// 字节码在 qemu-riscv64 参考执行,逐字节一致;参考不可用时显式说明。
func TestIntegerInstructionVectorsMatchReferenceLinux(t *testing.T) {
	vectors := d02Vectors()
	branches := d02Branches()
	image := wrapStaticELF(buildD02Probe(vectors, branches))
	reference, err := diff.ReferenceLinux(t.Context(), image, "")
	if err != nil {
		t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖)", err)
	}
	vm := runProbeOnVM(t, image, []string{"/probe"}, nil)
	if reference.Stdout != vm {
		t.Fatalf("差分失败:参考 %d 字节 vs VM %d 字节", len(reference.Stdout), len(vm))
	}
	if reference.ExitCode != 0 {
		t.Fatalf("参考退出码=%d want 0", reference.ExitCode)
	}
}
