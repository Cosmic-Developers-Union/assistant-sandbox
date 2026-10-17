package df

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type capacityStub struct {
	vmfs.System
	spaces  []vmfs.Space
	listErr error

	pathErr error

	byPath map[string]vmfs.Space
}

func (c capacityStub) Space(_ context.Context, name string) (vmfs.Space, error) {
	if c.pathErr != nil {
		return vmfs.Space{}, c.pathErr
	}
	if space, ok := c.byPath[name]; ok {
		return space, nil
	}
	if len(c.spaces) == 0 {
		return vmfs.Space{}, errno.NotFound
	}
	return c.spaces[0], nil
}

func (c capacityStub) Filesystems(context.Context) ([]vmfs.Space, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.spaces, nil
}

func newSystem(t *testing.T) vmfs.System {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return system
}

func withSystem(t *testing.T, system vmfs.System, run commandio.Handler) commandio.Handler {
	t.Helper()
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		set.FS = system
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}

func declared(total, used, free uint64) vmfs.Quota {
	return vmfs.Quota{Declared: true, Total: total, Used: used, Free: free}
}

func TestPortableFormatMatchesPOSIX(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/work", Root: "/work",

		Bytes: declared(1000, 100, 900),
	}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})
	want := "Filesystem 512-blocks Used Available Capacity Mounted on\n/work 2 1 2 10% /work\n"
	if result.Code != 0 || result.Stdout != want || result.Stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestPercentageRoundsUp(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/", Root: "/", Bytes: declared(4000, 100, 2900),
	}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})
	if !strings.Contains(result.Stdout, " 4% /") {
		t.Fatalf("百分比应向上取整: %q", result.Stdout)
	}
}

func TestKilobytesSwitchesUnits(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/", Root: "/", Bytes: declared(1000, 100, 900),
	}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-k", "-P"}, "",
		commandtest.Options{})
	want := "Filesystem 1024-blocks Used Available Capacity Mounted on\n/ 1 1 1 10% /\n"
	if result.Stdout != want {
		t.Fatalf("stdout=%q", result.Stdout)
	}
}

func TestDefaultFormatCarriesFreeInodes(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/", Root: "/", Bytes: declared(1024, 512, 512),
		Inodes: declared(10, 2, 8),
	}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), nil, "", commandtest.Options{})
	want := "Filesystem 512-blocks Used Available Capacity Mounted on Inodes IFree\n/ 2 1 1 50% / 10 8\n"
	if result.Stdout != want {
		t.Fatalf("stdout=%q", result.Stdout)
	}
}

func TestTotalOptionIsAcceptedAndPortableIsExclusive(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/", Root: "/", Bytes: declared(1024, 512, 512), Inodes: declared(10, 2, 8),
	}}}
	withTotal := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-t"}, "",
		commandtest.Options{})
	plain := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), nil, "", commandtest.Options{})
	if withTotal.Stdout != plain.Stdout || withTotal.Code != 0 {
		t.Fatalf("-t 不应改变输出: %q vs %q", withTotal.Stdout, plain.Stdout)
	}
	both := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P", "-t"}, "",
		commandtest.Options{})
	if both.Code != 2 || !strings.Contains(both.Stderr, "互斥") {
		t.Fatalf("-P 与 -t 同时给应被拒绝: code=%d stderr=%q", both.Code, both.Stderr)
	}
}

func TestUndeclaredCapacityPrintsDash(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/", Root: "/",
		Bytes:  vmfs.Quota{Used: 1024},
		Inodes: declared(10, 2, 8),
	}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})
	want := "Filesystem 512-blocks Used Available Capacity Mounted on\n/ - 2 - - /\n"
	if result.Stdout != want {
		t.Fatalf("stdout=%q", result.Stdout)
	}
}

func TestZeroDenominatorIsZeroPercent(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
		Name: "/", Root: "/", Bytes: declared(0, 0, 0),
	}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})
	if !strings.Contains(result.Stdout, "/ 0 0 0 0% /\n") {
		t.Fatalf("stdout=%q", result.Stdout)
	}
}

