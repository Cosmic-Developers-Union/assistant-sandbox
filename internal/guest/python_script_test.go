package guest_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

// TestPython312ScriptFile 回归验证脚本主程序:客体 CPython 必须以
// __main__.__file__ 运行文件,且普通文件不能被 isatty 误判为终端;
// 否则解释器会进入交互模式并把脚本当作 REPL 输入。
func TestPython312ScriptFile(t *testing.T) {
	machine, connection := python312Guest(t)
	script := `import os, sys
handle = open("/work/q10_script_probe.py", "rb")
print("file", getattr(sys.modules["__main__"], "__file__", "MISSING"))
print("argv", sys.argv)
print("tty", os.isatty(0), os.isatty(handle.fileno()))
`
	writeGuestFile(t, machine, "/work/q10_script_probe.py", []byte(script), 0o644)
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: "/usr/bin/python3.12",
		Args:    []string{"-S", "/work/q10_script_probe.py"},
		Dir:     "/work",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "PYTHONHOME": "/usr",
		},
		Streams: fd.Streams{Output: output, Error: diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := output.String() + diagnostic.String()
	want := "file /work/q10_script_probe.py\nargv ['/work/q10_script_probe.py']\ntty False False\n"
	if state.ExitCode != 0 || got != want {
		t.Fatalf("脚本执行 exit=%d output=%q want %q", state.ExitCode, got, want)
	}
	if strings.Contains(got, ">>>") {
		t.Fatalf("脚本不应进入交互模式: %q", got)
	}
}
