package shell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/bash"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

func Run(ctx context.Context, kernel *vm.Kernel, parent proc.Proc, source, dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer) error {
	session, err := newSessionWithKernel(kernel, parent, dir, environment, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	defer session.close()
	tree, err := parse(source)
	if err != nil {
		return err
	}
	return session.runner.Run(proc.WithContext(ctx, session.parent), tree)
}

type session struct {
	runner *interp.Runner
	kernel *vm.Kernel
	parent proc.Proc

	control *sessionControl

	sink *mutableSink
}

const extensionProgram = "@wasi"

func (s *session) close() {
	if err := s.kernel.Finish(s.parent.Parent, s.parent.ID, vm.ExitStatus{}); err == nil {
		// H10/K10:Finish 不再自动回收,会话关闭时由 init 收回外壳。
		_, _, _ = s.kernel.WaitChild(context.Background(), s.parent.Parent, s.parent.ID, true)
	}
}

// finishReaped 终态化子进程并回收僵尸(H10/K10:Finish 不再自动回收)。
func finishReaped(kernel *vm.Kernel, child proc.Proc, status vm.ExitStatus) error {
	if err := kernel.Finish(child.Parent, child.ID, status); err != nil {
		return err
	}
	_, _, err := kernel.WaitChild(context.Background(), child.Parent, child.ID, true)
	return err
}

func Programs(factory bash.RunnerFactory) (executable.Registry, error) {
	handlers := commands.Registry(factory)
	entries := map[string]executable.Program{}
	for name, handler := range handlers {
		entries[name] = executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
			err := commandio.Execute(ctx, p.Program, handler, p.Args)
			if status, ok := errors.AsType[interp.ExitStatus](err); ok {
				return uint8(status), nil
			}
			return 0, err
		})
	}
	entries[extensionProgram] = executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {
		err := extension(ctx, p.Args)
		if status, ok := errors.AsType[interp.ExitStatus](err); ok {
			return uint8(status), nil
		}
		return 0, err
	})
	return executable.New(entries)
}

func newSessionWithKernel(kernel *vm.Kernel, parent proc.Proc, dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer) (*session, error) {
	control := &sessionControl{kernel: kernel}
	runner, err := newRunner(kernel, parent, dir, environment, stdin, stdout, stderr, control)
	if err != nil {
		return nil, err
	}
	current := &session{runner: runner, kernel: kernel, parent: parent, control: control}

	if sink, ok := stdout.(*mutableSink); ok {
		current.sink = sink
	}
	return current, nil
}

// sessionControl 跟踪前台命令进程(P05):Interrupt 只向当前前台子进程
// 投递 SIGINT,不取消 Shell 会话本身。
type sessionControl struct {
	mu         sync.Mutex
	kernel     *vm.Kernel
	foreground proc.PID
}

func (c *sessionControl) set(pid proc.PID) {
	c.mu.Lock()
	c.foreground = pid
	c.mu.Unlock()
}

func (c *sessionControl) clear(pid proc.PID) {
	c.mu.Lock()
	if c.foreground == pid {
		c.foreground = 0
	}
	c.mu.Unlock()
}

func (c *sessionControl) interrupt(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	pid := c.foreground
	kernel := c.kernel
	c.mu.Unlock()
	if pid == 0 || kernel == nil {
		return nil
	}
	return kernel.Signal(pid, signal.INT)
}

func NewRunner(kernel *vm.Kernel, parent proc.Proc, dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer) (*interp.Runner, error) {
	return newRunner(kernel, parent, dir, environment, stdin, stdout, stderr, nil)
}

