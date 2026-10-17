package executable

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type Program interface {
	Run(context.Context, proc.Proc) (uint8, error)
}

type Func func(context.Context, proc.Proc) (uint8, error)

func (f Func) Run(ctx context.Context, p proc.Proc) (uint8, error) { return f(ctx, p) }

type Registry interface{ Lookup(string) (Program, error) }

type Static struct{ entries map[string]Program }

func New(entries map[string]Program) (*Static, error) {
	for name, p := range entries {
		_, missing := capability.Require(p)
		if name == "" || strings.ContainsAny(name, "/\\\x00 \t\r\n") || missing != nil {
			return nil, fmt.Errorf("无效程序登记%s: %w", name, errno.Invalid)
		}
	}
	return &Static{entries: maps.Clone(entries)}, nil
}

func (r *Static) Lookup(name string) (Program, error) {
	p, ok := r.entries[name]
	if !ok {
		return nil, fmt.Errorf("程序%s: %w", name, errno.NotFound)
	}
	return p, nil
}

func (r *Static) Names() []string {
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func Merge(registries ...Registry) (Registry, error) {
	merged := map[string]Program{}
	for _, registry := range registries {
		if named, ok := registry.(*Static); ok {
			for name, program := range named.entries {
				if _, conflict := merged[name]; conflict {
					return nil, fmt.Errorf("程序%s重复登记: %w", name, errno.Exist)
				}
				merged[name] = program
			}
			continue
		}
		return nil, errno.Invalid
	}
	static, err := New(merged)
	if err != nil {
		return nil, err
	}
	return static, nil
}
