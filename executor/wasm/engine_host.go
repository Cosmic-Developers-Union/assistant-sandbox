//go:build !wasip1

package wasm

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

type HostEngine struct {
	runtime wazero.Runtime
	mu      sync.Mutex
	close   bool
}

func NewHostEngine(ctx context.Context) (*HostEngine, error) {
	return NewHostEngineWithMemoryLimit(ctx, 0)
}

func NewHostEngineWithMemoryLimit(ctx context.Context, limitPages uint32) (*HostEngine, error) {
	config := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	if limitPages > 0 {
		config = config.WithMemoryLimitPages(limitPages)
	}
	runtime := wazero.NewRuntimeWithConfig(ctx, config)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		_ = runtime.Close(ctx)
		return nil, errno.Wrap(err, "装配 WASI 预览")
	}
	return &HostEngine{runtime: runtime}, nil
}

func (e *HostEngine) Instantiate(ctx context.Context, module *Module, moduleBytes []byte, config Config) (Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if module == nil || len(moduleBytes) == 0 {
		return nil, errno.Wrap(errno.Invalid, "缺少模块")
	}
	e.mu.Lock()
	if e.close {
		e.mu.Unlock()
		return nil, errno.Wrap(errno.Cancelled, "引擎已关闭")
	}
	e.mu.Unlock()

	if config.Hosts != nil {
		if missing := config.Hosts.ValidateImports(module); len(missing) > 0 {
			return nil, errno.Wrap(errno.NotSupported,
				"模块声明了未登记的宿主函数: "+missing[0].Module+"."+missing[0].Name)
		}
	}

	boundModules, err := e.bindHostModules(ctx, config.Bindings)
	if err != nil {
		return nil, err
	}

	compiled, err := e.runtime.CompileModule(ctx, moduleBytes)
	if err != nil {
		return nil, classifyCompileError(err)
	}

	runCtx, cancel := context.WithCancel(context.Background())

	budget := newOutputBudget(config.MaxOutputBytes)
	stdout := config.Stdout
	if stdout == nil {
		stdout = discardWriter{}
	}
	stderr := config.Stderr
	if stderr == nil {
		stderr = discardWriter{}
	}

	moduleConfig := wazero.NewModuleConfig().
		WithStartFunctions().
		WithName(config.Name).
		WithArgs(config.Args...).
		WithStdout(writerAdapter{&boundedWriter{target: stdout, budget: budget}}).
		WithStderr(writerAdapter{&boundedWriter{target: stderr, budget: budget}}).
		WithStdin(bytes.NewReader(config.Stdin))

	if config.Dir != "" {
		moduleConfig = moduleConfig.WithEnv("PWD", config.Dir)
	}
	for _, entry := range config.Env {
		if name, value, found := cutEnv(entry); found {
			moduleConfig = moduleConfig.WithEnv(name, value)
		}
	}

	state := newRunState()
	state.cancel = cancel
	instance := &hostInstance{state: state, compiled: compiled, stdout: stdout, stderr: stderr, hosts: boundModules}

	state.setInterruptHook(func() { cancel() })

	if config.Cancel != nil {
		go func() {
			select {
			case <-config.Cancel:
				_ = instance.Interrupt(signal.TERM)
			case <-state.done:
			}
		}()
	}
	go func() {
		defer cancel()
		module, startErr := e.runtime.InstantiateModule(runCtx, compiled, moduleConfig)
		if startErr != nil {
			instance.settle(runCtx.Err() != nil, startErr)
			return
		}
		instance.attach(module)

		entry := config.Entry
		if entry == "" {
			entry = "_start"
		}
		start := module.ExportedFunction(entry)
		if start == nil {
			instance.settle(false, errno.Wrap(errno.NoExec, "模块未导出入口 "+entry))
			return
		}
		_, startErr = start.Call(runCtx)
		instance.settle(runCtx.Err() != nil, startErr)
	}()
	return instance, nil
}

func (e *HostEngine) Close() error {
	e.mu.Lock()
	if e.close {
		e.mu.Unlock()
		return nil
	}
	e.close = true
	e.mu.Unlock()
	return e.runtime.Close(context.Background())
}

type hostInstance struct {
	state    *runState
	compiled wazero.CompiledModule
	stdout   Writer
	stderr   Writer

	hosts []api.Module
}

func (i *hostInstance) attach(module api.Module) {
	if module == nil {
		return
	}
	i.state.attachModule(module)
}

func (i *hostInstance) release() error {
	err := i.state.releaseModule()
	for _, host := range i.hosts {
		err = errors.Join(err, host.Close(context.Background()))
	}
	i.hosts = nil
	return err
}

func (i *hostInstance) settle(cancelled bool, startErr error) {

	if cancelled {
		i.state.finish(Result{Code: signal.ExitCode(signal.TERM), Signal: signal.TERM, Reason: Interrupted}, nil)
		return
	}
	if startErr == nil {
		i.state.finish(Result{Reason: Exited}, nil)
		return
	}
	var exit *sys.ExitError
	if errors.As(startErr, &exit) {
		code := exit.ExitCode()

		if code <= 0 {
			i.state.finish(Result{Reason: Exited}, nil)
			return
		}
		i.state.finish(Result{Code: uint8(code), Reason: Exited}, nil)
		return
	}

	if errors.Is(startErr, errno.Limit) {
		i.state.finish(Result{Reason: Quota}, errno.Wrap(startErr, "WASM 模块超出资源预算"))
		return
	}
	i.state.finish(Result{Reason: Fault}, errno.Wrap(startErr, "WASM 模块执行故障"))
}

