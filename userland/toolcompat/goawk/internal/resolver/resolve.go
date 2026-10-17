package resolver

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/ast"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/lexer"
)

type ResolvedProgram struct {
	ast.Program
	resolver *resolver
}

func (r *ResolvedProgram) LookupVar(funcName, name string) (Scope, VarInfo, bool) {
	scope, info, _, exists := r.resolver.lookupVar(funcName, name)
	return scope, info, exists
}

func (r *ResolvedProgram) IterVars(funcName string, f func(name string, info VarInfo)) {
	for name, info := range r.resolver.varInfo[funcName] {
		f(name, info)
	}
}

func (r *ResolvedProgram) LookupFunc(name string) (FuncInfo, bool) {
	info, ok := r.resolver.funcInfo[name]
	return info, ok
}

func (r *ResolvedProgram) IterFuncs(f func(name string, info FuncInfo)) {
	for name, info := range r.resolver.funcInfo {
		f(name, info)
	}
}

type VarInfo struct {
	Type  Type
	Index int
}

type FuncInfo struct {
	Native bool
	Index  int
	Params []string
}

type Scope int

const (
	Local Scope = iota + 1
	Special
	Global
)

func (s Scope) String() string {
	switch s {
	case Local:
		return "local"
	case Special:
		return "special"
	case Global:
		return "global"
	default:
		return "unknown scope"
	}
}

type Type int

const (
	unknown Type = iota
	Scalar
	Array
)

func (t Type) String() string {
	switch t {
	case Scalar:
		return "scalar"
	case Array:
		return "array"
	default:
		return "unknown type"
	}
}

type Config struct {
	DebugTypes bool

	DebugWriter io.Writer

	Funcs map[string]any
}

func Resolve(prog *ast.Program, config *Config) *ResolvedProgram {
	if config == nil {
		config = &Config{}
	}

	funcInfo := make(map[string]FuncInfo)
	var nativeNames []string
	for name := range config.Funcs {
		nativeNames = append(nativeNames, name)
	}
	sort.Strings(nativeNames)
	for i, name := range nativeNames {
		funcInfo[name] = FuncInfo{Native: true, Index: i}
	}

	callGraph := callGraphVisitor{
		calls:    make(map[string]map[string]struct{}),
		funcs:    make(map[string]*ast.Function),
		funcInfo: funcInfo,
	}
	ast.Walk(&callGraph, prog)
	orderedFuncs := topoSort(callGraph.calls)

	called := make(map[string]struct{}, len(orderedFuncs))
	for _, name := range orderedFuncs {
		called[name] = struct{}{}
	}
	for name := range callGraph.funcs {
		if _, ok := called[name]; !ok {
			orderedFuncs = append(orderedFuncs, name)
		}
	}

	varInfo := make(map[string]map[string]VarInfo)
	for funcName, info := range funcInfo {
		if info.Native {
			continue
		}
		varInfo[funcName] = make(map[string]VarInfo)
		for _, param := range info.Params {
			varInfo[funcName][param] = VarInfo{}
		}
	}

	r := resolver{varInfo: varInfo, funcInfo: funcInfo, funcs: callGraph.funcs}
	r.varInfo[""] = make(map[string]VarInfo)

	r.recordVar("", "ARGV", Array, lexer.Position{Line: 1, Column: 1})
	r.recordVar("", "ENVIRON", Array, lexer.Position{Line: 1, Column: 1})
	r.recordVar("", "FIELDS", Array, lexer.Position{Line: 1, Column: 1})

	main := mainVisitor{r: &r, nativeFuncs: config.Funcs}
	updates := r.updates
	main.walkOrdered(prog, orderedFuncs)

	for i := 0; r.updates != updates; i++ {
		updates = r.updates
		main.walkOrdered(prog, orderedFuncs)
		if i >= 100 {
			panic(ast.PosErrorf(lexer.Position{Line: 1, Column: 1},
				"too many iterations trying to resolve variable types"))
		}
	}

	for _, infos := range r.varInfo {
		for varName, info := range infos {
			if info.Type == unknown {
				infos[varName] = VarInfo{Type: Scalar, Index: info.Index}
			}
		}
	}

	for funcName, infos := range r.varInfo {
		var names []string
		if funcName == "" {

			for name := range infos {
				names = append(names, name)
			}
			sort.Strings(names)
		} else {

			names = r.funcInfo[funcName].Params
		}
		scalar := 0
		array := 0
		for _, name := range names {
			info := infos[name]
			if info.Type == Array {
				infos[name] = VarInfo{Type: info.Type, Index: array}
				array++
			} else {
				infos[name] = VarInfo{Type: info.Type, Index: scalar}
				scalar++
			}
		}
	}

	if config.DebugTypes {
		printVarTypes(config.DebugWriter, r.varInfo, r.funcInfo)
	}

	return &ResolvedProgram{
		Program:  *prog,
		resolver: &r,
	}
}

