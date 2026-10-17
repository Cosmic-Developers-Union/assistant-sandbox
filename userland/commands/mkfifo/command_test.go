package mkfifo

import (
	"context"
	stdfs "io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type fifoStub struct {
	vmfs.System
	created  []string
	modes    []stdfs.FileMode
	chmods   []stdfs.FileMode
	createAt map[string]error
	chmodErr error
}

func (s *fifoStub) Mkfifo(_ context.Context, name string, mode stdfs.FileMode) error {
	if err := s.createAt[name]; err != nil {
		return err
	}
	s.created = append(s.created, name)
	s.modes = append(s.modes, mode)
	return nil
}

func (s *fifoStub) Chmod(_ context.Context, _ string, mode stdfs.FileMode) error {
	if s.chmodErr != nil {
		return s.chmodErr
	}
	s.chmods = append(s.chmods, mode)
	return nil
}

type fifoOnly struct {
	vmfs.System
	fifos vmfs.Fifos
}

func (f fifoOnly) Mkfifo(ctx context.Context, name string, mode stdfs.FileMode) error {
	return f.fifos.Mkfifo(ctx, name, mode)
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

func TestCreatesWithDefaultMode(t *testing.T) {
	stub := &fifoStub{System: newSystem(t)}
	result := commandtest.RunWith(t, withSystem(t, stub, Run), t.TempDir(),
		[]string{"one", "two"}, "", commandtest.Options{})
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if strings.Join(stub.created, ",") != "one,two" {
		t.Fatalf("创建顺序应按操作数给定: %v", stub.created)
	}
	for _, mode := range stub.modes {
		if mode != defaultMode {
			t.Fatalf("默认模式应是 0666: %v", stub.modes)
		}
	}
	if len(stub.chmods) != 0 {
		t.Fatalf("没有 -m 时不应改权限: %v", stub.chmods)
	}
}

func TestModeIsAppliedAfterCreation(t *testing.T) {
	stub := &fifoStub{System: newSystem(t)}
	result := commandtest.RunWith(t, withSystem(t, stub, Run), t.TempDir(),
		[]string{"-m", "640", "pipe"}, "", commandtest.Options{})
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if len(stub.modes) != 1 || stub.modes[0] != defaultMode {
		t.Fatalf("创建时必须是 0666: %v", stub.modes)
	}
	if len(stub.chmods) != 1 || stub.chmods[0] != 0o640 {
		t.Fatalf("随后应收紧到 0640: %v", stub.chmods)
	}
}

func TestSymbolicModeIsRefused(t *testing.T) {
	stub := &fifoStub{System: newSystem(t)}
	result := commandtest.RunWith(t, withSystem(t, stub, Run), t.TempDir(),
		[]string{"-m", "u=rw", "pipe"}, "", commandtest.Options{})
	if result.Code != 2 || !strings.Contains(result.Stderr, "八进制") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if len(stub.created) != 0 {
		t.Fatal("参数错误时不应创建任何节点")
	}
}

func TestModeWithoutChmodPortFailsBeforeCreating(t *testing.T) {
	stub := &fifoStub{System: newSystem(t)}
	result := commandtest.RunWith(t, withSystem(t, fifoOnly{System: stub, fifos: stub}, Run), t.TempDir(),
		[]string{"-m", "600", "pipe"}, "", commandtest.Options{})
	if result.Code != 2 || !strings.Contains(result.Stderr, "模式位能力") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if len(stub.created) != 0 {
		t.Fatalf("不能留下权限不对的节点: %v", stub.created)
	}
}

func TestPerOperandFailureKeepsGoing(t *testing.T) {
	stub := &fifoStub{System: newSystem(t), createAt: map[string]error{"taken": errno.Exist}}
	result := commandtest.RunWith(t, withSystem(t, stub, Run), t.TempDir(),
		[]string{"taken", "fresh"}, "", commandtest.Options{})
	if result.Code != 1 || !strings.Contains(result.Stderr, "taken") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if strings.Join(stub.created, ",") != "fresh" {
		t.Fatalf("其余操作数仍应创建: %v", stub.created)
	}
}

func TestChmodFailureIsReported(t *testing.T) {
	stub := &fifoStub{System: newSystem(t), chmodErr: errno.NotSupported}
	result := commandtest.RunWith(t, withSystem(t, stub, Run), t.TempDir(),
		[]string{"-m", "600", "pipe"}, "", commandtest.Options{})
	if result.Code != 1 || !strings.Contains(result.Stderr, "设置权限位") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestWithoutCapabilityFails(t *testing.T) {
	base, err := readonly.New(newSystem(t))
	if err != nil {
		t.Fatal(err)
	}
	result := commandtest.RunWith(t, withSystem(t, base, Run), t.TempDir(),
		[]string{"pipe"}, "", commandtest.Options{})
	if result.Code != 2 || !strings.Contains(result.Stderr, "不能创建命名管道") {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}

func TestArgumentBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		code int
	}{
		{"缺操作数", nil, 2},
		{"未知选项", []string{"-z", "pipe"}, 2},
		{"越界八进制", []string{"-m", "10000", "pipe"}, 2},
		{"-- 之后按操作数", []string{"--", "-weird"}, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stub := &fifoStub{System: newSystem(t)}
			result := commandtest.RunWith(t, withSystem(t, stub, Run), t.TempDir(),
				testCase.args, "", commandtest.Options{})
			if result.Code != testCase.code {
				t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
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
	result := commandtest.RunWith(t, run, t.TempDir(), []string{"pipe"}, "", commandtest.Options{})
	if result.Code != 2 || result.Stderr == "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
}
