package legacy

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/experimental/sysfs"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

var compilationCache = wazero.NewCompilationCache()

type Config struct {
	ShellArchive []byte

	MessageSource MessageSource

	CompilationCacheDir                                   string
	MemoryMB, Concurrency, MaxSessions                    int
	Timeout                                               time.Duration
	MaxFileBytes, WriteBytes, OutputBytes, FileOperations int64
	ReadOnly                                              bool

	Modules map[string][]byte
}

type Runtime struct {
	cfg        Config
	slots      chan struct{}
	runtime    wazero.Runtime
	shell      wazero.CompiledModule
	modules    map[string]wazero.CompiledModule
	close      func() error
	mu         sync.Mutex
	closed     bool
	base       context.Context
	cancelBase context.CancelFunc
	sessions   map[*Session]bool
	defaults   map[string]*Session
}

func New(ctx context.Context, cfg Config) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(cfg.ShellArchive) == 0 {
		return nil, fmt.Errorf("需要通过 Config.ShellArchive 提供 WASI gzip 客体")
	}
	return newRuntime(ctx, cfg, cfg.ShellArchive)
}

func newRuntime(ctx context.Context, cfg Config, archive []byte) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.FileOperations < 0 || cfg.MaxSessions < 0 || cfg.MaxSessions > 128 || cfg.OutputBytes > 1<<20 || cfg.MemoryMB < 0 || cfg.MemoryMB > 4096 || cfg.Concurrency < 0 || cfg.Concurrency > 128 || cfg.Timeout < 0 || cfg.MaxFileBytes < 0 || cfg.WriteBytes < 0 || cfg.OutputBytes < 0 {
		return nil, fmt.Errorf("沙箱资源预算无效")
	}
	cfg.FileOperations = cmp.Or(cfg.FileOperations, 4096)
	cfg.MaxSessions = cmp.Or(cfg.MaxSessions, 16)
	cfg.MemoryMB = cmp.Or(cfg.MemoryMB, 128)
	cfg.Concurrency = cmp.Or(cfg.Concurrency, 4)
	cfg.Timeout = cmp.Or(cfg.Timeout, 2*time.Minute)
	cfg.MaxFileBytes = cmp.Or(cfg.MaxFileBytes, 8<<20)
	cfg.WriteBytes = cmp.Or(cfg.WriteBytes, 64<<20)
	cfg.OutputBytes = cmp.Or(cfg.OutputBytes, 1<<20)
	if cfg.CompilationCacheDir != "" {
		var err error
		cfg.CompilationCacheDir, err = filepath.Abs(cfg.CompilationCacheDir)
		if err != nil {
			return nil, fmt.Errorf("定位编译缓存目录: %w", err)
		}
	}
	base, cancelBase := context.WithCancel(ctx)
	r := &Runtime{cfg: cfg, slots: make(chan struct{}, cfg.Concurrency), modules: map[string]wazero.CompiledModule{}, base: base, cancelBase: cancelBase, sessions: map[*Session]bool{}, defaults: map[string]*Session{}}
	cache := compilationCache
	closeCache := func() error { return nil }
	if cfg.CompilationCacheDir != "" {
		var err error
		cache, err = openCompilationCache(cfg.CompilationCacheDir)
		if err != nil {
			cancelBase()
			return nil, err
		}
		closeCache = func() error { return cache.Close(context.Background()) }
	} else if len(cfg.Modules) > 0 {
		cache = wazero.NewCompilationCache()
		closeCache = func() error { return cache.Close(context.Background()) }
	}
	r.runtime = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(cache).WithMemoryLimitPages(uint32(cfg.MemoryMB)*16).WithCloseOnContextDone(true))
	r.close = sync.OnceValue(func() error {
		r.cancelBase()
		r.mu.Lock()
		r.closed = true
		sessions := make([]*Session, 0, len(r.sessions))
		for s := range r.sessions {
			sessions = append(sessions, s)
		}
		r.mu.Unlock()
		var sessionErr error
		for _, s := range sessions {
			sessionErr = errors.Join(sessionErr, s.Close())
		}
		return errors.Join(sessionErr, r.runtime.Close(context.Background()), closeCache())
	})
	failed := func(err error) (*Runtime, error) { return nil, errors.Join(err, r.Close()) }
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r.runtime); err != nil {
		return failed(fmt.Errorf("初始化 WASI: %w", err))
	}
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return failed(err)
	}
	code, err := io.ReadAll(reader)
	err = errors.Join(err, reader.Close())
	if err != nil {
		return failed(err)
	}
	r.shell, err = r.runtime.CompileModule(ctx, code)
	if err != nil {
		return failed(fmt.Errorf("编译 Bash 客体: %w", err))
	}
	if _, err := r.runtime.NewHostModuleBuilder("assistant").NewFunctionBuilder().WithFunc(r.callModule).Export("exec").Instantiate(ctx); err != nil {
		return failed(fmt.Errorf("初始化命令注册表: %w", err))
	}
	if len(cfg.Modules) > 32 {
		return failed(fmt.Errorf("WASI 扩展模块不能超过 32 个"))
	}
	for name, code := range cfg.Modules {
		if !validCommand(name) || name == "assistant-bash" || len(code) > 64<<20 {
			return failed(fmt.Errorf("WASI 模块登记名无效"))
		}
		module, err := r.runtime.CompileModule(ctx, code)
		if err != nil {
			return failed(fmt.Errorf("编译 WASI 模块 %s: %w", name, err))
		}
		for _, function := range module.ImportedFunctions() {
			owner, _, _ := function.Import()
			if owner != "wasi_snapshot_preview1" {
				return failed(errors.Join(fmt.Errorf("扩展模块 %s 只能导入 WASI 能力", name), module.Close(context.Background())))
			}
		}
		if len(module.ImportedMemories()) > 0 {
			return failed(errors.Join(fmt.Errorf("扩展模块不能导入外部内存"), module.Close(context.Background())))
		}
		r.modules[name] = module
	}
	return r, nil
}
func validCommand(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (r *Runtime) Close() error { return r.close() }

// State reports whether the runtime has been closed.
func (r *Runtime) Closed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// CloseWithDeadline 在截止时间前协作关闭；超时后取消基础上下文并返回超时错误。
func (r *Runtime) CloseWithDeadline(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- r.close() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		r.cancelBase()
		return ctx.Err()
	}
}

