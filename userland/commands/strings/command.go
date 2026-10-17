package stringscmd

import (
	"context"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	environment := envMap(h)
	if system, err := commandio.Filesystem(ctx); err == nil {
		release := vfs.Bind(system, h.Dir, environment)
		defer release()
	}
	rc := &toolcompat.RunContext{
		Ctx: ctx, Dir: h.Dir, Env: environ(h),
		Stdio: toolcompat.Stdio{In: h.Stdin, Out: h.Stdout, Err: h.Stderr},
	}
	if code := cmd.Run(rc, args); code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

func environ(h interp.HandlerContext) []string {
	var environment []string
	if h.Env == nil {
		return environment
	}
	h.Env.Each(func(name string, value expand.Variable) bool {
		if value.IsSet() {
			environment = append(environment, name+"="+value.String())
		}
		return true
	})
	return environment
}

func envMap(h interp.HandlerContext) map[string]string {
	environment := map[string]string{}
	if h.Env == nil {
		return environment
	}
	h.Env.Each(func(name string, value expand.Variable) bool {
		if value.IsSet() {
			environment[name] = value.String()
		}
		return true
	})
	return environment
}
