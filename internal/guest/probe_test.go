package guest_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// TestProbeBuildIsDeterministic:A08——两次构建字节一致,摘要与记录值
// 相等;跨进程一致由 CI 的 -count=1 重复执行隐含覆盖。
func TestProbeBuildIsDeterministic(t *testing.T) {
	first, second := guest.Probe(), guest.Probe()
	if !bytes.Equal(first, second) {
		t.Fatal("两次构建产物不一致")
	}
	if digest := guest.SourceDigest(); digest != guest.ProbeSourceSHA256 {
		t.Fatalf("probe.asm 摘要 %s 与记录值不一致(源码变更须显式更新)", digest)
	}
	if digest := guest.ImageDigest(); digest != guest.ProbeImageSHA256 {
		t.Fatalf("产物摘要 %s 与记录值不一致(编码/布局变更须显式更新)", digest)
	}
}

// TestProbeStructureIsValidForReferenceLinux:布局满足静态 ELF 的
// 内核装载约束——单 PT_LOAD、offset≡vaddr (mod align)、入口落在
// 段内、机器/类别正确;参考 Linux 的实际执行由 A09 差分执行器覆盖。
func TestProbeStructureIsValidForReferenceLinux(t *testing.T) {
	image := guest.Probe()
	if !bytes.Equal(image[0:4], []byte("\x7fELF")) || image[4] != 2 || image[5] != 1 {
		t.Fatal("ELF 头标识/类别/端序错误")
	}
	if binary.LittleEndian.Uint16(image[16:]) != 2 {
		t.Fatal("须为 ET_EXEC")
	}
	if machine := binary.LittleEndian.Uint16(image[18:]); machine != 243 {
		t.Fatalf("e_machine=%d want EM_RISCV(243)", machine)
	}
	entry := binary.LittleEndian.Uint64(image[24:])
	if count := binary.LittleEndian.Uint16(image[56:]); count != 1 {
		t.Fatalf("e_phnum=%d want 1", count)
	}
	phdr := image[binary.LittleEndian.Uint64(image[32:]):]
	offset := binary.LittleEndian.Uint64(phdr[8:])
	vaddr := binary.LittleEndian.Uint64(phdr[16:])
	filesz := binary.LittleEndian.Uint64(phdr[32:])
	memsz := binary.LittleEndian.Uint64(phdr[40:])
	align := binary.LittleEndian.Uint64(phdr[48:])
	if binary.LittleEndian.Uint32(phdr[0:]) != 1 {
		t.Fatal("段须为 PT_LOAD")
	}
	if offset%align != vaddr%align {
		t.Fatalf("offset=%#x vaddr=%#x 违反页同余约束", offset, vaddr)
	}
	if entry < vaddr || entry >= vaddr+memsz {
		t.Fatalf("entry=%#x 不在段 [%#x,%#x) 内", entry, vaddr, vaddr+memsz)
	}
	if uint64(len(image)) < offset+filesz {
		t.Fatal("文件长度不足 filesz")
	}
}

// TestProbeRunsOnVM:A08——同一份字节码在本 VM 内执行:写出
// "guest-ok\n" 并以 42 退出。
func TestProbeRunsOnVM(t *testing.T) {
	image := guest.Probe()
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

	const pid = 33
	table := fd.New()
	output := &stringStream{}
	table.Attach(pid, fd.Streams{Output: output})
	process := proc.Proc{
		ID: pid, Parent: 1, Program: "/probe",
		Args: []string{"/probe"}, Dir: "/",
		Resources: capability.Set{
			FS:       system,
			FD:       table,
			Clock:    &clock.Virtual{},
			Identity: identity.SingleUser{},
			Entropy:  counterEntropy{},
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
	if code != 42 {
		t.Fatalf("退出码=%d want 42", code)
	}
	if output.String() != "guest-ok\n" {
		t.Fatalf("输出=%q want %q", output.String(), "guest-ok\n")
	}
}

// TestProbeArtifactIsWrittenForDifferentialUse:差分执行器(A09)从
// 磁盘读取同一份产物;构建器提供导出能力。
func TestProbeArtifactIsWrittenForDifferentialUse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guest-probe")
	if err := os.WriteFile(path, guest.Probe(), 0o755); err != nil {
		t.Fatal(err)
	}
	back, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if guest.ImageDigest() != guest.ImageDigestOf(back) {
		t.Fatal("落盘产物与构建器摘要不一致")
	}
}

type stringStream struct{ buffer strings.Builder }

func (s *stringStream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *stringStream) Write(data []byte) (int, error) { return s.buffer.Write(data) }

func (s *stringStream) Close() error { return nil }

func (s *stringStream) String() string { return s.buffer.String() }

type counterEntropy struct{}

func (counterEntropy) Read(_ context.Context, buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(index)
	}
	return len(buffer), nil
}