func printVarTypes(w io.Writer, varInfo map[string]map[string]VarInfo, funcInfo map[string]FuncInfo) {
	var funcNames []string
	for funcName := range varInfo {
		funcNames = append(funcNames, funcName)
	}
	sort.Strings(funcNames)
	for _, funcName := range funcNames {
		if funcName != "" {
			info := funcInfo[funcName]
			_, _ = fmt.Fprintf(w, "function %s(%s)  # index %d\n",
				funcName, strings.Join(info.Params, ", "), info.Index)
		} else {
			_, _ = fmt.Fprintln(w, "globals")
		}
		var varNames []string
		for name := range varInfo[funcName] {
			varNames = append(varNames, name)
		}
		sort.Strings(varNames)
		for _, name := range varNames {
			info := varInfo[funcName][name]
			_, _ = fmt.Fprintf(w, "  %s: %s %d\n", name, info.Type, info.Index)
		}
	}
}

type resolver struct {
	varInfo  map[string]map[string]VarInfo
	funcInfo map[string]FuncInfo
	funcs    map[string]*ast.Function
	updates  int
}

func (r *resolver) lookupVar(funcName, varName string) (scope Scope, info VarInfo, varFunc string, exists bool) {

	if funcName != "" {
		if info, exists = r.varInfo[funcName][varName]; exists {
			return Local, info, funcName, true
		}
	}

	index := ast.SpecialVarIndex(varName)
	if index > 0 {

		return Special, VarInfo{Type: Scalar, Index: index}, "", true
	}

	if info, exists = r.varInfo[""][varName]; exists {
		return Global, info, "", true
	}
	return 0, VarInfo{}, "", false
}

func (r *resolver) recordVar(funcName, varName string, typ Type, pos lexer.Position) {
	_, info, varFunc, exists := r.lookupVar(funcName, varName)
	if !exists {

		r.varInfo[""][varName] = VarInfo{Type: typ}
		r.updates++
		if _, isFunc := r.funcs[varName]; isFunc {
			panic(ast.PosErrorf(pos, "global var %q can't also be a function", varName))
		}
		return
	}
	if info.Type != typ && info.Type != unknown && typ != unknown {
		panic(ast.PosErrorf(pos, "can't use %s %q as %s", info.Type, varName, typ))
	}
	if info.Type == unknown && typ != unknown {
		r.varInfo[varFunc][varName] = VarInfo{Type: typ, Index: info.Index}
		r.updates++
	}
}

type callGraphVisitor struct {
	calls    map[string]map[string]struct{}
	funcs    map[string]*ast.Function
	funcInfo map[string]FuncInfo
	curFunc  string
}

func (v *callGraphVisitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.Function:
		if _, ok := v.funcs[n.Name]; ok {
			panic(ast.PosErrorf(n.Pos, "function %q already defined", n.Name))
		}
		v.funcInfo[n.Name] = FuncInfo{Index: len(v.funcs), Params: n.Params}
		v.funcs[n.Name] = n
		v.curFunc = n.Name
		ast.WalkStmtList(v, n.Body)
		v.curFunc = ""

	case *ast.UserCallExpr:
		if _, ok := v.calls[v.curFunc]; !ok {
			v.calls[v.curFunc] = make(map[string]struct{})
		}
		v.calls[v.curFunc][n.Name] = struct{}{}
		ast.WalkExprList(v, n.Args)

	default:
		return v
	}
	return nil
}

type mainVisitor struct {
	r           *resolver
	nativeFuncs map[string]any
	curFunc     string
}

func (v *mainVisitor) walkOrdered(prog *ast.Program, orderedFuncs []string) {
	for _, funcName := range orderedFuncs {
		if funcName == "" {
			continue
		}
		function, exists := v.r.funcs[funcName]
		if !exists {

			continue
		}
		v.curFunc = funcName
		ast.WalkStmtList(v, function.Body)
		v.curFunc = ""
	}
	for _, stmts := range prog.Begin {
		ast.WalkStmtList(v, stmts)
	}
	for _, action := range prog.Actions {
		ast.Walk(v, action)
	}
	for _, stmts := range prog.End {
		ast.WalkStmtList(v, stmts)
	}
}

