package bash

import (
	"context"
	"slices"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func TestProcessEnvironmentPrefersProcess(t *testing.T) {
	handler := interp.HandlerContext{Env: expand.ListEnviron("FROM_INTERP=1")}
	withProcess := func(environment map[string]string) context.Context {
		return proc.WithContext(context.Background(), proc.Proc{ID: 1, Env: environment, Resources: capability.Set{}})
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
		want []string
	}{
		{"无进程上下文退回解释器", context.Background(), []string{"FROM_INTERP=1"}},
		{"进程不带环境退回解释器", withProcess(nil), []string{"FROM_INTERP=1"}},
		{"进程环境优先且按名排序", withProcess(map[string]string{"B": "2", "A": "1"}), []string{"A=1", "B=2"}},

		{"空环境原样保留", withProcess(map[string]string{}), []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := processEnvironment(test.ctx, handler)
			if !slices.Equal(got, test.want) {
				t.Fatalf("得到 %q，期望 %q", got, test.want)
			}
		})
	}
}
