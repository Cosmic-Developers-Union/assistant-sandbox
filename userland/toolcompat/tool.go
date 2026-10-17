package toolcompat

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type Stdio struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

type RunContext struct {
	Ctx context.Context
	Dir string

	Env []string
	Stdio

	InvocationName string

	SIGPIPEIgnored bool
}

func IsClosedPipeError(err error) bool {
	return errors.Is(err, errno.BrokenPipe) || errors.Is(err, io.ErrClosedPipe)
}

func (rc *RunContext) Path(name string) string {
	if path.IsAbs(name) {
		return name
	}
	return path.Join(rc.Dir, name)
}

func (rc *RunContext) Getenv(name string) string {
	prefix := name + "="
	for _, entry := range rc.Env {
		if strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):]
		}
	}
	return ""
}

type Tool struct {
	Name     string
	Synopsis string
	Usage    string
	Run      func(rc *RunContext, args []string) int
}

var (
	mu       sync.Mutex
	registry = map[string]*Tool{}
)

func Register(t *Tool) {
	mu.Lock()
	defer mu.Unlock()
	if t == nil || t.Name == "" {
		panic("toolcompat: 命令名不能为空")
	}
	if _, exists := registry[t.Name]; exists {
		panic("toolcompat: 命令重复登记: " + t.Name)
	}
	registry[t.Name] = t
}

func Lookup(name string) (*Tool, bool) {
	mu.Lock()
	defer mu.Unlock()
	tool, ok := registry[name]
	return tool, ok
}

func SysErr(err error) error { return err }

func SysErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
