package legacy

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type executionKey struct{}
type executionState struct {
	filesystem wazero.FSConfig
	budget     *outputBudget
}
type moduleRequest struct {
	Args  []string
	Dir   string
	Env   []string
	Stdin []byte
}
type moduleResponse struct {
	Code           int
	Stdout, Stderr string
}

func (r *Runtime) callModule(ctx context.Context, caller api.Module, request, size, response, capacity uint32) uint32 {
	if size > 2<<20 || capacity > 1<<20 {
		return 0
	}
	data, ok := caller.Memory().Read(request, size)
	if !ok {
		return 0
	}
	var req moduleRequest
	if err := json.Unmarshal(data, &req, json.RejectUnknownMembers(true)); err != nil || len(req.Args) == 0 || len(req.Stdin) > 1<<20 {
		return 0
	}
	module := r.modules[req.Args[0]]
	if module == nil {
		return 0
	}
	if err := validateStream(req.Args[0], req.Args[1:], req.Env); err != nil {
		return 0
	}
	state, ok := ctx.Value(executionKey{}).(*executionState)
	if !ok {
		return 0
	}

	if req.Dir != "/work" && req.Dir != "/tmp" && !strings.HasPrefix(req.Dir, "/work/") && !strings.HasPrefix(req.Dir, "/tmp/") {
		return 0
	}
	var out, diagnostic bytes.Buffer
	childBudget := &outputBudget{remaining: 256 << 10, cancel: state.budget.cancel}
	config := wazero.NewModuleConfig().WithName("").WithArgs(req.Args...).WithFSConfig(state.filesystem).WithStdin(bytes.NewReader(req.Stdin)).WithStdout(&boundedOutput{out: &out, budget: childBudget}).WithStderr(&boundedOutput{out: &diagnostic, budget: childBudget})
	for _, value := range req.Env {
		name, value, _ := strings.Cut(value, "=")
		if name != "PWD" {
			config = config.WithEnv(name, value)
		}
	}
	config = config.WithEnv("PWD", req.Dir)
	instance, err := r.runtime.InstantiateModule(ctx, module, config)
	var closeErr error
	if instance != nil {
		closeErr = instance.Close(context.Background())
	}
	code, executionErr := moduleResult(ctx, err)
	executionErr = errors.Join(executionErr, closeErr)
	if executionErr != nil {
		code.ExitCode = 125
		_, _ = fmt.Fprintln(&diagnostic, "执行扩展 WASI 模块失败")
	}
	encoded, err := json.Marshal(moduleResponse{Code: code.ExitCode, Stdout: out.String(), Stderr: diagnostic.String()})
	if err != nil || uint64(len(encoded)) > uint64(capacity) || uint64(len(encoded)) > 1<<32-1 || !caller.Memory().Write(response, encoded) {
		return 0
	}
	return uint32(len(encoded)) //nolint:gosec // 模块名与导出名长度受登记上限约束
}
