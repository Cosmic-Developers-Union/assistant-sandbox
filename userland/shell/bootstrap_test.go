package shell

import (
	"context"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/modules"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func newKernel(dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer) (*vm.Kernel, proc.Proc, error) {
	var kernel *vm.Kernel
	var parent proc.Proc
	factory := func(dir string, env []string, in io.Reader, out, diagnostic io.Writer) (*interp.Runner, error) {
		return NewRunner(kernel, parent, dir, env, in, out, diagnostic)
	}
	registry, err := Programs(factory)
	if err != nil {
		return nil, parent, err
	}
	storage := filesystem.NewWorkspace(dir)
	info, err := storage.Stat(context.Background(), dir)
	if err != nil {
		return nil, parent, err
	}
	if !info.IsDir() {
		return nil, parent, fmt.Errorf("Bash 工作区不是目录")
	}
	kernel, err = vm.New(vm.Services{Set: capability.Set{FS: storage, FD: fd.New(), Identity: identity.SingleUser{}, Clock: &clock.Virtual{}, Entropy: &entropy.Counter{}, Bridge: modules.Modules{}}, Programs: registry}, 128)
	if err != nil {
		return nil, parent, err
	}
	parent, err = kernel.Begin(0, "bash", nil, dir, envMap(expand.ListEnviron(environment...)), fd.Streams{Input: stdin, Output: stdout, Error: stderr})
	return kernel, parent, err
}

func newSession(dir string, env []string, in io.Reader, out, diagnostic io.Writer) (*session, error) {
	k, p, err := newKernel(dir, env, in, out, diagnostic)
	if err != nil {
		return nil, err
	}
	return newSessionWithKernel(k, p, dir, env, in, out, diagnostic)
}
func run(ctx context.Context, source, dir string, env []string, in io.Reader, out, diagnostic io.Writer) error {
	k, p, err := newKernel(dir, env, in, out, diagnostic)
	if err != nil {
		return err
	}
	return Run(ctx, k, p, source, dir, env, in, out, diagnostic)
}
func loop(ctx context.Context, in io.Reader, out io.Writer, dir string, env []string) error {
	k, p, err := newKernel(dir, env, nil, nil, nil)
	if err != nil {
		return err
	}
	return Loop(ctx, k, p, in, out, dir, env)
}
