package diff_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/guest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

// runOnVM 在本 VM 内执行客体映像,收集退出码与 stdout。
func runOnVM(t *testing.T, image []byte) diff.Result {
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
	const pid = 33
	table := fd.New()
	output := &builderStream{}
	diagnostic := &builderStream{}
	table.Attach(pid, fd.Streams{Output: output, Error: diagnostic})
	process := proc.Proc{
		ID: pid, Parent: 1, Program: "/probe",
		Args: []string{"/probe"}, Dir: "/",
		Resources: capability.Set{
			FS:       system,
			FD:       table,
			Clock:    &clock.Virtual{},
			Identity: identity.SingleUser{},
			Entropy:  sequenceEntropy{},
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
	return diff.Result{ExitCode: int(code), Stdout: output.String(), Stderr: diagnostic.String()}
}

// TestDifferDetectsPresetDivergence:A09 验收——差分器能发现一个
// 预设的 errno、输出与文件副作用差异,并在报告中指名客体程序与输入。
func TestDifferDetectsPresetDivergence(t *testing.T) {
	actual := runOnVM(t, guest.ENOENTProbe())
	if actual.ExitCode != 254 {
		t.Fatalf("VM 侧 openat(/missing) 退出码=%d want 254(ENOENT)", actual.ExitCode)
	}
	// 预设三轴错误期望:退出码(errno 面)、输出、文件副作用。
	preset := diff.Result{
		ExitCode: 0,
		Stdout:   "nope\n",
		Files:    map[string]string{"/out.txt": "data"},
	}
	findings := diff.Compare(guest.ENOENTProbeImageSHA256, "argv=[] stdin=∅", preset, actual)
	if len(findings) != 3 {
		t.Fatalf("应发现 3 处差异(exit-code/stdout/file-missing),实际 %d: %s", len(findings), diff.Report(findings))
	}
	report := diff.Report(findings)
	for _, needle := range []string{
		guest.ENOENTProbeImageSHA256[:16], // 客体程序摘要
		"argv=[] stdin=∅",                 // 输入
		"exit-code",
		"stdout",
		"file-missing",
	} {
		if !strings.Contains(report, needle) {
			t.Fatalf("差异报告缺少 %q:\n%s", needle, report)
		}
	}
}

// TestVMProbeBehaviourIsStable:两枚探针在 VM 侧的观察面稳定
// (guest-ok/42 与 ENOENT/254),为参考对拍提供基准。
func TestVMProbeBehaviourIsStable(t *testing.T) {
	ok := runOnVM(t, guest.Probe())
	if ok.ExitCode != 42 || ok.Stdout != "guest-ok\n" {
		t.Fatalf("probe: %+v", ok)
	}
	enoent := runOnVM(t, guest.ENOENTProbe())
	if enoent.ExitCode != 254 || enoent.Stdout != "" {
		t.Fatalf("enoent: %+v", enoent)
	}
}

// TestProbeMatchesReferenceLinux:同一份字节码在参考 Linux(qemu
// 用户态或原生 riscv64)与本 VM 执行,观察面必须一致。参考执行器
// 不可用时显式跳过并说明入口(CI 的 differential 任务提供 qemu)。
func TestProbeMatchesReferenceLinux(t *testing.T) {
	for _, tc := range []struct {
		name   string
		image  []byte
		digest string
	}{
		{"probe", guest.Probe(), guest.ProbeImageSHA256},
		{"enoent", guest.ENOENTProbe(), guest.ENOENTProbeImageSHA256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reference, err := diff.ReferenceLinux(t.Context(), tc.image, "")
			if err != nil {
				t.Skipf("%v(由 CI differential 任务以 qemu-riscv64 覆盖;本机可安装 qemu-user-static)", err)
			}
			vm := runOnVM(t, tc.image)
			findings := diff.Compare(tc.digest, "argv=[] stdin=∅", reference, vm)
			if report := diff.Report(findings); report != "" {
				t.Fatalf("参考 Linux 与本 VM 行为不一致:\n%s", report)
			}
		})
	}
}

type builderStream struct{ inner strings.Builder }

func (s *builderStream) Read([]byte) (int, error) { return 0, io.EOF }

func (s *builderStream) Write(data []byte) (int, error) { return s.inner.Write(data) }

func (s *builderStream) Close() error { return nil }

func (s *builderStream) String() string { return s.inner.String() }

type sequenceEntropy struct{}

func (sequenceEntropy) Read(_ context.Context, buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(index)
	}
	return len(buffer), nil
}