func (i *hostInstance) Wait(ctx context.Context) (Result, error) { return i.state.wait(ctx) }

func (i *hostInstance) Interrupt(n signal.Number) error {
	_ = n
	return i.state.interrupt()
}

func (i *hostInstance) Close() error {
	cancel, first := i.state.close()
	if !first {
		return nil
	}
	if cancel != nil {
		cancel()
	}

	_ = i.release()
	<-i.state.done
	return nil
}

type wazeroMemory struct{ memory api.Memory }

func (m wazeroMemory) Size() uint32 { return m.memory.Size() }

func (m wazeroMemory) Read(offset, length uint32) ([]byte, error) {
	if length == 0 {
		if offset > m.memory.Size() {
			return nil, errno.Wrap(errno.Range, "客体内存指针越界")
		}
		return nil, nil
	}
	size := m.memory.Size()
	if offset >= size || length > size-offset {
		return nil, errno.Wrap(errno.Range, "客体内存指针越界")
	}
	view, ok := m.memory.Read(offset, length)
	if !ok {
		return nil, errno.Wrap(errno.Range, "客体内存读取失败")
	}
	return view, nil
}

func classifyCompileError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) {
		return errno.Wrap(errno.NoExec, "WASM 模块无法编译")
	}
	return errno.Wrap(err, "WASM 模块无法编译")
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

type writerAdapter struct{ target Writer }

func (w writerAdapter) Write(p []byte) (int, error) { return w.target.Write(p) }

type outputBudget struct {
	mu        sync.Mutex
	enabled   bool
	remaining int64
}

func newOutputBudget(limit int64) *outputBudget {
	if limit <= 0 {
		return &outputBudget{}
	}
	return &outputBudget{enabled: true, remaining: limit}
}

func (b *outputBudget) charge(bytes int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.enabled {
		return nil
	}
	if bytes > b.remaining {
		b.remaining = 0
		return errno.Wrap(errno.Limit, "WASM 输出超出预算")
	}
	b.remaining -= bytes
	return nil
}

type boundedWriter struct {
	target Writer
	budget *outputBudget
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if err := w.budget.charge(int64(len(p))); err != nil {

		panic(err)
	}
	return w.target.Write(p)
}

func cutEnv(entry string) (string, string, bool) {
	for index := 0; index < len(entry); index++ {
		if entry[index] == '=' {
			return entry[:index], entry[index+1:], true
		}
	}
	return "", "", false
}

func (e *HostEngine) bindHostModules(ctx context.Context, bindings []HostBinding) ([]api.Module, error) {
	if len(bindings) == 0 {
		return nil, nil
	}

	grouped := map[string][]HostBinding{}
	order := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if err := binding.Validate(); err != nil {
			return nil, err
		}
		if _, seen := grouped[binding.Module]; !seen {
			order = append(order, binding.Module)
		}
		grouped[binding.Module] = append(grouped[binding.Module], binding)
	}

	modules := make([]api.Module, 0, len(order))
	for _, name := range order {
		builder := e.runtime.NewHostModuleBuilder(name)
		for _, binding := range grouped[name] {
			params := toValueTypes(binding.Params)
			results := toValueTypes(binding.Results)
			handler := binding.Handler
			builder.NewFunctionBuilder().
				WithGoModuleFunction(api.GoModuleFunc(func(callCtx context.Context, module api.Module, stack []uint64) {

					args := make([]uint64, len(stack))
					copy(args, stack)
					var memory GuestMemory
					if module != nil && module.Memory() != nil {
						memory = wazeroMemory{memory: module.Memory()}
					}
					if err := handler(callCtx, memory, args); err != nil {

						panic(err)
					}

					for index := range results {
						if index < len(stack) && index < len(args) {
							stack[index] = args[index]
						}
					}
				}), params, results).
				Export(binding.Entry)
		}
		instantiated, err := builder.Instantiate(ctx)
		if err != nil {
			for _, module := range modules {
				_ = module.Close(context.Background())
			}
			return nil, errno.Wrap(err, "装配宿主模块 "+name)
		}
		modules = append(modules, instantiated)
	}
	return modules, nil
}

func toValueTypes(types []ValueType) []api.ValueType {
	out := make([]api.ValueType, 0, len(types))
	for _, value := range types {
		switch value {
		case TypeI32:
			out = append(out, api.ValueTypeI32)
		case TypeI64:
			out = append(out, api.ValueTypeI64)
		case TypeF32:
			out = append(out, api.ValueTypeF32)
		case TypeF64:
			out = append(out, api.ValueTypeF64)
		default:

			out = append(out, api.ValueTypeI32)
		}
	}
	return out
}
