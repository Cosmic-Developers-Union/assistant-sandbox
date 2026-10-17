package native

import (
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/contract"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/deployment"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/deployment/wasm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

func TestNativeDeploymentPassesContractSuite(t *testing.T) {
	contract.Suite(t, func() (contract.Session, error) {
		runtime, err := New(context.Background(), Options{Seed: 7})
		if err != nil {
			return nil, err
		}
		return runtime.Session(), nil
	})
}

func TestDeterministicSeedProducesIdenticalControlSequences(t *testing.T) {
	run := func(t *testing.T) []struct {
		Seq  uint64
		PID  uint64
		Kind string
	} {
		t.Helper()
		runtime, err := New(t.Context(), Options{Seed: 42})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := runtime.Kernel.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
		session := runtime.Session()
		var out syncBuffer
		process, err := session.Exec(context.Background(), contract.ExecSpec{
			Program: contract.FixtureEcho, Args: []string{"determinism"}, Dir: workRoot, Stdout: &out,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := process.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		events := runtime.Sequencer.Log()
		outEvents := make([]struct {
			Seq  uint64
			PID  uint64
			Kind string
		}, 0, len(events))
		for _, event := range events {
			outEvents = append(outEvents, struct {
				Seq  uint64
				PID  uint64
				Kind string
			}{event.Seq, event.PID, event.Kind})
		}
		return outEvents
	}
	first := run(t)
	second := run(t)
	if len(first) != len(second) {
		t.Fatalf("控制序列长度不一致: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("第 %d 个控制事件不一致: %+v vs %+v", i, first[i], second[i])
		}
	}
}

type syncBuffer struct{ data []byte }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func TestMatricesAreHonest(t *testing.T) {
	runtime, err := New(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	native := runtime.Matrix()
	if native.Name != "native" || native.Executors["builtin-go"] != deployment.Supported {
		t.Fatalf("native matrix=%+v", native)
	}

	if _, ok := native.Executors["unknown"]; ok {
		t.Fatal("不应声明未知执行器")
	}

	if native.AddressSpace != deployment.Partial {
		t.Fatalf("native 的地址空间应声明为 partial: %v", native.AddressSpace)
	}
	guest := wasm.Matrix()
	if guest.Name != "wasm" || guest.Executors["builtin-go"] != deployment.Supported {
		t.Fatalf("wasm matrix=%+v", guest)
	}
	if guest.AddressSpace != deployment.Unsupported {
		t.Fatalf("wasm 的地址空间应声明为不支持: %v", guest.AddressSpace)
	}
	if guest.Signals != deployment.Unsupported || guest.Snapshot != deployment.Unsupported {
		t.Fatal("WASM 部署未装配的能力必须声明为不支持")
	}
}

func TestSessionBudgetRejectsOverspend(t *testing.T) {
	runtime, err := New(t.Context(), Options{
		Session: resources.Limits{Memory: 1 << 20, Work: 4},
		Process: resources.Limits{Memory: 1 << 20, Work: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Kernel.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	session := runtime.Session()
	var out syncBuffer

	for range 3 {
		process, err := session.Exec(context.Background(), contract.ExecSpec{
			Program: contract.FixtureEcho, Args: []string{"x"}, Dir: workRoot, Stdout: &out,
		})
		if err != nil {
			t.Fatal(err)
		}
		if state, err := process.Wait(context.Background()); err != nil || state.Code != 0 {
			t.Fatalf("预算内执行应成功: %+v err=%v", state, err)
		}
	}

	if _, err := session.Exec(context.Background(), contract.ExecSpec{
		Program: contract.FixtureEcho, Dir: workRoot, Stdout: &out,
	}); err == nil {
		t.Fatal("会话工作量预算耗尽后必须拒绝新任务")
	}
}

var elfWriteProgram = []uint32{
	0x00100513,
	0x000105B7,
	0x20058593,
	0x00200613,
	0x04000893,
	0x00000073,
	0x00700513,
	0x05D00893,
	0x00000073,
}

var elfReadProgram = []uint32{
	0xF9C00513,
	0x000105B7,
	0x30058593,
	0x00000613,
	0x00000693,
	0x03800893,
	0x00000073,
	0x000125B7,
	0x00200613,
	0x03F00893,
	0x00000073,
	0x00100513,
	0x000125B7,
	0x00200613,
	0x04000893,
	0x00000073,
	0x00700513,
	0x05D00893,
	0x00000073,
}

func elfFixture(t *testing.T, program []uint32, path string) []byte {
	t.Helper()
	const (
		codeBase   = 0x10000
		entry      = 0x10100
		messageAt  = 0x200
		programAt  = 0x100
		dataOffset = 0x2000
		codeSize   = 0x1000
	)
	raw := make([]byte, dataOffset+0x10)
	copy(raw[0:4], "\x7fELF")
	raw[4], raw[5], raw[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], entry)
	binary.LittleEndian.PutUint64(raw[32:], 0x40)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 2)
	putNativePhdr(raw[0x40:], 1, 5, 0, codeBase, codeSize, codeSize)
	putNativePhdr(raw[0x78:], 1, 6, dataOffset, 0x12000, 0x10, 0x100)
	copy(raw[messageAt:], "hi")
	if path != "" {
		copy(raw[0x300:], append([]byte(path), 0))
	}
	for index, instruction := range program {
		binary.LittleEndian.PutUint32(raw[programAt+index*4:], instruction)
	}
	return raw
}

func putNativePhdr(dst []byte, kind, flags uint32, offset, vaddr, filesz, memsz uint64) {
	binary.LittleEndian.PutUint32(dst[0:], kind)
	binary.LittleEndian.PutUint32(dst[4:], flags)
	binary.LittleEndian.PutUint64(dst[8:], offset)
	binary.LittleEndian.PutUint64(dst[16:], vaddr)
	binary.LittleEndian.PutUint64(dst[32:], filesz)
	binary.LittleEndian.PutUint64(dst[40:], memsz)
	binary.LittleEndian.PutUint64(dst[48:], 0x1000)
}

func TestNativeRunsELFProgram(t *testing.T) {
	ctx := context.Background()
	runtime, err := New(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/work/program"
	file, err := runtime.FS.Open(ctx, path, vmfs.Create|vmfs.WriteOnly, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(elfFixture(t, elfWriteProgram, "")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	process, err := runtime.Kernel.Begin(0, path, []string{path}, workRoot, nil, fd.Streams{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runtime.Kernel.RunRegistered(ctx, process)
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 7 || status.Signal != 0 {
		t.Fatalf("终态=%+v want 退出码 7", status)
	}
	if output.String() != "hi" {
		t.Fatalf("标准输出=%q want hi", output.String())
	}
}

func TestNativeRunsProgramThatOpensFile(t *testing.T) {
	ctx := context.Background()
	runtime, err := New(ctx, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"/work/program": "", "/work/data": "hi"} {
		file, err := runtime.FS.Open(ctx, path, vmfs.Create|vmfs.WriteOnly, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte(content)
		if path == "/work/program" {
			payload = elfFixture(t, elfReadProgram, "/work/data")
		}
		if _, err := file.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	process, err := runtime.Kernel.Begin(0, "/work/program", []string{"/work/program"}, workRoot, nil, fd.Streams{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runtime.Kernel.RunRegistered(ctx, process)
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 7 {
		t.Fatalf("终态=%+v want 退出码 7", status)
	}
	if output.String() != "hi" {
		t.Fatalf("标准输出=%q want hi（openat 读文件后写出）", output.String())
	}

	space, err := runtime.Kernel.AddressSpace(process.ID)
	if err != nil {
		t.Fatalf("地址空间未登记: %v", err)
	}
	regions, err := space.Regions(ctx)
	if err != nil || len(regions) == 0 {
		t.Fatalf("登记的地址空间=%v err=%v", regions, err)
	}
}

func TestNativeELFMatrixIsHonest(t *testing.T) {
	runtime, err := New(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	matrix := runtime.Matrix()
	if matrix.Executors["elf"] != deployment.Partial {
		t.Fatalf("elf 应声明为 partial: %v", matrix.Executors["elf"])
	}
	joined := strings.Join(matrix.Notes, "；")
	for _, fragment := range []string{"ELF", "动态链接", "mmap"} {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("ELF 的边界说明缺少 %q: %s", fragment, joined)
		}
	}
}

var elfSpinProgram = []uint32{
	0x00100513,
	0x0000006F,
}

func TestNativeStopsELFProgramThatSpins(t *testing.T) {
	ctx := context.Background()
	runtime, err := New(ctx, Options{Process: resources.Limits{Work: 2000, Memory: 64 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/work/spin"
	file, err := runtime.FS.Open(ctx, path, vmfs.Create|vmfs.WriteOnly, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(elfFixture(t, elfSpinProgram, "")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	process, err := runtime.Kernel.Begin(0, path, []string{path}, workRoot, nil, fd.Streams{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := runtime.Kernel.RunRegistered(ctx, process)

	if err == nil && status.Code == 0 {
		t.Fatalf("死循环没有在预算内停下: %+v", status)
	}
}