func TestWithoutCapacityFailsHonestly(t *testing.T) {
	base, err := readonly.New(newSystem(t))
	if err != nil {
		t.Fatal(err)
	}
	result := commandtest.RunWith(t, withSystem(t, base, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})
	if result.Code != 2 || !strings.Contains(result.Stderr, "没有容量能力") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestOperandsDeduplicateAndReportFailures(t *testing.T) {
	left := vmfs.Space{Name: "/a", Root: "/a", Bytes: declared(1024, 0, 1024)}
	right := vmfs.Space{Name: "/b", Root: "/b", Bytes: declared(2048, 1024, 1024)}
	system := capacityStub{
		System: newSystem(t),
		byPath: map[string]vmfs.Space{"/a/one": left, "/a/two": left, "/b/one": right},

		spaces: nil,
	}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(),
		[]string{"-P", "/a/one", "/a/two", "/b/one", "/missing"}, "", commandtest.Options{})
	if result.Code != 1 {
		t.Fatalf("有操作数失败应以 1 退出: code=%d stderr=%q", result.Code, result.Stderr)
	}
	if !strings.Contains(result.Stderr, "/missing") {
		t.Fatalf("失败的操作数必须出现在诊断里: %q", result.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "/a ") || !strings.HasPrefix(lines[2], "/b ") {
		t.Fatalf("同一文件系统只应输出一行: %q", result.Stdout)
	}
}

func TestNoOperandsListsEveryFilesystem(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{
		{Name: "/", Root: "/", Bytes: declared(1024, 512, 512)},
		{Name: "/raw", Root: "/raw"},
	}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], "/raw ") {
		t.Fatalf("两个文件系统都要列出: %q", result.Stdout)
	}
}

func TestEnumerationFailureIsReported(t *testing.T) {
	system := capacityStub{System: newSystem(t), listErr: errors.New("挂载表坏了")}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-P"}, "",
		commandtest.Options{})

	if result.Code != 2 || !strings.Contains(result.Stderr, "挂载表坏了") || result.Stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestAllOperandsFailingIsCommandLevelError(t *testing.T) {
	system := capacityStub{System: newSystem(t), pathErr: errno.NotFound}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(),
		[]string{"-P", "/missing"}, "", commandtest.Options{})
	if result.Code != 2 || result.Stdout != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestArgumentErrors(t *testing.T) {
	system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{Name: "/", Root: "/"}}}
	result := commandtest.RunWith(t, withSystem(t, system, Run), t.TempDir(), []string{"-z"}, "",
		commandtest.Options{})
	if result.Code != 2 || result.Stdout != "" || result.Stderr == "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

type onceWriter struct{ writes int }

func (w *onceWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return len(p), nil
	}
	return 0, io.ErrClosedPipe
}

func TestWriteFailureIsNotSwallowed(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		args   []string
		output io.Writer
	}{
		{"表头写失败", []string{"-P"}, commandtest.FailWriter{}},
		{"数据行写失败", nil, &onceWriter{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			system := capacityStub{System: newSystem(t), spaces: []vmfs.Space{{
				Name: "/", Root: "/", Bytes: declared(1024, 512, 512), Inodes: declared(10, 2, 8),
			}}}
			run := withSystem(t, system, func(ctx context.Context, args []string) error {
				p, err := proc.FromContext(ctx)
				if err != nil {
					return err
				}
				descriptors, err := p.Resources.Descriptors()
				if err != nil {
					return err
				}
				bindings, ok := descriptors.(interface{ Attach(uint64, fd.Streams) })
				if !ok {
					t.Fatalf("描述符后端类型: %T", descriptors)
				}
				bindings.Attach(uint64(p.ID), fd.Streams{
					Input: commandio.Context(ctx).Stdin, Output: testCase.output,
					Error: commandio.Context(ctx).Stderr,
				})
				return Run(proc.WithContext(ctx, p), args)
			})
			result := commandtest.RunWith(t, run, t.TempDir(), testCase.args, "", commandtest.Options{})
			if result.Code != 2 {
				t.Fatalf("写失败必须上报: code=%d stderr=%q", result.Code, result.Stderr)
			}
		})
	}
}

func TestMissingFilesystemFails(t *testing.T) {
	run := func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set := p.Resources.(capability.Set)
		set.FS = nil
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
	result := commandtest.RunWith(t, run, t.TempDir(), []string{"-P"}, "", commandtest.Options{})
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}
