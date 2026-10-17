package getconf

import (
	"context"
	"errors"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type configControl struct {
	childMax  int64
	argMax    int64
	configErr error
}

func (c configControl) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c configControl) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (c configControl) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c configControl) SystemConfiguration() (proc.SystemConfiguration, error) {
	if c.configErr != nil {
		return proc.SystemConfiguration{}, c.configErr
	}
	return proc.SystemConfiguration{BootTime: 3600 * 1e9, BootDeclared: true, ChildMax: c.childMax, ArgMax: c.argMax}, nil
}

type bareControl struct{}

func (bareControl) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (bareControl) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (bareControl) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}

func runWith(t *testing.T, options commandtest.Options, args ...string) commandtest.Result {
	t.Helper()
	return commandtest.RunWith(t, Run, t.TempDir(), args, "", options)
}

func TestGetconfReportsDeclaredFacts(t *testing.T) {
	options := commandtest.Options{Control: configControl{childMax: 8, argMax: 131072}}
	if r := runWith(t, options, "CHILD_MAX"); r.Code != 0 || r.Stdout != "8\n" {
		t.Fatalf("CHILD_MAX = %+v", r)
	}
	if r := runWith(t, options, "ARG_MAX"); r.Code != 0 || r.Stdout != "131072\n" {
		t.Fatalf("ARG_MAX = %+v", r)
	}
	if r := runWith(t, options, "-a"); r.Code != 0 || r.Stdout != "CHILD_MAX 8\nARG_MAX 131072\n" {
		t.Fatalf("-a = %+v", r)
	}

	if r := runWith(t, options, "-v", "POSIX_V6_ILP32_OFFBIG", "CHILD_MAX"); r.Code != 0 || r.Stdout != "8\n" {
		t.Fatalf("-v = %+v", r)
	}
}

func TestGetconfRejectsUndeclaredFacts(t *testing.T) {
	options := commandtest.Options{Control: configControl{childMax: 8, argMax: 131072}}

	for _, name := range []string{"PATH_MAX", "NAME_MAX", "LINK_MAX", "CHILD_MAX X"} {
		args := strings.Fields(name)
		r := runWith(t, options, args...)
		if r.Code != 1 || !strings.Contains(r.Stderr, "客体没有该配置事实") {
			t.Fatalf("%s = %+v", name, r)
		}
		if r.Stdout != "" {
			t.Fatalf("%s 不应有输出: %+v", name, r)
		}
	}

	partial := commandtest.Options{Control: configControl{argMax: 131072}}
	if r := runWith(t, partial, "CHILD_MAX"); r.Code != 1 {
		t.Fatalf("零值 CHILD_MAX = %+v", r)
	}
	if r := runWith(t, partial, "-a"); r.Code != 0 || r.Stdout != "ARG_MAX 131072\n" {
		t.Fatalf("零值 -a = %+v", r)
	}
}

func TestGetconfRequiresSystemControl(t *testing.T) {

	if r := runWith(t, commandtest.Options{Control: bareControl{}}, "CHILD_MAX"); r.Code != 1 || !strings.Contains(r.Stderr, "客体没有系统配置事实") {
		t.Fatalf("bare control = %+v", r)
	}

	if r := runWith(t, commandtest.Options{}, "-a"); r.Code != 1 || !strings.Contains(r.Stderr, "客体没有系统配置事实") {
		t.Fatalf("no control = %+v", r)
	}

	broken := commandtest.Options{Control: configControl{configErr: errors.New("注入的配置查询失败")}}
	if r := runWith(t, broken, "CHILD_MAX"); r.Code != 2 {
		t.Fatalf("查询失败 = %+v", r)
	}
}

func TestGetconfUsage(t *testing.T) {
	options := commandtest.Options{Control: configControl{childMax: 8, argMax: 131072}}
	if r := runWith(t, options); r.Code != 2 {
		t.Fatalf("无操作数 = %+v", r)
	}
	if r := runWith(t, options, "-a", "CHILD_MAX"); r.Code != 2 {
		t.Fatalf("-a 带变量 = %+v", r)
	}
	if r := runWith(t, options, "--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
	if r := runWith(t, options, "-v"); r.Code != 2 {
		t.Fatalf("-v 缺值 = %+v", r)
	}
}
