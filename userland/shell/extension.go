package shell

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

type extensionRequest struct {
	Args  []string
	Dir   string
	Env   []string
	Stdin []byte
}
type extensionResponse struct {
	Code           int
	Stdout, Stderr string
}

func extension(ctx context.Context, args []string) error {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	host, err := p.Resources.Host()
	if err != nil {
		return err
	}
	var callErr error
	err = exchangeExtension(ctx, args, commandio.Context(ctx), func(input, output []byte) uint32 {
		result, err := host.Call(ctx, uint64(p.ID), "exec", input)
		callErr = err
		if len(result) > len(output) {
			callErr = errno.Limit
		}
		if callErr != nil {
			return 0
		}
		return uint32(copy(output, result))
	})
	if callErr != nil && !errors.Is(callErr, errno.NotFound) {
		return callErr
	}
	return err
}

func extensionWith(ctx context.Context, args []string, exchange func([]byte, []byte) uint32) error {
	return exchangeExtension(ctx, args, interp.HandlerCtx(ctx), exchange)
}

func exchangeExtension(ctx context.Context, args []string, hc interp.HandlerContext, exchange func([]byte, []byte) uint32) error {
	if len(args) == 0 {
		return errno.Invalid
	}
	inputReader := hc.Stdin
	if inputReader == nil {
		inputReader = strings.NewReader("")
	}
	input, err := io.ReadAll(io.LimitReader(inputReader, 1<<20+1))
	if err != nil {
		return err
	}
	if len(input) > 1<<20 {
		return fmt.Errorf("扩展命令输入超过 1 MiB")
	}
	req := extensionRequest{Args: args, Dir: hc.Dir, Stdin: input}
	hc.Env.Each(func(name string, v expand.Variable) bool {
		if v.Exported && v.IsSet() {
			req.Env = append(req.Env, name+"="+v.String())
		}
		return true
	})
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	response := make([]byte, 1<<20)
	n := exchange(data, response)
	if n == 0 || uint64(n) > uint64(len(response)) {
		_, _ = fmt.Fprintf(hc.Stderr, "未注册沙箱命令: %s\n", args[0])
		return interp.ExitStatus(127)
	}
	var result extensionResponse
	if err := json.Unmarshal(response[:n], &result, json.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if _, err := io.WriteString(hc.Stdout, result.Stdout); err != nil {
		return err
	}
	if _, err := io.WriteString(hc.Stderr, result.Stderr); err != nil {
		return err
	}
	if result.Code != 0 {
		return interp.ExitStatus(result.Code)
	}
	return nil
}
