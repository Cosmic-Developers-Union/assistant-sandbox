package main

import (
	"context"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

var shellBuiltins = []string{
	"cd", "echo", "eval", "exec", "exit", "export", "false", "history",
	"pwd", "read", "set", "shift", "test", "times", "trap", "true",
	"type", "ulimit", "umask", "unset", "wait",
}

type shellCompleter struct {
	ctx     context.Context
	machine *instance.VM
	fs      vmfs.System
	pid     proc.PID
}

func newShellCompleter(ctx context.Context, machine *instance.VM, pid proc.PID) *shellCompleter {
	return &shellCompleter{ctx: ctx, machine: machine, fs: machine.Filesystem(), pid: pid}
}

func (c *shellCompleter) Commands(prefix string) []string {
	candidates := make([]string, 0, len(shellBuiltins)+64)
	for _, name := range shellBuiltins {
		if strings.HasPrefix(name, prefix) {
			candidates = append(candidates, name)
		}
	}
	for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		entries, err := c.fs.ReadDir(c.ctx, dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
				continue
			}
			candidates = append(candidates, entry.Name())
		}
	}
	return candidates
}

func (c *shellCompleter) Prompt() string {
	dir := "/work"
	for _, process := range c.machine.Kernel().Processes() {
		if process.ID == c.pid {
			dir = process.Dir
			break
		}
	}
	return "sandbox:" + dir + "# "
}
