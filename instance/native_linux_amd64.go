//go:build linux && amd64

package instance

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/guest/hostroot"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// NativeExecSpec 描述原生后端中的一次客体执行。文件句柄由调用方提供，
// 父进程只把这些显式句柄和一个内部控制管道传给隔离子进程。
type NativeExecSpec struct {
	Program string
	Args    []string
	Dir     string
	Env     map[string]string
	Stdin   *os.File
	Stdout  *os.File
	Stderr  *os.File
}

// NativeExit 是原生客体进程的退出状态。
type NativeExit struct {
	Code   int
	Signal syscall.Signal
}

// NativeVM 是 linux/amd64 原生后端的最小生命周期实现。
type NativeVM struct {
	root string

	mu        sync.Mutex
	closed    bool
	processes map[int]*NativeProcess
	mounts    map[string]string
}

type nativeBackendFactory struct{}

func (nativeBackendFactory) Backend() Backend { return BackendNativeAMD64 }

func (nativeBackendFactory) New(ctx context.Context, opts Options) (BackendInstance, error) {
	return newNativeVM(ctx, opts)
}

func newNativeVM(ctx context.Context, opts Options) (*NativeVM, error) {
	if opts.Session.Work != 0 || opts.Process.Work != 0 {
		return nil, ErrNativeWorkBudgetUnsupported
	}
	root, err := os.MkdirTemp("", "assistant-native-")
	if err != nil {
		return nil, fmt.Errorf("create native root directory: %w", err)
	}
	if err := hostroot.Extract(ctx, hostroot.Archive(), root); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("extract native root filesystem: %w", err)
	}
	return &NativeVM{root: root, processes: map[int]*NativeProcess{}, mounts: map[string]string{}}, nil
}

// Root 返回该 VM 私有的宿主根目录；仅用于受控挂载与测试。
func (v *NativeVM) Root() string { return v.root }

// Mount 把宿主授权目录 bind mount 到客体绝对路径。宿主符号链接在
// 注册时解析；客体侧符号链接和 .. 不能越出该 bind mount。
func (v *NativeVM) Mount(host, guest string) error {
	absoluteHost, err := filepath.Abs(host)
	if err != nil {
		return fmt.Errorf("resolve native mount source: %w", err)
	}
	resolvedHost, err := filepath.EvalSymlinks(absoluteHost)
	if err != nil {
		return fmt.Errorf("resolve native mount source: %w", err)
	}
	info, err := os.Stat(resolvedHost)
	if err != nil {
		return fmt.Errorf("stat native mount source: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("native mount source is not a directory: %w", errno.Invalid)
	}
	cleanGuest := path.Clean(guest)
	if !path.IsAbs(cleanGuest) || cleanGuest == "/" {
		return fmt.Errorf("native mount target must be an absolute non-root path: %w", errno.Invalid)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return errno.Cancelled
	}
	if _, exists := v.mounts[cleanGuest]; exists {
		return fmt.Errorf("native mount target already registered: %w", errno.AddressInUse)
	}
	v.mounts[cleanGuest] = resolvedHost
	return nil
}

// Mounts 返回当前授权挂载的副本。
func (v *NativeVM) Mounts() map[string]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	mounts := make(map[string]string, len(v.mounts))
	for guest, host := range v.mounts {
		mounts[guest] = host
	}
	return mounts
}

// Exec 在隔离 namespace 中启动客体程序，并在 init 完成 pivot_root、
// capabilities 清除和 seccomp 安装后才返回。
func (v *NativeVM) Exec(ctx context.Context, spec NativeExecSpec) (*NativeProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Program == "" || !strings.HasPrefix(spec.Program, "/") {
		return nil, fmt.Errorf("native guest program must be absolute: %w", ErrNativeArchitecture)
	}
	if spec.Dir == "" {
		spec.Dir = "/"
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil, errno.Cancelled
	}
	v.mu.Unlock()

	stdin, closeStdin, err := nativeFileOrDefault(spec.Stdin, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	stdout, closeStdout, err := nativeFileOrDefault(spec.Stdout, os.O_WRONLY)
	if err != nil {
		if closeStdin {
			_ = stdin.Close()
		}
		return nil, err
	}
	stderr, closeStderr, err := nativeFileOrDefault(spec.Stderr, os.O_WRONLY)
	if err != nil {
		if closeStdin {
			_ = stdin.Close()
		}
		if closeStdout {
			_ = stdout.Close()
		}
		return nil, err
	}
	controlReader, controlWriter, err := os.Pipe()
	if err != nil {
		closeNativeFiles(stdin, closeStdin, stdout, closeStdout, stderr, closeStderr)
		return nil, fmt.Errorf("create native control pipe: %w", err)
	}
	helper := filepath.Join(v.root, "usr/bin/assistant-init")
	arguments := []string{
		helper, "--rootfs", v.root, "--control-fd", "3", "--chdir", spec.Dir,
	}
	mounts := v.Mounts()
	guestPaths := make([]string, 0, len(mounts))
	for guest := range mounts {
		guestPaths = append(guestPaths, guest)
	}
	slices.Sort(guestPaths)
	for _, guest := range guestPaths {
		arguments = append(arguments, "--mount", guest+"="+mounts[guest])
	}
	arguments = append(arguments, "--", spec.Program)
	arguments = append(arguments, spec.Args...)
	environment := map[string]string{}
	for name, value := range spec.Env {
		environment[name] = value
	}
	attributes := &os.ProcAttr{
		Dir:   "/",
		Env:   nativeEnvironment(environment),
		Files: []*os.File{stdin, stdout, stderr, controlWriter},
		Sys: &syscall.SysProcAttr{
			Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS |
				syscall.CLONE_NEWPID | syscall.CLONE_NEWNET | syscall.CLONE_NEWIPC |
				syscall.CLONE_NEWUTS | syscall.CLONE_NEWCGROUP,
			UidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getuid(), Size: 1},
			},
			GidMappings: []syscall.SysProcIDMap{
				{ContainerID: 0, HostID: os.Getgid(), Size: 1},
			},
			GidMappingsEnableSetgroups: false,
			Pdeathsig:                  syscall.SIGKILL,
			Setsid:                     true,
		},
	}
	process, err := os.StartProcess(helper, arguments, attributes)
	_ = controlWriter.Close()
	closeNativeFiles(stdin, closeStdin, stdout, closeStdout, stderr, closeStderr)
	if err != nil {
		_ = controlReader.Close()
		return nil, fmt.Errorf("start native guest: %w", err)
	}
	handle := &NativeProcess{
		process: process,
		done:    make(chan struct{}),
		vm:      v,
	}
	go handle.wait()
	if err := awaitNativeReady(ctx, controlReader, handle); err != nil {
		_ = handle.process.Kill()
		select {
		case <-handle.done:
		case <-time.After(5 * time.Second):
		}
		return nil, err
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		_ = handle.process.Kill()
		select {
		case <-handle.done:
		case <-time.After(5 * time.Second):
		}
		return nil, errno.Cancelled
	}
	v.processes[process.Pid] = handle
	v.mu.Unlock()
	return handle, nil
}

