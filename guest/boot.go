package guest

import (
	"context"
	"fmt"
	"io"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/modules"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

const (
	guestMemoryBudget  = 256 << 20
	guestWorkBudget    = 4 << 20
	guestProcessMemory = 64 << 20
	guestProcessWork   = 1 << 20
)

func New(dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer) (*vm.Kernel, proc.Proc, error) {
	return NewWithClock(dir, environment, stdin, stdout, stderr, nil)
}

func NewWithClock(dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer, source clock.Source) (*vm.Kernel, proc.Proc, error) {
	return NewWithClockFS(dir, environment, stdin, stdout, stderr, source, nil)
}

func NewWithClockFS(dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer, source clock.Source, storage vmfs.System) (*vm.Kernel, proc.Proc, error) {
	var kernel *vm.Kernel
	var parent proc.Proc
	factory := func(dir string, env []string, in io.Reader, out, diagnostic io.Writer) (*interp.Runner, error) {
		return shell.NewRunner(kernel, parent, dir, env, in, out, diagnostic)
	}
	registry, err := shell.Programs(factory)
	if err != nil {
		return nil, parent, err
	}
	if storage == nil {
		storage = filesystem.NewWorkspace(dir)
	}
	info, err := storage.Stat(context.Background(), dir)
	if err != nil {
		return nil, parent, err
	}
	if !info.IsDir() {
		return nil, parent, fmt.Errorf("Bash 工作区不是目录")
	}
	meters, err := resources.NewSession(resources.Limits{Memory: guestMemoryBudget, Work: guestWorkBudget},
		resources.Limits{Memory: guestProcessMemory, Work: guestProcessWork})
	if err != nil {
		return nil, parent, err
	}
	kernel, err = vm.New(vm.Services{
		Set: capability.Set{
			FS:       storage,
			FD:       fd.New(),
			Identity: identity.SingleUser{},
			Clock:    clockOrVirtual(source),
			Entropy:  &entropy.Counter{},
			Bridge:   modules.Modules{},

			Resources: meters,
		},
		Programs: registry,
		Meters:   meters,
		Binaries: elf.Factory{},
	}, 128)
	if err != nil {
		return nil, parent, err
	}
	parent, err = kernel.Begin(0, "bash", nil, dir, envMap(expand.ListEnviron(environment...)), fd.Streams{Input: stdin, Output: stdout, Error: stderr})
	return kernel, parent, err
}

func clockOrVirtual(source clock.Source) clock.Source {
	if source == nil {
		return &clock.Virtual{}
	}
	return source
}

func envMap(environment expand.Environ) map[string]string {
	result := map[string]string{}
	environment.Each(func(name string, v expand.Variable) bool {
		if v.IsSet() && v.Exported {
			result[name] = v.String()
		}
		return true
	})
	return result
}
