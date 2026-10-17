package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func TestDynamicScriptsAndUnavailableShellCapabilities(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good"), []byte("basename /work/value"), 0600); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad"), []byte("cat <(printf value)"), 0600); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "too-big"), []byte(strings.Repeat("x", 4<<20+1)), 0600); err != nil {
		t.Error(err)
	}
	for _, tc := range []struct {
		script  string
		success bool
	}{
		{`eval 'basename /work/value'`, true},
		{`eval 'if'`, false}, {`eval 'cat <(printf value)'`, false},
		{`source good`, true}, {`source bad`, false}, {`source missing`, false}, {`source too-big`, false},
		{`pwd -P`, false}, {`read -t 1 value`, false},
		{`cat <(printf value)`, false}, {`time true`, false}, {`echo value &`, false},
	} {
		t.Run(tc.script, func(t *testing.T) {
			var out bytes.Buffer
			err := run(t.Context(), tc.script, dir, nil, strings.NewReader(""), &out, &out)
			if (err == nil) != tc.success {
				t.Fatal(tc.script, err, out.String())
			}
		})
	}
	if _, err := newSession(filepath.Join(dir, "good"), nil, nil, new(bytes.Buffer), new(bytes.Buffer)); err == nil {
		t.Fatal("以文件充当工作目录")
	}
}

func TestRunnerCannotBypassProcessBudgetOrMissingCapabilities(t *testing.T) {
	for _, script := range []string{"probe", "unknown", "printf value > file", "[[ -f file ]]", "printf '%s' *", "source file"} {
		for _, limit := range []int{2, 3} {
			reg, _ := executable.New(map[string]executable.Program{"bash": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, nil }), "probe": executable.Func(func(context.Context, proc.Proc) (uint8, error) { return 0, errno.NotImplemented })})
			kernel, _ := vm.New(vm.Services{Set: capability.Set{FD: fd.New(), Identity: identity.SingleUser{}}, Programs: reg}, limit)
			parent, _ := kernel.Begin(0, "bash", nil, "/", nil, fd.Streams{Output: io.Discard, Error: io.Discard})
			runner, err := NewRunner(kernel, parent, "/", nil, strings.NewReader(""), io.Discard, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			tree, _ := parse(script)

			_ = runner.Run(t.Context(), tree)
			if len(kernel.Processes()) != 2 {
				t.Fatal(script, kernel.Processes())
			}
		}
	}
}

type failingBridge struct{ err error }

func (f failingBridge) Call(context.Context, uint64, string, []byte) ([]byte, error) {
	return nil, f.err
}
func TestExtensionKeepsCapabilityAndBridgeErrors(t *testing.T) {
	if err := exchangeExtension(t.Context(), nil, interp.HandlerContext{}, nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	sentinel := errors.New("桥接失败")
	for _, mode := range []string{"proc", "host", "bridge"} {
		runner, _ := interp.New(interp.Dir(t.TempDir()), interp.StdIO(strings.NewReader(""), io.Discard, io.Discard), interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				table := fd.New()
				h := interp.HandlerCtx(ctx)
				table.Attach(2, fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr})
				p := proc.Proc{ID: 2, Resources: capability.Set{FD: table, Bridge: failingBridge{sentinel}}}
				if mode == "proc" {
					p = proc.Proc{}
				}
				if mode == "host" {
					p.Resources = capability.Set{FD: table}
				}
				return extension(proc.WithContext(ctx, p), args)
			}
		}))
		tree, _ := syntax.NewParser().Parse(strings.NewReader("extension"), "")
		err := runner.Run(t.Context(), tree)
		if mode == "bridge" && !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		if mode != "bridge" && !errors.Is(err, errno.NotImplemented) {
			t.Fatal(mode, err)
		}
	}
}
