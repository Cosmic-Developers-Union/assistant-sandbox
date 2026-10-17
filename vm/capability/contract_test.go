package capability

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"testing"
)

var setToAccess = map[string]string{
	"FS":        "Filesystem",
	"Clock":     "Time",
	"Entropy":   "Random",
	"Identity":  "Credentials",
	"FD":        "Descriptors",
	"Spaces":    "AddressSpace",
	"Resources": "Meter",
	"Bridge":    "Host",
	"Image":     "InitialImage",
	"Audit":     "Events",
	"Language":  "Languages",
	"Sessions":  "SessionStore",
	"Snapshots": "SnapshotStore",
	"Replay":    "Journal",
	"Net":       "Network",
	"IPC":       "Communication",

	"UmaskSource": "Umask",
}

func hasMethod(value any, name string) bool {
	if value == nil {
		return false
	}
	_, ok := reflect.TypeOf(value).MethodByName(name)
	return ok
}

func TestEverySetFieldIsReachableThroughAccess(t *testing.T) {
	setType := reflect.TypeOf(Set{})
	accessType := reflect.TypeOf((*Access)(nil)).Elem()

	scopedType := reflect.TypeOf(&scoped{})

	for index := 0; index < setType.NumField(); index++ {
		field := setType.Field(index).Name
		method, ok := setToAccess[field]
		if !ok {
			t.Errorf("Set.%s 未登记对应的 Access 方法：新增能力必须同时更新装配、授权视图与 Scope", field)
			continue
		}
		if _, ok := accessType.MethodByName(method); !ok {
			t.Errorf("Set.%s 对应的 Access.%s 不存在", field, method)
		}

		if _, ok := scopedType.MethodByName(method); !ok {
			t.Errorf("Set.%s 已装配但 scoped 未实现 %s：能力装上了却取不到", field, method)
		}

		if view := Scope(0, Set{}, nil); !hasMethod(view, method) {
			t.Errorf("Scope 返回值不具备 %s：能力装上了却取不到", method)
		}
	}

	known := map[string]bool{}
	for _, method := range setToAccess {
		known[method] = true
	}
	for index := 0; index < accessType.NumMethod(); index++ {
		method := accessType.Method(index).Name
		if !known[method] {
			t.Errorf("Access.%s 未登记在映射表中：请显式登记它对应哪个装配字段", method)
		}
	}
}

func TestSetHasNoRawHostCallbacks(t *testing.T) {
	setType := reflect.TypeOf(Set{})
	functionKind := reflect.TypeOf(func() {}).Kind()
	for index := 0; index < setType.NumField(); index++ {
		field := setType.Field(index)
		if field.Type.Kind() == functionKind {
			t.Errorf("Set.%s 是裸函数类型 %s：宿主回调必须经 vm/bridge 登记", field.Name, field.Type)
		}

		if field.Type.Kind() == reflect.Pointer && field.Type.Elem().Kind() == functionKind {
			t.Errorf("Set.%s 是指向函数的指针 %s：宿主回调必须经 vm/bridge 登记", field.Name, field.Type)
		}
	}
}

func TestScopedDoesNotExportAssembly(t *testing.T) {

	scopedType := reflect.TypeOf(scoped{})
	for index := 0; index < scopedType.NumField(); index++ {
		field := scopedType.Field(index)
		if field.IsExported() {
			t.Errorf("scoped.%s 是导出字段：装配细节不得从授权视图泄露", field.Name)
		}
	}
}

func TestNoPackageLevelMutableBackends(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{

		"scope.go": true,
	}
	for _, file := range files {
		if filepath.Ext(file) != ".go" || len(file) > 8 && file[len(file)-8:] == "_test.go" {
			continue
		}
		if allowed[file] {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if name.Name == "_" {
						continue
					}
					t.Errorf("%s 声明了包级变量 %s：能力必须经 Set/Scope 按进程装配", file, name.Name)
				}
			}
		}
	}
}
