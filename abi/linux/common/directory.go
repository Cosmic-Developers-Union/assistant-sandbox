package common

import (
	"context"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// DirectoryState 是每进程可变工作目录:chdir 更新后,同一进程的
// 后续路径解析与 getcwd 立即生效,但不影响其他进程。
type DirectoryState struct {
	mu  sync.RWMutex
	dir string
}

func NewDirectoryState(dir string) *DirectoryState {
	if dir == "" {
		dir = "/"
	}
	return &DirectoryState{dir: dir}
}

func (d *DirectoryState) Get() string {
	if d == nil {
		return "/"
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.dir == "" {
		return "/"
	}
	return d.dir
}

func (d *DirectoryState) Set(dir string) {
	if d == nil || dir == "" {
		return
	}
	d.mu.Lock()
	d.dir = dir
	d.mu.Unlock()
}

// DirectoryControl 是内核进程工作目录端口(chdir/fchdir)。
type DirectoryControl interface {
	Chdir(ctx context.Context, path string) error
}

// Resolve 返回按当前工作目录解析后的客体绝对路径。
func (p ProcessFiles) Resolve(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.resolver().Resolve(path)
}

// chdirPath 校验目录并更新本地与内核工作目录。
func (s Syscalls) chdirPath(ctx context.Context, path string) Reply {
	resolver, ok := s.Files.(interface {
		Resolve(context.Context, string) (string, error)
	})
	resolved := path
	if ok {
		target, err := resolver.Resolve(ctx, path)
		if err != nil {
			return Reply{Errno: errnoOf(err)}
		}
		resolved = target
	}
	info, err := s.Files.Stat(ctx, resolved)
	if err != nil {
		return Reply{Errno: errnoOf(err)}
	}
	if !info.IsDir() {
		return Reply{Errno: errno.NotDirectory}
	}
	if s.Directories != nil {
		if err := s.Directories.Chdir(ctx, resolved); err != nil {
			return Reply{Errno: errnoOf(err)}
		}
	}
	if s.Directory != nil {
		s.Directory.Set(resolved)
	}
	return Reply{}
}

func (s Syscalls) chdir(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Memory == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path, code := s.readPath(ctx, call.Args[0])
	if code != "" {
		return Reply{Errno: code}
	}
	return s.chdirPath(ctx, path)
}

func (s Syscalls) fchdir(ctx context.Context, call Call) Reply {
	if s.Files == nil || s.Descriptors == nil {
		return Reply{Errno: errno.NotImplemented}
	}
	path := s.descriptorPath(ctx, call.Args[0])
	if path == "" {
		return Reply{Errno: errno.NotSupported}
	}
	return s.chdirPath(ctx, path)
}
