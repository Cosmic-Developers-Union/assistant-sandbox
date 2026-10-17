package make

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

type controlStub struct {
	specs []proc.RunSpec
	code  uint8

	codes []uint8
	err   error
}

func (c *controlStub) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotSupported
}

func (c *controlStub) Finish(proc.PID, proc.PID, uint8) error { return nil }

func (c *controlStub) Run(_ context.Context, spec proc.RunSpec) (proc.Proc, error) {
	c.specs = append(c.specs, spec)
	if c.err != nil {
		return proc.Proc{}, c.err
	}
	code := c.code
	if len(c.codes) > 0 {
		code = c.codes[0]
		c.codes = c.codes[1:]
	}
	return proc.Proc{ExitCode: code}, nil
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(path.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stamp(t *testing.T, dir, name string, mtime int64) {
	t.Helper()
	if err := filesystem.NewFilesystem(dir).SetTimes(t.Context(), path.Join(dir, name), mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func runMake(t *testing.T, dir, makefile, args string, stub *controlStub, env map[string]string) commandtest.Result {
	t.Helper()
	writeFile(t, dir, "Makefile", makefile)

	var control proc.Control
	if stub != nil {
		control = stub
	}
	return commandtest.RunWith(t, Run, dir, fields(args), "", commandtest.Options{Control: control, Env: env})
}

func fields(s string) []string {
	return strings.Fields(s)
}

func TestAssignmentForms(t *testing.T) {
	for _, test := range []struct {
		name     string
		makefile string
		args     string
		env      map[string]string
		want     string
	}{
		{"递归赋值延迟展开", "V = one\nall:\n\techo $(V)", "all", nil, "echo one\n"},
		{"简单赋值就地展开", "V = one\nW := $(V)\nV = two\nall:\n\techo $(W)$(V)", "all", nil, "echo onetwo\n"},
		{"条件赋值未定义时生效", "A ?= x\nall:\n\techo $(A)", "all", nil, "echo x\n"},
		{"条件赋值已定义时跳过", "A = y\nA ?= x\nall:\n\techo $(A)", "all", nil, "echo y\n"},
		{"追加递归变量", "A = one\nA += two\nall:\n\techo [$(A)]", "all", nil, "echo [one two]\n"},
		{"追加简单变量就地展开", "V = orig\nW := $(V)\nV = changed\nW += app-$(V)\nall:\n\techo [$(W)]", "all", nil, "echo [orig app-changed]\n"},
		{"追加到未定义变量", "C += cc\nall:\n\techo [$(C)]", "all", nil, "echo [cc]\n"},
		{"命令行赋值压过 makefile", "X = file\nall:\n\techo $(X)", "X=cmd all", nil, "echo cmd\n"},
		{"环境缺省压不过 makefile", "P = file\nall:\n\techo $(P)", "all", map[string]string{"P": "env"}, "echo file\n"},
		{"单字符变量引用", "X = y\nall:\n\techo $X", "all", nil, "echo y\n"},
		{"花括号引用", "X = y\nall:\n\techo ${X}", "all", nil, "echo y\n"},
		{"美元转义", "all:\n\techo $$HOME", "all", nil, "echo $HOME\n"},
		{"行续接拼合", "X = one \\\n\ttwo\nall:\n\techo $(X)", "all", nil, "echo one two\n"},
		{"注释剥离", "# leading comment\nX = val # trailing\nall:\n\techo $(X)", "all", nil, "echo val \n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runMake(t, t.TempDir(), test.makefile, test.args, &controlStub{}, test.env)
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("code=%d stdout=%q stderr=%q，期望 %q", result.Code, result.Stdout, result.Stderr, test.want)
			}
		})
	}
}

func TestEnvironmentOverride(t *testing.T) {
	makefile := "P = file\nall:\n\techo $(P)\n"
	env := map[string]string{"P": "env"}
	t.Run("缺省 makefile 压过环境", func(t *testing.T) {
		result := runMake(t, t.TempDir(), makefile, "all", &controlStub{}, env)
		if result.Code != 0 || result.Stdout != "echo file\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("-e 环境压过 makefile", func(t *testing.T) {
		result := runMake(t, t.TempDir(), makefile, "-e all", &controlStub{}, env)
		if result.Code != 0 || result.Stdout != "echo env\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("recipe 环境导出环境来源与命令行变量", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "P = over\nall:\n\techo x\n")
		stub := &controlStub{}
		result := commandtest.RunWith(t, Run, dir, fields("C=1 all"), "", commandtest.Options{Control: stub, Env: map[string]string{"P": "base", "U": "keep"}})
		if result.Code != 0 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
		if len(stub.specs) != 1 {
			t.Fatalf("应执行一次 recipe: %v", stub.specs)
		}
		want := map[string]string{"P": "over", "U": "keep", "C": "1"}
		for name, value := range want {
			if stub.specs[0].Env[name] != value {
				t.Fatalf("recipe 环境 %s = %q，期望 %q", name, stub.specs[0].Env[name], value)
			}
		}
	})
}

func TestRulesAndDefaultGoal(t *testing.T) {
	t.Run("默认目标取第一个非点目标", func(t *testing.T) {
		result := runMake(t, t.TempDir(), ".SUFFIXES:\nfirst:\n\techo 1\nsecond:\n\techo 2\n", "", &controlStub{}, nil)
		if result.Code != 0 || result.Stdout != "echo 1\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("显式目标按给定顺序", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "a:\n\techo a\nb:\n\techo b\n", "b a", &controlStub{}, nil)
		if result.Code != 0 || result.Stdout != "echo b\necho a\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("存在的文件且无规则视为最新", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "")
		writeFile(t, dir, "done", "")
		result := commandtest.RunWith(t, Run, dir, fields("done"), "", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "make: 'done' 是最新的\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("没有规则的目标报错", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "a:\n\techo a\n", "missing", nil, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "没有规则可以制作目标 'missing'") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("同一目标多条规则合并先决", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "b", "")
		writeFile(t, dir, "out", "")
		stamp(t, dir, "b", 300)
		stamp(t, dir, "out", 100)
		stub := &controlStub{}
		result := runMake(t, dir, "out: b\nout: \n\techo built $?\n", "", stub, nil)
		if result.Code != 0 || result.Stdout != "echo built b\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
}

func TestMtimeSemantics(t *testing.T) {
	mk := func(t *testing.T, targetM, depM int64) string {
		t.Helper()
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "out: src\n\techo rebuild\n")
		writeFile(t, dir, "out", "")
		writeFile(t, dir, "src", "")
		stamp(t, dir, "out", targetM)
		stamp(t, dir, "src", depM)
		return dir
	}
	t.Run("先决较新则重建", func(t *testing.T) {
		result := commandtest.RunWith(t, Run, mk(t, 100, 200), fields("out"), "", commandtest.Options{Control: &controlStub{}})
		if result.Code != 0 || result.Stdout != "echo rebuild\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("目标最新则不动", func(t *testing.T) {
		result := commandtest.RunWith(t, Run, mk(t, 300, 200), fields("out"), "", commandtest.Options{Control: &controlStub{}})
		if result.Code != 0 || result.Stdout != "make: 'out' 是最新的\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("时间相同不算新", func(t *testing.T) {
		result := commandtest.RunWith(t, Run, mk(t, 200, 200), fields("out"), "", commandtest.Options{Control: &controlStub{}})
		if result.Code != 0 || result.Stdout != "make: 'out' 是最新的\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("目标缺失则重建", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "out: src\n\techo rebuild\n")
		writeFile(t, dir, "src", "")
		stamp(t, dir, "src", 100)
		result := commandtest.RunWith(t, Run, dir, fields("out"), "", commandtest.Options{Control: &controlStub{}})
		if result.Code != 0 || result.Stdout != "echo rebuild\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("-q 最新退出零", func(t *testing.T) {
		result := commandtest.RunWith(t, Run, mk(t, 300, 200), fields("-q out"), "", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("-q 需要重建退出一", func(t *testing.T) {
		result := commandtest.RunWith(t, Run, mk(t, 100, 200), fields("-q out"), "", commandtest.Options{})
		if result.Code != 1 || result.Stdout != "" {
			t.Fatalf("code=%d stdout=%q", result.Code, result.Stdout)
		}
	})
	t.Run("重建过的先决触发重建", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "out: mid\n\techo rebuild out\nmid:\n\techo rebuild mid\n")
		stub := &controlStub{}
		result := commandtest.RunWith(t, Run, dir, fields("out"), "", commandtest.Options{Control: stub})
		if result.Code != 0 || result.Stdout != "echo rebuild mid\necho rebuild out\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
}

func TestSuffixRules(t *testing.T) {
	t.Run("源存在时推导", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", ".SUFFIXES:\n.c.o:\n\techo build $@ from $< stem $*\n")
		writeFile(t, dir, "x.c", "")
		stub := &controlStub{}
		result := commandtest.RunWith(t, Run, dir, fields("-n x.o"), "", commandtest.Options{Control: stub})
		if result.Code != 0 || result.Stdout != "echo build x.o from x.c stem x\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
		if len(stub.specs) != 0 {
			t.Fatalf("-n 不应执行 recipe: %v", stub.specs)
		}
	})
	t.Run("同一对后缀的后续定义覆盖", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", ".SUFFIXES:\n.c.o:\n\techo first\n.c.o:\n\techo second\n")
		writeFile(t, dir, "x.c", "")
		result := commandtest.RunWith(t, Run, dir, fields("-n x.o"), "", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "echo second\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("源不存在则没有规则", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", ".c.o:\n\techo build\n")
		result := commandtest.RunWith(t, Run, dir, fields("x.o"), "", commandtest.Options{})
		if result.Code != 2 || !strings.Contains(result.Stderr, "没有规则可以制作目标 'x.o'") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
}

func TestPhony(t *testing.T) {
	t.Run("存在同名文件也重建", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", ".PHONY: clean\nclean:\n\techo cleaning\n")
		writeFile(t, dir, "clean", "")
		stamp(t, dir, "clean", 500)
		result := commandtest.RunWith(t, Run, dir, fields("clean"), "", commandtest.Options{Control: &controlStub{}})
		if result.Code != 0 || result.Stdout != "echo cleaning\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("没有规则的 phony 目标成功", func(t *testing.T) {
		result := runMake(t, t.TempDir(), ".PHONY: clean\n", "clean", nil, nil)
		if result.Code != 0 || result.Stdout != "" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("phony 先决触发重建", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", ".PHONY: prep\nout: prep\n\techo build\n")
		writeFile(t, dir, "out", "")
		stamp(t, dir, "out", 500)
		result := commandtest.RunWith(t, Run, dir, fields("out"), "", commandtest.Options{Control: &controlStub{}})
		if result.Code != 0 || result.Stdout != "echo build\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
}

func TestRecipePrefixes(t *testing.T) {
	t.Run("@ 不回显但执行", func(t *testing.T) {
		stub := &controlStub{}
		result := runMake(t, t.TempDir(), "all:\n\t@echo hidden\n", "all", stub, nil)
		if result.Code != 0 || result.Stdout != "" || len(stub.specs) != 1 {
			t.Fatalf("code=%d stdout=%q specs=%d stderr=%q", result.Code, result.Stdout, len(stub.specs), result.Stderr)
		}
	})
	t.Run("- 前缀忽略失败", func(t *testing.T) {
		stub := &controlStub{codes: []uint8{3, 0}}
		result := runMake(t, t.TempDir(), "all:\n\t-false\n\techo after\n", "all", stub, nil)
		if result.Code != 0 || result.Stdout != "false\necho after\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "错误 3（已忽略）") {
			t.Fatalf("stderr 缺忽略诊断: %q", result.Stderr)
		}
	})
	t.Run("-n 打印所有行且不执行", func(t *testing.T) {
		stub := &controlStub{}
		result := runMake(t, t.TempDir(), "all:\n\t@echo one\n\techo three\n", "-n all", stub, nil)
		if result.Code != 0 || result.Stdout != "echo one\necho three\n" || len(stub.specs) != 0 {
			t.Fatalf("code=%d stdout=%q specs=%d stderr=%q", result.Code, result.Stdout, len(stub.specs), result.Stderr)
		}
	})
	t.Run("-n 下 + 行仍执行", func(t *testing.T) {
		stub := &controlStub{}
		result := runMake(t, t.TempDir(), "all:\n\t+echo forced\n", "-n all", stub, nil)
		if result.Code != 0 || result.Stdout != "echo forced\n" || len(stub.specs) != 1 {
			t.Fatalf("code=%d stdout=%q specs=%d stderr=%q", result.Code, result.Stdout, len(stub.specs), result.Stderr)
		}
	})
	t.Run("-s 抑制回显但执行", func(t *testing.T) {
		stub := &controlStub{}
		result := runMake(t, t.TempDir(), "all:\n\techo quiet\n", "-s all", stub, nil)
		if result.Code != 0 || result.Stdout != "" || len(stub.specs) != 1 {
			t.Fatalf("code=%d stdout=%q specs=%d stderr=%q", result.Code, result.Stdout, len(stub.specs), result.Stderr)
		}
	})
	t.Run("-q 执行加号行", func(t *testing.T) {
		stub := &controlStub{}
		result := runMake(t, t.TempDir(), "all:\n\t+echo forced\n", "-q all", stub, nil)
		if result.Code != 1 || len(stub.specs) != 1 {
			t.Fatalf("code=%d specs=%d stderr=%q", result.Code, len(stub.specs), result.Stderr)
		}
	})
	t.Run("组合前缀", func(t *testing.T) {
		stub := &controlStub{code: 2}
		result := runMake(t, t.TempDir(), "all:\n\t@-false\n", "all", stub, nil)
		if result.Code != 0 || result.Stdout != "" || !strings.Contains(result.Stderr, "已忽略") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
}

func TestFailureReporting(t *testing.T) {
	t.Run("recipe 失败按行列报告", func(t *testing.T) {
		stub := &controlStub{codes: []uint8{0, 1}}
		result := runMake(t, t.TempDir(), "all:\n\techo first\n\tfalse\n", "all", stub, nil)
		if result.Code != 2 || result.Stdout != "echo first\nfalse\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "make: *** [Makefile:3: all] 错误 1") {
			t.Fatalf("stderr=%q", result.Stderr)
		}
	})
	t.Run("缺 -k 时失败即停", func(t *testing.T) {
		stub := &controlStub{codes: []uint8{1}}
		result := runMake(t, t.TempDir(), "a:\n\tfalse\nb:\n\techo b\n", "a b", stub, nil)
		if result.Code != 2 || strings.Contains(result.Stdout, "echo b") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("-k 继续其余目标", func(t *testing.T) {
		stub := &controlStub{codes: []uint8{1, 0}}
		result := runMake(t, t.TempDir(), "a:\n\tfalse\nb:\n\techo b\n", "-k a b", stub, nil)
		if result.Code != 2 || result.Stdout != "false\necho b\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("-k 下先决失败的目标未重建", func(t *testing.T) {
		stub := &controlStub{codes: []uint8{1}}
		result := runMake(t, t.TempDir(), "dep:\n\tfalse\nall: dep\n\techo all\n", "-k all", stub, nil)
		if result.Code != 2 || strings.Contains(result.Stdout, "echo all") {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
		if !strings.Contains(result.Stderr, "因先决目标失败而未重建") {
			t.Fatalf("stderr=%q", result.Stderr)
		}
	})
	t.Run("依赖循环报错", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "a: b\nb: a\n", "a", nil, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "依赖循环涉及") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("找不到 shell 按错误 127", func(t *testing.T) {
		stub := &controlStub{err: errno.NotFound}
		result := runMake(t, t.TempDir(), "all:\n\techo x\n", "all", stub, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "错误 127") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("无法执行 shell 按错误 126", func(t *testing.T) {
		stub := &controlStub{err: errno.NoExec}
		result := runMake(t, t.TempDir(), "all:\n\techo x\n", "all", stub, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "错误 126") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("- 前缀下的启动失败按忽略", func(t *testing.T) {
		stub := &controlStub{err: errno.NotFound}
		result := runMake(t, t.TempDir(), "all:\n\t-echo x\n", "all", stub, nil)
		if result.Code != 0 || !strings.Contains(result.Stderr, "错误 127") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("缺少控制端口报错", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "all:\n\techo x\n", "all", nil, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "缺少进程控制端口") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
}

func TestRunSpecShape(t *testing.T) {
	t.Run("缺省 bash 与逐行独立 shell", func(t *testing.T) {
		dir := t.TempDir()
		stub := &controlStub{}
		result := runMake(t, dir, "all:\n\techo one\n\techo two\n", "all", stub, nil)
		if result.Code != 0 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
		if len(stub.specs) != 2 {
			t.Fatalf("每行独立 shell，应有两次调用: %v", stub.specs)
		}
		for i, want := range []string{"echo one", "echo two"} {
			spec := stub.specs[i]
			if spec.Name != "/bin/bash" || len(spec.Args) != 2 || spec.Args[0] != "-c" || spec.Args[1] != want {
				t.Fatalf("RunSpec[%d] = %+v，期望 bash -c %q", i, spec, want)
			}
			if spec.Dir != dir {
				t.Fatalf("RunSpec Dir = %q，期望 %q", spec.Dir, dir)
			}
		}
	})
	t.Run("SHELL 变量覆盖子进程名", func(t *testing.T) {
		stub := &controlStub{}
		result := runMake(t, t.TempDir(), "SHELL = /bin/sh\nall:\n\techo x\n", "all", stub, nil)
		if result.Code != 0 || len(stub.specs) != 1 || stub.specs[0].Name != "/bin/sh" {
			t.Fatalf("code=%d specs=%v stderr=%q", result.Code, stub.specs, result.Stderr)
		}
	})
}

func TestMakefileDiscovery(t *testing.T) {
	t.Run("先找 makefile 后找 Makefile", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "makefile", "all:\n\techo lower\n")
		writeFile(t, dir, "Makefile", "all:\n\techo upper\n")
		result := commandtest.RunWith(t, Run, dir, fields("-n"), "", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "echo lower\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("都找不到时退出二", func(t *testing.T) {
		result := commandtest.RunWith(t, Run, t.TempDir(), nil, "", commandtest.Options{})
		if result.Code != 2 || !strings.Contains(result.Stderr, "没有找到 makefile 或 Makefile") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("-f 可重复且依次拼合", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.mk", "V = one\nall: b\n\techo $(V)\n")
		writeFile(t, dir, "b.mk", "b:\n\techo b\n")
		result := commandtest.RunWith(t, Run, dir, fields("-n -f a.mk -f b.mk"), "", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "echo b\necho one\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("-f - 读标准输入", func(t *testing.T) {
		dir := t.TempDir()
		result := commandtest.RunWith(t, Run, dir, fields("-n -f -"), "all:\n\techo stdin\n", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "echo stdin\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("include 展开", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "include sub.mk\nall:\n\techo $(V)\n")
		writeFile(t, dir, "sub.mk", "V = from-include\n")
		result := commandtest.RunWith(t, Run, dir, fields("-n"), "", commandtest.Options{})
		if result.Code != 0 || result.Stdout != "echo from-include\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("include 找不到报错", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "include nope.mk\n", "", nil, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "nope.mk") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("递归 include 报错", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "include Makefile\n", "", nil, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "递归 include") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
}

func TestAutomaticVariables(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Makefile", "out: s1 s2\n\techo [$@][$<][$?][$*][$%]\n")
	writeFile(t, dir, "s1", "")
	writeFile(t, dir, "s2", "")
	stamp(t, dir, "s1", 100)
	stamp(t, dir, "s2", 200)
	result := commandtest.RunWith(t, Run, dir, fields("-n out"), "", commandtest.Options{})

	if result.Code != 0 || result.Stdout != "echo [out][s1][s1 s2][][]\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestParseRejections(t *testing.T) {
	for _, test := range []struct {
		name     string
		makefile string
		hint     string
	}{
		{"模式规则", "%.o: %.c\n\techo x\n", "模式规则"},
		{"函数展开", "all:\n\techo $(shell date)\n", "函数展开"},
		{"替换引用", "X = a.c\nall: $(X:.c=.o)\n\techo x\n", "GNU 扩展"},
		{"单后缀规则", ".c:\n\techo x\n", "单后缀规则"},
		{"双冒号规则", "all::\n\techo x\n", "双冒号"},
		{"不受支持的特殊目标", ".SILENT:\nall:\n\techo x\n", "特殊目标"},
		{"recipe 先于规则", "\techo x\n", "recipe 出现在任何规则之前"},
		{"无效变量名", "A B = y\n", "无效的变量名"},
		{"预期规则或赋值", "garbage line\n", "预期规则行或变量赋值"},
		{"空目标", ": src\n", "空目标"},
		{".PHONY 带 recipe", ".PHONY: clean\n\techo x\n", "不能定义 recipe"},
		{".POSIX 带先决", ".POSIX: foo\n", ".POSIX"},
		{".SUFFIXES 带依赖", ".SUFFIXES: .c\n", ".SUFFIXES"},
		{"后缀规则带先决", ".c.o: extra\n\techo x\n", "不能有先决条件"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runMake(t, t.TempDir(), test.makefile, "all", nil, nil)
			if result.Code != 2 {
				t.Fatalf("code=%d stdout=%q stderr=%q，期望码 2", result.Code, result.Stdout, result.Stderr)
			}
			if test.hint != "" && !strings.Contains(result.Stderr, test.hint) {
				t.Fatalf("stderr 缺 %q: %q", test.hint, result.Stderr)
			}
		})
	}
	t.Run("未闭合引用按原样保留", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "Makefile", "all:\n\techo $(OPEN\n")
		stub := &controlStub{}
		result := commandtest.RunWith(t, Run, dir, fields("-n all"), "", commandtest.Options{Control: stub})
		if result.Code != 0 || result.Stdout != "echo $(OPEN\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
	t.Run("空 makefile 没有目标", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "", "", nil, nil)
		if result.Code != 2 || !strings.Contains(result.Stderr, "没有指定目标") {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run("未知选项退出二", func(t *testing.T) {
		result := runMake(t, t.TempDir(), "all:\n\techo x\n", "--nope all", nil, nil)
		if result.Code != 2 {
			t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
		}
	})
	t.Run(".POSIX 裸声明被接受", func(t *testing.T) {
		result := runMake(t, t.TempDir(), ".POSIX:\nall:\n\techo ok\n", "-n all", nil, nil)
		if result.Code != 0 || result.Stdout != "echo ok\n" {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
		}
	})
}

func TestSuffixHelpers(t *testing.T) {
	for _, test := range []struct {
		name   string
		double bool
		single bool
	}{{" .x", false, false}, {".c", false, true}, {".c.o", true, false}, {".xy.z", true, false},
		{"a.b", false, false}, {"/x/.c", false, false}, {"..o", false, false}, {"", false, false}} {
		if got := isDoubleSuffix(test.name); got != test.double {
			t.Fatalf("isDoubleSuffix(%q) = %v，期望 %v", test.name, got, test.double)
		}
		if got := isSingleSuffix(test.name); got != test.single {
			t.Fatalf("isSingleSuffix(%q) = %v，期望 %v", test.name, got, test.single)
		}
	}
	for _, test := range []struct {
		line string
		eq   int
		op   string
		ok   bool
	}{
		{"A = b", 2, "=", true},
		{"A:=b", 2, ":=", true},
		{"A?=b", 2, "?=", true},
		{"A+=b", 2, "+=", true},
		{"a: b=c", 0, "", false},
		{"A", 0, "", false},
	} {
		eq, op, ok := findAssignment(test.line)
		if eq != test.eq || op != test.op || ok != test.ok {
			t.Fatalf("findAssignment(%q) = (%d, %q, %v)，期望 (%d, %q, %v)", test.line, eq, op, ok, test.eq, test.op, test.ok)
		}
	}
	if !oddTrailingSlash("a\\") || oddTrailingSlash("a\\\\") {
		t.Fatal("oddTrailingSlash 的奇偶判定不符")
	}
	if stripComment("a # b") != "a " || stripComment("a \\# b") != "a \\# b" {
		t.Fatal("stripComment 的转义处理不符")
	}
}

func TestRecipeLineContinuation(t *testing.T) {
	result := runMake(t, t.TempDir(), "all:\n\techo one \\\n\techo two\n\n\t@echo three\n", "-n all", nil, nil)
	if result.Code != 0 || result.Stdout != "echo one \\\necho two\necho three\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", result.Code, result.Stdout, result.Stderr)
	}
}

func TestCanceledUpdate(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr strings.Builder
	e := &engine{
		ctx: ctx,
		h:   interp.HandlerContext{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Dir: dir},
		m:   newMakefile(false), dir: dir, shell: "/bin/bash", env: map[string]string{},
		system:   filesystem.NewFilesystem(dir),
		o:        options{},
		visiting: map[string]bool{}, done: map[string]bool{}, made: map[string]bool{},
	}
	if _, err := e.update("a"); !errors.Is(err, errFailure) {
		t.Fatalf("取消应报 errFailure，得到 %v", err)
	}
	if !strings.Contains(stderr.String(), "已取消") {
		t.Fatalf("stderr 缺取消诊断: %q", stderr.String())
	}
}

func TestControlStubContract(t *testing.T) {
	stub := &controlStub{err: errors.New("boom")}
	if _, err := stub.Run(context.Background(), proc.RunSpec{}); err == nil {
		t.Fatal("应返回注入的错误")
	}
	if _, err := stub.Begin(1, "x", nil, "", nil, fd.Streams{}); err == nil {
		t.Fatal("Begin 应不支持")
	}
	if err := stub.Finish(1, 2, 0); err != nil {
		t.Fatalf("Finish 应成功: %v", err)
	}
}