func (r *Runtime) Execute(ctx context.Context, dir, command string, stdout, stderr io.Writer) (Result, error) {
	full, err := filepath.Abs(dir)
	if err != nil {
		return Result{}, err
	}
	r.mu.Lock()
	session := r.defaults[full]
	r.mu.Unlock()
	if session == nil {
		candidate, err := r.Bind(full)
		if err != nil {
			return Result{}, err
		}
		r.mu.Lock()
		session = r.defaults[full]
		if session == nil {
			session = candidate
			r.defaults[full] = session
		}
		r.mu.Unlock()
		if session != candidate {
			if err := candidate.Close(); err != nil {
				return Result{}, err
			}
		}
	}
	return session.Execute(ctx, full, command, stdout, stderr)
}

func (r *Runtime) execute(ctx context.Context, dir string, module wazero.CompiledModule, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer, cancel context.CancelFunc) (result Result, resultErr error) {
	work, err := OpenFilesystem(dir, FilesystemConfig{ReadOnly: r.cfg.ReadOnly, MaxFileBytes: r.cfg.MaxFileBytes, WriteBytes: r.cfg.WriteBytes, Operations: r.cfg.FileOperations})
	if err != nil {
		return Result{}, fmt.Errorf("打开沙箱工作区: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, work.Close())
	}()
	temp, err := os.MkdirTemp("", "assistant-wasi-*")
	if err != nil {
		return Result{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, os.RemoveAll(temp))
	}()
	scratch, err := OpenFilesystem(temp, FilesystemConfig{MaxFileBytes: r.cfg.MaxFileBytes, WriteBytes: r.cfg.WriteBytes, Operations: r.cfg.FileOperations})
	if err != nil {
		return Result{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, scratch.Close())
	}()
	filesystem := wazero.NewFSConfig().(sysfs.FSConfig).WithSysFSMount(work, "/work")
	filesystem = filesystem.(sysfs.FSConfig).WithSysFSMount(scratch, "/tmp")
	filesystem = filesystem.(sysfs.FSConfig).WithSysFSMount(devices{}, "/dev")
	budget := &outputBudget{remaining: r.cfg.OutputBytes, cancel: cancel}
	if wire, ok := ctx.Value(loopBudgetKey{}).(*outputBudget); ok {
		budget = wire
	}
	config := wazero.NewModuleConfig().WithName("").WithArgs(args...).WithFSConfig(filesystem).WithStdin(stdin).WithStdout(&boundedOutput{out: stdout, budget: budget}).WithStderr(&boundedOutput{out: stderr, budget: budget}).WithEnv("PWD", "/work").WithEnv("HOME", "/tmp").WithEnv("TMPDIR", "/tmp").WithEnv("PATH", "/commands").WithEnv("LANG", "C.UTF-8")
	for _, value := range env {
		name, value, _ := strings.Cut(value, "=")
		config = config.WithEnv(name, value)
	}
	ctx = context.WithValue(ctx, executionKey{}, &executionState{filesystem: filesystem, budget: budget})
	instance, err := r.runtime.InstantiateModule(ctx, module, config)
	if instance != nil {
		defer func() {
			resultErr = errors.Join(resultErr, instance.Close(context.Background()))
		}()
	}
	if budget.exceeded {
		return Result{}, fmt.Errorf("沙箱输出超过 %d 字节预算", r.cfg.OutputBytes)
	}
	return moduleResult(ctx, err)
}
func moduleResult(ctx context.Context, err error) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err == nil {
		return Result{}, nil
	}
	if exit, ok := errors.AsType[*sys.ExitError](err); ok {
		return Result{ExitCode: int(exit.ExitCode())}, nil
	}
	return Result{}, fmt.Errorf("执行 WASI 客体: %w", err)
}

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
	exceeded  bool
	cancel    context.CancelFunc
}
type boundedOutput struct {
	out    io.Writer
	budget *outputBudget
}

func (w *boundedOutput) Write(data []byte) (int, error) {
	w.budget.mu.Lock()
	defer w.budget.mu.Unlock()
	if int64(len(data)) > w.budget.remaining {
		w.budget.exceeded = true
		w.budget.cancel()
		return 0, fmt.Errorf("沙箱输出预算耗尽")
	}
	w.budget.remaining -= int64(len(data))
	return w.out.Write(data)
}