func newRunner(kernel *vm.Kernel, parent proc.Proc, dir string, environment []string, stdin io.Reader, stdout, stderr io.Writer, control *sessionControl) (*interp.Runner, error) {
	environment = append([]string{"HOME=" + dir, "TMPDIR=" + dir}, environment...)
	credentials, err := parent.Resources.Credentials()
	if err != nil {
		return nil, err
	}
	user, err := credentials.Lookup(context.Background(), uint64(parent.ID))
	if err != nil {
		return nil, err
	}
	environment = append(environment, "UID="+strconv.FormatUint(uint64(user.UID), 10), "EUID="+strconv.FormatUint(uint64(user.UID), 10), "GID="+strconv.FormatUint(uint64(user.GID), 10))
	fallback := func(ctx context.Context) context.Context {
		if _, err := proc.FromContext(ctx); err != nil {
			return proc.WithContext(ctx, parent)
		}
		return ctx
	}
	fileSystem := func(ctx context.Context) (vmfs.System, error) {
		p, err := proc.FromContext(fallback(ctx))
		if err != nil {
			return nil, err
		}
		return p.Resources.Filesystem()
	}
	state := newShellState()
	runner, err := interp.New(func(r *interp.Runner) error { r.Dir = dir; return nil }, interp.Env(expand.ListEnviron(environment...)), interp.StdIO(stdin, stdout, stderr),
		interp.OpenHandler(func(ctx context.Context, name string, flags int, mode fs.FileMode) (io.ReadWriteCloser, error) {
			system, err := fileSystem(ctx)
			if err != nil {
				return nil, err
			}
			h := interp.HandlerCtx(ctx)
			return system.Open(ctx, commandio.Path(h.Dir, name), uint32(flags), mode)
		}),
		interp.StatHandler(func(ctx context.Context, name string, _ bool) (fs.FileInfo, error) {
			system, err := fileSystem(ctx)
			if err != nil {
				return nil, err
			}
			return system.Stat(ctx, name)
		}),
		interp.ReadDirHandler2(func(ctx context.Context, name string) ([]fs.DirEntry, error) {
			system, err := fileSystem(ctx)
			if err != nil {
				return nil, err
			}
			return system.ReadDir(ctx, name)
		}),
		// 访问检查按客体文件系统的权限位判定:解释器的默认实现查
		// 宿主文件系统,会让客体的 cd 等操作按宿主路径误判(目录缺少
		// 宿主可执行位时报"permission denied")。
		interp.AccessHandler(func(ctx context.Context, name string, mode interp.AccessMode) error {
			system, err := fileSystem(ctx)
			if err != nil {
				return err
			}
			info, err := system.Stat(ctx, name)
			if err != nil {
				return err
			}
			need := fs.FileMode(mode) & 0o7
			if need == 0 {
				return nil
			}
			// 客体为单身份模型:属主、属组与其他三类权限位任一满足
			// 即视为允许(身份细分随 O01 落地)。
			perm := info.Mode().Perm()
			// 权限位布局:属主在高位(0o700)、属组居中(0o070)、
			// 其他在低位(0o007);任一类权限位满足全部需要即放行。
			for _, shift := range []uint{6, 3, 0} {
				granted := (perm >> shift) & 0o7
				if need&^granted == 0 {
					return nil
				}
			}
			return fs.ErrPermission
		}),
		interp.CallHandler(func(ctx context.Context, args []string) ([]string, error) {
			if len(args) == 0 {
				return args, nil
			}

			_ = kernel.RunDueJobs(ctx)

			if args[0] != "fc" && args[0] != "hash" {
				state.record(args)
			}
			target := args
			for len(target) > 1 && (target[0] == "command" || target[0] == "builtin") && !strings.HasPrefix(target[1], "-") {
				target = target[1:]
			}
			switch target[0] {
			case "eval":
				if _, err := parse(strings.Join(target[1:], " ")); err != nil {
					return nil, err
				}
			case ".", "source":
				if len(target) > 1 {
					system, err := fileSystem(ctx)
					if err != nil {
						return nil, err
					}
					file, err := system.Open(ctx, commandio.Path(interp.HandlerCtx(ctx).Dir, target[1]), vmfs.ReadOnly, 0)
					if err != nil {
						return nil, err
					}
					source, readErr := commandio.ReadText(ctx, file)
					closeErr := file.Close()
					if readErr != nil {
						return nil, readErr
					}
					if closeErr != nil {
						return nil, closeErr
					}
					if _, err := parse(source); err != nil {
						return nil, err
					}
				}
			case "pwd":
				for _, arg := range target[1:] {
					if arg == "-P" {
						return nil, fmt.Errorf("VM 尚未提供物理路径解析")
					}
				}
			case "read":
				for _, arg := range target[1:] {
					if strings.HasPrefix(arg, "-t") {
						return nil, fmt.Errorf("VM 尚未提供 read 超时能力")
					}
				}
			case "kill":

				return append([]string{"/bin/" + target[0]}, target[1:]...), nil
			case "hash", "fc":

				if target[0] == "hash" {
					if err := builtinHash(ctx, kernel, state, target[1:]); err != nil {
						if _, isStatus := errors.AsType[interp.ExitStatus](err); !isStatus {
							_, _ = fmt.Fprintln(interp.HandlerCtx(ctx).Stderr, err)
						}
						return []string{"false"}, nil
					}
					return []string{"true"}, nil
				}
				result, err := builtinFC(ctx, state, target[1:])
				if err != nil {
					if _, isStatus := errors.AsType[interp.ExitStatus](err); !isStatus {
						_, _ = fmt.Fprintln(interp.HandlerCtx(ctx).Stderr, err)
					}
					return []string{"false"}, nil
				}
				if len(result) > 0 {

					return result, nil
				}
				return []string{"true"}, nil
			case "jobs", "bg", "fg", "newgrp":

				return append([]string{"/bin/" + target[0]}, target[1:]...), nil
			case "umask":

				if err := builtinUmask(ctx, target[1:]); err != nil {
					_, _ = fmt.Fprintln(interp.HandlerCtx(ctx).Stderr, err)
					return []string{"false"}, nil
				}
				return []string{"true"}, nil
			}
			return args, nil
		}),
		interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				ctx = fallback(ctx)
				p, err := proc.FromContext(ctx)
				if err != nil {
					return err
				}
				h := interp.HandlerCtx(ctx)
				if args[0] == extensionProgram {
					_, _ = fmt.Fprintf(h.Stderr, "未注册沙箱命令: %s\n", args[0])
					return interp.ExitStatus(127)
				}

				_ = kernel.RunDueJobs(ctx)
				child, err := kernel.Begin(p.ID, args[0], args[1:], h.Dir, envMap(h.Env), fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr})
				if err != nil {
					if !errors.Is(err, errno.NotFound) {
						return err
					}
					child, err = kernel.Begin(p.ID, extensionProgram, args, h.Dir, envMap(h.Env), fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr})
					if err != nil {
						return err
					}
				}
				runCtx := proc.WithContext(ctx, child)
				if control != nil {
					control.set(child.ID)
					defer control.clear(child.ID)
				}

				status, runErr := kernel.RunRegistered(runCtx, child)
				finishErr := finishReaped(kernel, child, status)
				if runErr != nil {
					joined := errors.Join(runErr, finishErr)
					if ctx.Err() != nil {
						return joined
					}
					// 未注册命令经 @wasi 扩展兜底失败时按 Bash 报 127,
					// 不把解释器整体置为退出(P14/P15)。
					if !kernel.HasProgram(args[0]) {
						_, _ = fmt.Fprintf(h.Stderr, "未注册沙箱命令: %s\n", args[0])
						return interp.ExitStatus(127)
					}
					return joined
				}
				if finishErr != nil {
					return finishErr
				}
				if status.Code != 0 {
					return interp.ExitStatus(status.Code)
				}
				return nil
			}
		}))
	if err != nil {
		finishErr := kernel.Finish(parent.Parent, parent.ID, vm.ExitStatus{Code: 125})
		return nil, errors.Join(err, finishErr)
	}
	return runner, nil
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
func parse(source string) (*syntax.File, error) {
	tree, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "Bash")
	if err != nil {
		return nil, err
	}
	syntax.Walk(tree, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.ProcSubst:
			err = errors.New("VM 不支持 IPC 进程替换")
		case *syntax.TimeClause:
			err = errors.New("VM 尚未提供语言计时接口")
		}
		return err == nil
	})
	return tree, err
}