func (v *mainVisitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.VarExpr:
		v.r.recordVar(v.curFunc, n.Name, Scalar, n.Pos)

	case *ast.ForInStmt:
		v.r.recordVar(v.curFunc, n.Var, Scalar, n.VarPos)
		v.r.recordVar(v.curFunc, n.Array, Array, n.ArrayPos)
		ast.WalkStmtList(v, n.Body)

	case *ast.IndexExpr:
		ast.WalkExprList(v, n.Index)
		v.r.recordVar(v.curFunc, n.Array, Array, n.ArrayPos)

	case *ast.InExpr:
		ast.WalkExprList(v, n.Index)
		v.r.recordVar(v.curFunc, n.Array, Array, n.ArrayPos)

	case *ast.DeleteStmt:
		v.r.recordVar(v.curFunc, n.Array, Array, n.ArrayPos)
		ast.WalkExprList(v, n.Index)

	case *ast.CallExpr:
		switch n.Func {
		case lexer.F_SPLIT:
			ast.Walk(v, n.Args[0])
			varExpr := n.Args[1].(*ast.VarExpr)
			v.r.recordVar(v.curFunc, varExpr.Name, Array, varExpr.Pos)
			ast.WalkExprList(v, n.Args[2:])

		case lexer.F_LENGTH:
			if len(n.Args) > 0 {
				if varExpr, ok := n.Args[0].(*ast.VarExpr); ok {

					v.r.recordVar(v.curFunc, varExpr.Name, unknown, varExpr.Pos)
					return nil
				}
			}
			ast.WalkExprList(v, n.Args)

		default:
			ast.WalkExprList(v, n.Args)
		}

	case *ast.UserCallExpr:
		_, _, varFunc, exists := v.r.lookupVar(v.curFunc, n.Name)
		if varFunc != "" && exists {
			panic(ast.PosErrorf(n.Pos, "can't call local variable %q as function", n.Name))
		}

		funcInfo, exists := v.r.funcInfo[n.Name]
		if !exists {
			panic(ast.PosErrorf(n.Pos, "undefined function %q", n.Name))
		}

		numParams := len(funcInfo.Params)
		if funcInfo.Native {
			typ := reflect.TypeOf(v.nativeFuncs[n.Name])
			numParams = typ.NumIn()
			if typ.IsVariadic() {
				numParams = 1000000000
			}
		}
		if len(n.Args) > numParams {
			panic(ast.PosErrorf(n.Pos, "%q called with more arguments than declared", n.Name))
		}

		for i, arg := range n.Args {
			varExpr, ok := arg.(*ast.VarExpr)
			if !ok {

				if !funcInfo.Native {
					paramInfo := v.r.varInfo[n.Name][funcInfo.Params[i]]
					if paramInfo.Type == Array {
						panic(ast.PosErrorf(n.Pos, "can't pass scalar %s as array param", arg))
					}
				}
				ast.Walk(v, arg)
				continue
			}

			if funcInfo.Native {

				v.r.recordVar(v.curFunc, varExpr.Name, Scalar, varExpr.Pos)
				continue
			}

			paramName := funcInfo.Params[i]
			paramInfo := v.r.varInfo[n.Name][paramName]
			_, varInfo, _, _ := v.r.lookupVar(v.curFunc, varExpr.Name)
			switch {
			case varInfo.Type == unknown && paramInfo.Type != unknown:

				v.r.recordVar(v.curFunc, varExpr.Name, paramInfo.Type, varExpr.Pos)
			case varInfo.Type != unknown && paramInfo.Type == unknown:

				funcPos := v.r.funcs[n.Name].Pos
				v.r.recordVar(n.Name, paramName, varInfo.Type, funcPos)
			case varInfo.Type != paramInfo.Type && varInfo.Type != unknown && paramInfo.Type != unknown:

				panic(ast.PosErrorf(varExpr.Pos, "can't pass %s %q as %s param",
					varInfo.Type, varExpr.Name, paramInfo.Type))
			default:

				v.r.recordVar(v.curFunc, varExpr.Name, unknown, varExpr.Pos)
			}
		}

	default:
		return v
	}
	return nil
}
