package instance

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
)

// Backend 标识 VM 执行后端。零值表示自动选择。
type Backend string

const (
	BackendAuto        Backend = ""
	BackendRISC64      Backend = "riscv64"
	BackendNativeAMD64 Backend = "linux/amd64"
)

var (
	// ErrNativeArchitecture 表示原生后端只能在 linux/amd64 宿主使用。
	ErrNativeArchitecture = errors.New("native backend requires linux/amd64")
	// ErrNativeCapability 表示缺少 namespace、seccomp 或 cgroup 委派能力。
	ErrNativeCapability = errors.New("native backend capability unavailable")
	// ErrBackendUnavailable 表示请求的后端尚未注册或镜像不可用。
	ErrBackendUnavailable = errors.New("requested backend unavailable")
	// ErrNativeWorkBudgetUnsupported 表示原生后端不能解释 RISC-V 指令预算。
	ErrNativeWorkBudgetUnsupported = errors.New("native backend does not support instruction Work budgets")
)

// BackendInstance 是选择层需要的最小生命周期契约；具体后端可额外实现
// Filesystem、Connect 等公开能力。
type BackendInstance interface {
	Close(context.Context) error
}

// BackendFactory 创建一个后端实例。实现可以在返回 error 时同时返回已分配
// 的实例，选择层会调用 Close 完成回滚。
type BackendFactory interface {
	Backend() Backend
	New(context.Context, Options) (BackendInstance, error)
}

// BackendCapabilities 是原生后端的宿主能力快照。
type BackendCapabilities struct {
	GOOS            string
	GOARCH          string
	Namespaces      bool
	Seccomp         bool
	CgroupV2        bool
	CgroupDelegated bool
}

func (c BackendCapabilities) available() bool {
	return c.GOOS == "linux" && c.GOARCH == "amd64" &&
		c.Namespaces && c.Seccomp && c.CgroupV2 && c.CgroupDelegated
}

type backendProbe func(context.Context) (BackendCapabilities, error)

// Selection 保存实际选中的后端与其生命周期实例。
type Selection struct {
	Backend  Backend
	Instance BackendInstance
}

func (s *Selection) Close(ctx context.Context) error {
	if s == nil || s.Instance == nil {
		return nil
	}
	return s.Instance.Close(ctx)
}

// Open 按 Options.Backend 选择后端并创建实例。linux/amd64 注册原生工厂；
// 自动选择只在全部能力探测通过时启用它，否则回退 RISC-V。
func Open(ctx context.Context, opts Options) (*Selection, error) {
	factories := map[Backend]BackendFactory{
		BackendRISC64: riscvBackendFactory{},
	}
	if native := registeredNativeFactory(); native != nil {
		factories[BackendNativeAMD64] = native
	}
	return selectBackend(ctx, opts, probeNativeBackend, backendEnvironment{runtime.GOOS, runtime.GOARCH}, factories)
}

type backendEnvironment struct {
	goos   string
	goarch string
}

func selectBackend(
	ctx context.Context,
	opts Options,
	probe backendProbe,
	environment backendEnvironment,
	factories map[Backend]BackendFactory,
) (*Selection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requested := opts.Backend
	if requested != BackendAuto && requested != BackendRISC64 && requested != BackendNativeAMD64 {
		return nil, fmt.Errorf("未知后端 %q: %w", requested, ErrBackendUnavailable)
	}

	selected := requested
	switch selected {
	case BackendAuto:
		selected = BackendRISC64
		if _, registered := factories[BackendNativeAMD64]; registered && environment.goos == "linux" && environment.goarch == "amd64" {
			capabilities, err := probe(ctx)
			if err == nil && capabilities.available() {
				selected = BackendNativeAMD64
			}
		}
	case BackendNativeAMD64:
		if environment.goos != "linux" || environment.goarch != "amd64" {
			return nil, ErrNativeArchitecture
		}
		if _, registered := factories[BackendNativeAMD64]; !registered {
			return nil, ErrBackendUnavailable
		}
		capabilities, err := probe(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNativeCapability, err)
		}
		if !capabilities.available() {
			return nil, fmt.Errorf("%w: %+v", ErrNativeCapability, capabilities)
		}
	}

	factory, ok := factories[selected]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBackendUnavailable, selected)
	}
	opts.Backend = selected
	instance, err := factory.New(ctx, opts)
	if err != nil {
		if instance != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			err = errors.Join(err, instance.Close(cleanupCtx))
		}
		return nil, err
	}
	if instance == nil {
		return nil, fmt.Errorf("%w: %s factory returned nil", ErrBackendUnavailable, selected)
	}
	return &Selection{Backend: selected, Instance: instance}, nil
}

type riscvBackendFactory struct{}

func (riscvBackendFactory) Backend() Backend { return BackendRISC64 }

func (riscvBackendFactory) New(ctx context.Context, opts Options) (BackendInstance, error) {
	return New(ctx, opts)
}
