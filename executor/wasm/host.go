package wasm

import (
	"reflect"
	"sort"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type GuestMemory interface {
	Read(offset uint32, length uint32) ([]byte, error)

	Size() uint32
}

type Registry struct {
	entries map[string]map[string]any
}

func NewRegistry() *Registry {
	return &Registry{entries: map[string]map[string]any{}}
}

func (r *Registry) Register(module, entry string, function any) error {
	if r == nil {
		return errno.Wrap(errno.Invalid, "登记表不能为空")
	}
	if module == "" || entry == "" {
		return errno.Wrap(errno.Invalid, "宿主函数的模块名与条目名不能为空")
	}
	if function == nil {
		return errno.Wrap(errno.Invalid, "宿主函数实现不能为空")
	}
	if reflect.TypeOf(function).Kind() != reflect.Func {
		return errno.Wrap(errno.Invalid, "宿主函数必须是函数值")
	}
	if _, exists := r.entries[module][entry]; exists {
		return errno.Wrap(errno.Exist, "宿主函数已登记: "+module+"."+entry)
	}
	if r.entries[module] == nil {
		r.entries[module] = map[string]any{}
	}
	r.entries[module][entry] = function
	return nil
}

func (r *Registry) Lookup(module, entry string) (any, bool) {
	if r == nil {
		return nil, false
	}
	function, ok := r.entries[module][entry]
	return function, ok
}

func (r *Registry) Modules() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Entries(module string) []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.entries[module]))
	for name := range r.entries[module] {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) ValidateImports(module *Module) []Import {
	if module == nil {
		return nil
	}
	var missing []Import
	for _, imported := range module.Imports {
		if _, ok := r.Lookup(imported.Module, imported.Name); !ok {
			missing = append(missing, imported)
		}
	}
	return missing
}

func ReadString(memory GuestMemory, offset uint32, budget int) (string, error) {
	if memory == nil {
		return "", errno.Wrap(errno.Invalid, "缺少客体内存视图")
	}
	if budget <= 0 {
		return "", errno.Wrap(errno.Invalid, "字符串预算必须为正")
	}

	size := memory.Size()
	if offset >= size {
		return "", errno.Wrap(errno.Range, "指针越出客体内存")
	}
	length := uint64(size - offset)
	if length > uint64(budget) {
		length = uint64(budget)
	}
	remaining := uint32(length)
	limited := uint64(size-offset) > uint64(remaining)
	window, err := memory.Read(offset, remaining)
	if err != nil {
		return "", err
	}
	for index, value := range window {
		if value == 0 {
			return string(window[:index]), nil
		}
	}
	if limited {
		return "", errno.Wrap(errno.Range, "字符串未在预算内结束")
	}
	return "", errno.Wrap(errno.Range, "字符串缺少终止符")
}

func ReadBytes(memory GuestMemory, offset, length uint32) ([]byte, error) {
	if memory == nil {
		return nil, errno.Wrap(errno.Invalid, "缺少客体内存视图")
	}
	view, err := memory.Read(offset, length)
	if err != nil {
		return nil, err
	}

	out := make([]byte, len(view))
	copy(out, view)
	return out, nil
}