// Run 启动并等待一个短生命周期客体程序。
func (v *NativeVM) Run(ctx context.Context, spec NativeExecSpec) (NativeExit, error) {
	process, err := v.Exec(ctx, spec)
	if err != nil {
		return NativeExit{}, err
	}
	return process.Wait(ctx)
}

// NativeProcess 是原生客体进程句柄。
type NativeProcess struct {
	process *os.Process
	vm      *NativeVM
	done    chan struct{}
	result  NativeExit
	err     error
}

// ID 返回宿主可见的进程号；客体 PID namespace 内该进程通常为 PID 1。
func (p *NativeProcess) ID() int { return p.process.Pid }

func (p *NativeProcess) wait() {
	state, err := p.process.Wait()
	if err == nil {
		if status, ok := state.Sys().(syscall.WaitStatus); ok {
			if status.Exited() {
				p.result.Code = status.ExitStatus()
			} else if status.Signaled() {
				p.result.Signal = status.Signal()
				p.result.Code = 128 + int(status.Signal())
			}
		}
	}
	p.err = err
	close(p.done)
	if p.vm != nil {
		p.vm.mu.Lock()
		delete(p.vm.processes, p.process.Pid)
		p.vm.mu.Unlock()
	}
}

// Wait 等待进程结束；取消等待不会终止进程。
func (p *NativeProcess) Wait(ctx context.Context) (NativeExit, error) {
	select {
	case <-p.done:
		return p.result, p.err
	case <-ctx.Done():
		return NativeExit{}, ctx.Err()
	}
}

// Signal 向客体 init 投递宿主信号。
func (p *NativeProcess) Signal(signal syscall.Signal) error {
	return p.process.Signal(signal)
}

// Close 幂等关闭 VM：杀死全部活动进程并删除私有根目录。
func (v *NativeVM) Close(ctx context.Context) error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	processes := make([]*NativeProcess, 0, len(v.processes))
	for _, process := range v.processes {
		processes = append(processes, process)
	}
	v.processes = map[int]*NativeProcess{}
	v.mu.Unlock()
	for _, process := range processes {
		_ = process.process.Kill()
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for _, process := range processes {
		select {
		case <-process.done:
		case <-deadline.C:
			return errors.Join(ctx.Err(), os.RemoveAll(v.root))
		}
	}
	return errors.Join(ctx.Err(), os.RemoveAll(v.root))
}

func nativeFileOrDefault(file *os.File, mode int) (*os.File, bool, error) {
	if file != nil {
		return file, false, nil
	}
	opened, err := os.OpenFile(os.DevNull, mode, 0)
	if err != nil {
		return nil, false, fmt.Errorf("open native stdio fallback: %w", err)
	}
	return opened, true, nil
}

func closeNativeFiles(stdin *os.File, closeStdin bool, stdout *os.File, closeStdout bool, stderr *os.File, closeStderr bool) {
	if closeStdin {
		_ = stdin.Close()
	}
	if closeStdout {
		_ = stdout.Close()
	}
	if closeStderr {
		_ = stderr.Close()
	}
}

func nativeEnvironment(explicit map[string]string) []string {
	environment := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/root",
		"LANG=C.UTF-8",
	}
	for name, value := range explicit {
		environment = append(environment, name+"="+value)
	}
	slices.Sort(environment)
	return environment
}

func awaitNativeReady(ctx context.Context, control *os.File, process *NativeProcess) error {
	defer func() { _ = control.Close() }()
	type ready struct {
		line string
		err  error
	}
	result := make(chan ready, 1)
	go func() {
		line, err := bufio.NewReader(control).ReadString('\n')
		result <- ready{line: strings.TrimSpace(line), err: err}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case response := <-result:
		if response.err != nil {
			return fmt.Errorf("read native init status: %w", response.err)
		}
		if strings.HasPrefix(response.line, "READY ") {
			return nil
		}
		if strings.HasPrefix(response.line, "ERROR ") {
			return fmt.Errorf("native init failed: %s", strings.TrimPrefix(response.line, "ERROR "))
		}
		return fmt.Errorf("invalid native init response %q", response.line)
	}
}
