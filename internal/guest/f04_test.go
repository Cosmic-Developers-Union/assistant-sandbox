package guest_test

import (
	"encoding/binary"
	"testing"

	loaderelf "github.com/Cosmic-Developers-Union/assistant-sandbox/loader/elf"
)

// pieProbeProgram 生成 ET_DYN 位置无关探针指令:扫描 auxv 取
// AT_ENTRY(9),与 auipc 自身地址相减,差应等于「入口相对校验点的
// 偏移」——该期望值由 patch 按实际指令布局回填,断言随任意装载
// 基址成立;通过则写 "PIE-OK\n" 并以 argc 退出,失败 exit(99)。
func pieProbeProgram(a *asm) {
	const auxvOff = 8 + 8*3 + 8*1          // argc=2、envc=0 → auxv 基址偏移
	a.emit(iIns(0x13, a1, 0, sp, auxvOff)) // addi a1, sp, auxvOff
	a.emit(li(t2, 0))
	a.label("scan")
	a.emit(iIns(0x03, t0, 3, a1, 0)) // ld t0, 0(a1) key
	a.emit(iIns(0x03, t1, 3, a1, 8)) // ld t1, 8(a1) val
	a.branch(0, t0, x0, "noscan")    // AT_NULL 前未见 → 98
	a.emit(li(a2, 9))
	a.branch(0, t0, a2, "check")      // key==9 → 校验
	a.emit(iIns(0x13, a1, 0, a1, 16)) // a1 += 16
	a.emit(iIns(0x13, t2, 0, t2, 1))  // i++
	a.emit(li(a2, 16))
	a.branch(5, a2, t2, "scan")
	// 校验:AT_ENTRY − PC == 期望偏移(0x78 − 校验点索引×4)。
	a.label("check")
	a.emit(0x00000297)                      // auipc t0, 0
	a.emit(rIns(0x33, t0, 0, t1, 0x20, t0)) // sub t0, t1, t0
	expectAt := len(a.ins)
	a.emit(0x00000013) // li t2, imm(占位)
	a.patches = append(a.patches, func(words []uint32) {
		// AT_ENTRY 与校验点同基:差 = −校验点相对程序首的偏移。
		expect := int32(-(a.labels["check"] * 4))
		words[expectAt] = iIns(0x13, t2, 0, x0, uint32(expect))
	})
	a.branch(1, t0, t2, "fail")
	// 通过:write "PIE-OK\n" + exit(argc)。
	a.emit(0x00000597) // auipc a1, 0
	addiAt := len(a.ins)
	a.emit(0x00000013)
	a.patches = append(a.patches, func(words []uint32) {
		delta := (a.labels["msg"] - addiAt + 1) * 4
		words[addiAt] = iIns(0x13, a1, 0, a1, uint32(delta))
	})
	a.emit(li(a2, 7))
	a.emit(li(a7, 64))
	a.emit(li(a0, 1))
	a.emit(0x00000073)
	a.emit(iIns(0x03, a0, 3, sp, 0)) // exit(argc)
	a.emit(li(a7, 93))
	a.emit(0x00000073)
	a.label("msg")
	a.emit(0x2D454950) // "PIE-"
	a.emit(0x000A4B4F) // "OK\n\0"
	a.label("noscan")
	a.emit(li(a0, 98))
	a.emit(li(a7, 93))
	a.emit(0x00000073)
	a.label("fail")
	a.emit(li(a0, 99))
	a.emit(li(a7, 93))
	a.emit(0x00000073)
}

func pieProbeImage(t *testing.T) []byte {
	t.Helper()
	a := newAsm()
	pieProbeProgram(a)
	image := wrapStaticELF(a.finish())
	binary.LittleEndian.PutUint16(image[16:], 3) // ET_DYN
	return image
}

// TestPIEProgramRunsAtDeterministicBase(F04 执行轴):ET_DYN 探针经
// executor 以确定性 PIE 基址启动——入口与辅助向量随基址平移,且
// 程序真实执行(位置无关断言通过、输出与退出码正确)。
func TestPIEProgramRunsAtDeterministicBase(t *testing.T) {
	image := pieProbeImage(t)
	output, code := runProbeWithExit(t, image, []string{"/probe", "arg"}, nil)
	if output != "PIE-OK\n" {
		t.Fatalf("输出=%q 退出=%d", output, code)
	}
}

// TestPIEPlanShiftsAcrossBases(F04 布局轴):装载器对同一 ET_DYN 映像
// 以多个合法基址生成计划——入口、程序头地址与全部段地址随基址
// 精确平移,不同基址启动同一程序的布局正确。
func TestPIEPlanShiftsAcrossBases(t *testing.T) {
	image := pieProbeImage(t)
	bases := []uint64{0, 0x1000, 0x20000000, 0x3F000000}
	var zero *loaderelf.Plan
	for _, base := range bases {
		plan, err := loaderelf.Load(image, loaderelf.Options{
			LoadBias: base, Argv: []string{"prog"}, ExecPath: "/probe",
		})
		if err != nil {
			t.Fatalf("bias=%#x: %v", base, err)
		}
		if plan.Bias != base {
			t.Fatalf("bias=%#x 计划基址=%#x", base, plan.Bias)
		}
		if zero == nil {
			zero = plan
			continue
		}
		if plan.Entry != zero.Entry+base {
			t.Fatalf("bias=%#x 入口=%#x want %#x", base, plan.Entry, zero.Entry+base)
		}
		if plan.ProgramAddress != zero.ProgramAddress+base {
			t.Fatalf("bias=%#x 程序头=%#x", base, plan.ProgramAddress)
		}
		if len(plan.Segments) != len(zero.Segments) {
			t.Fatal("段数随基址变化")
		}
		for index := range plan.Segments {
			if plan.Segments[index].Address != zero.Segments[index].Address+base {
				t.Fatalf("bias=%#x 段 %d 未平移", base, index)
			}
		}
	}
}
