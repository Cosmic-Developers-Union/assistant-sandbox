package ast

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/lexer"
)

type Program struct {
	Begin     []Stmts
	Actions   []*Action
	End       []Stmts
	Functions []*Function
}

func (p *Program) String() string {
	parts := []string{}
	for _, ss := range p.Begin {
		parts = append(parts, "BEGIN {\n"+ss.String()+"}")
	}
	for _, a := range p.Actions {
		parts = append(parts, a.String())
	}
	for _, ss := range p.End {
		parts = append(parts, "END {\n"+ss.String()+"}")
	}
	for _, function := range p.Functions {
		parts = append(parts, function.String())
	}
	return strings.Join(parts, "\n\n")
}

type Stmts []Stmt

func (ss Stmts) String() string {
	lines := []string{}
	for _, s := range ss {
		subLines := strings.Split(s.String(), "\n")
		for _, sl := range subLines {
			lines = append(lines, "    "+sl+"\n")
		}
	}
	return strings.Join(lines, "")
}

type Action struct {
	Pattern []Expr
	Stmts   Stmts
}

func (a *Action) String() string {
	patterns := make([]string, len(a.Pattern))
	for i, p := range a.Pattern {
		patterns[i] = p.String()
	}
	sep := ""
	if len(patterns) > 0 && a.Stmts != nil {
		sep = " "
	}
	stmtsStr := ""
	if a.Stmts != nil {
		stmtsStr = "{\n" + a.Stmts.String() + "}"
	}
	return strings.Join(patterns, ", ") + sep + stmtsStr
}

type Node interface {
	node()
}

func (p *Program) node()        {}
func (a *Action) node()         {}
func (f *Function) node()       {}
func (e *FieldExpr) node()      {}
func (e *NamedFieldExpr) node() {}
func (e *UnaryExpr) node()      {}
func (e *BinaryExpr) node()     {}
func (e *InExpr) node()         {}
func (e *CondExpr) node()       {}
func (e *NumExpr) node()        {}
func (e *StrExpr) node()        {}
func (e *RegExpr) node()        {}
func (e *VarExpr) node()        {}
func (e *IndexExpr) node()      {}
func (e *AssignExpr) node()     {}
func (e *AugAssignExpr) node()  {}
func (e *IncrExpr) node()       {}
func (e *CallExpr) node()       {}
func (e *UserCallExpr) node()   {}
func (e *MultiExpr) node()      {}
func (e *GetlineExpr) node()    {}
func (e *GroupingExpr) node()   {}
func (s *PrintStmt) node()      {}
func (s *PrintfStmt) node()     {}
func (s *ExprStmt) node()       {}
func (s *IfStmt) node()         {}
func (s *ForStmt) node()        {}
func (s *ForInStmt) node()      {}
func (s *WhileStmt) node()      {}
func (s *DoWhileStmt) node()    {}
func (s *BreakStmt) node()      {}
func (s *ContinueStmt) node()   {}
func (s *NextStmt) node()       {}
func (s *NextfileStmt) node()   {}
func (s *ExitStmt) node()       {}
func (s *DeleteStmt) node()     {}
func (s *ReturnStmt) node()     {}
func (s *BlockStmt) node()      {}

type Expr interface {
	Node
	precedence() int
	String() string
}

const (
	precAssign = iota
	precCond
	precOr
	precAnd
	precIn
	precMatch
	precCompare
	precConcat
	precAdd
	precMul
	precUnary
	precPower
	precPreIncr
	precPostIncr
	precField
	precPrimary
	precGrouping
)

func (e *FieldExpr) precedence() int      { return precField }
func (e *NamedFieldExpr) precedence() int { return precField }
func (e *UnaryExpr) precedence() int      { return precUnary }
func (e *InExpr) precedence() int         { return precIn }
func (e *CondExpr) precedence() int       { return precCond }
func (e *NumExpr) precedence() int        { return precPrimary }
func (e *StrExpr) precedence() int        { return precPrimary }
func (e *RegExpr) precedence() int        { return precPrimary }
func (e *VarExpr) precedence() int        { return precPrimary }
func (e *IndexExpr) precedence() int      { return precPrimary }
func (e *AssignExpr) precedence() int     { return precAssign }
func (e *AugAssignExpr) precedence() int  { return precAssign }
func (e *CallExpr) precedence() int       { return precPrimary }
func (e *UserCallExpr) precedence() int   { return precPrimary }
func (e *MultiExpr) precedence() int      { return precPrimary }
func (e *GetlineExpr) precedence() int    { return precPrimary }
func (e *GroupingExpr) precedence() int   { return precGrouping }

func (e *IncrExpr) precedence() int {
	if e.Pre {
		return precPreIncr
	}
	return precPostIncr
}

func (e *BinaryExpr) precedence() int {
	switch e.Op {
	case lexer.AND:
		return precAnd
	case lexer.OR:
		return precOr
	case lexer.CONCAT:
		return precConcat
	case lexer.ADD, lexer.SUB:
		return precAdd
	case lexer.MUL, lexer.DIV, lexer.MOD:
		return precMul
	case lexer.EQUALS, lexer.LESS, lexer.LTE, lexer.GREATER, lexer.GTE, lexer.NOT_EQUALS:
		return precCompare
	case lexer.MATCH, lexer.NOT_MATCH:
		return precMatch
	case lexer.POW:
		return precPower
	default:
		return precPrimary
	}
}

func parenthesize(e, other Expr) string {
	if e.precedence() < other.precedence() {
		return "(" + e.String() + ")"
	}
	return e.String()
}

type FieldExpr struct {
	Index Expr
}

func (e *FieldExpr) String() string {
	return "$" + parenthesize(e.Index, e)
}

type NamedFieldExpr struct {
	Field Expr
}

func (e *NamedFieldExpr) String() string {
	return "@" + parenthesize(e.Field, e)
}

type UnaryExpr struct {
	Op    lexer.Token
	Value Expr
}

func (e *UnaryExpr) String() string {
	return e.Op.String() + parenthesize(e.Value, e)
}

type BinaryExpr struct {
	Left  Expr
	Op    lexer.Token
	Right Expr
}

func (e *BinaryExpr) String() string {
	var op string
	if e.Op == lexer.CONCAT {
		op = " "
	} else {
		op = " " + e.Op.String() + " "
	}
	return parenthesize(e.Left, e) + op + parenthesize(e.Right, e)
}

type InExpr struct {
	Index    []Expr
	Array    string
	ArrayPos lexer.Position
}

func (e *InExpr) String() string {
	if len(e.Index) == 1 {
		return parenthesize(e.Index[0], e) + " in " + e.Array
	}
	indices := make([]string, len(e.Index))
	for i, index := range e.Index {
		indices[i] = index.String()
	}
	return "(" + strings.Join(indices, ", ") + ") in " + e.Array
}

type CondExpr struct {
	Cond  Expr
	True  Expr
	False Expr
}

func (e *CondExpr) String() string {
	return parenthesize(e.Cond, e) + " ? " + parenthesize(e.True, e) + " : " + parenthesize(e.False, e)
}

type NumExpr struct {
	Value float64
}

func (e *NumExpr) String() string {
	if e.Value == float64(int64(e.Value)) {
		return strconv.FormatInt(int64(e.Value), 10)
	} else {
		return fmt.Sprintf("%.6g", e.Value)
	}
}

type StrExpr struct {
	Value string
	Regex bool
}

func (e *StrExpr) String() string {
	if e.Regex {
		return formatRegex(e.Value)
	}
	return strconv.Quote(e.Value)
}

type RegExpr struct {
	Regex string
}

func (e *RegExpr) String() string {
	return formatRegex(e.Regex)
}

type VarExpr struct {
	Name string
	Pos  lexer.Position
}

func (e *VarExpr) String() string {
	return e.Name
}

type IndexExpr struct {
	Array    string
	ArrayPos lexer.Position
	Index    []Expr
}

func (e *IndexExpr) String() string {
	indices := make([]string, len(e.Index))
	for i, index := range e.Index {
		indices[i] = index.String()
	}
	return e.Array + "[" + strings.Join(indices, ", ") + "]"
}

type AssignExpr struct {
	Left  Expr
	Right Expr
}

func (e *AssignExpr) String() string {
	return parenthesize(e.Left, e) + " = " + parenthesize(e.Right, e)
}

type AugAssignExpr struct {
	Left  Expr
	Op    lexer.Token
	Right Expr
}

func (e *AugAssignExpr) String() string {
	return parenthesize(e.Left, e) + " " + e.Op.String() + "= " + parenthesize(e.Right, e)
}

type IncrExpr struct {
	Expr Expr
	Op   lexer.Token
	Pre  bool
}

func (e *IncrExpr) String() string {
	if e.Pre {
		return e.Op.String() + parenthesize(e.Expr, e)
	} else {
		return parenthesize(e.Expr, e) + e.Op.String()
	}
}

type CallExpr struct {
	Func lexer.Token
	Args []Expr
}

func (e *CallExpr) String() string {
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = a.String()
	}
	return e.Func.String() + "(" + strings.Join(args, ", ") + ")"
}

type UserCallExpr struct {
	Name string
	Args []Expr
	Pos  lexer.Position
}

func (e *UserCallExpr) String() string {
	args := make([]string, len(e.Args))
	for i, a := range e.Args {
		args[i] = a.String()
	}
	return e.Name + "(" + strings.Join(args, ", ") + ")"
}

type MultiExpr struct {
	Exprs []Expr
}

func (e *MultiExpr) String() string {
	exprs := make([]string, len(e.Exprs))
	for i, e := range e.Exprs {
		exprs[i] = e.String()
	}
	return "(" + strings.Join(exprs, ", ") + ")"
}

type GetlineExpr struct {
	Command Expr
	Target  Expr
	File    Expr
}

func (e *GetlineExpr) String() string {
	s := ""
	if e.Command != nil {
		s += parenthesize(e.Command, e) + " |"
	}
	s += "getline"
	if e.Target != nil {
		s += " " + e.Target.String()
	}
	if e.File != nil {
		s += " <" + parenthesize(e.File, e)
	}
	return s
}

type GroupingExpr struct {
	Expr Expr
}

func (e *GroupingExpr) String() string {
	return "(" + e.Expr.String() + ")"
}

func IsLValue(expr Expr) bool {
	switch expr.(type) {
	case *VarExpr, *IndexExpr, *FieldExpr:
		return true
	default:
		return false
	}
}

func formatRegex(r string) string {
	escaped := strings.ReplaceAll(r, "/", `\/`)
	return "/" + escaped + "/"
}

type Stmt interface {
	Node
	StartPos() lexer.Position
	EndPos() lexer.Position
	stmt()
	String() string
}

func (s *PrintStmt) stmt()    {}
func (s *PrintfStmt) stmt()   {}
func (s *ExprStmt) stmt()     {}
func (s *IfStmt) stmt()       {}
func (s *ForStmt) stmt()      {}
func (s *ForInStmt) stmt()    {}
func (s *WhileStmt) stmt()    {}
func (s *DoWhileStmt) stmt()  {}
func (s *BreakStmt) stmt()    {}
func (s *ContinueStmt) stmt() {}
func (s *NextStmt) stmt()     {}
func (s *NextfileStmt) stmt() {}
func (s *ExitStmt) stmt()     {}
func (s *DeleteStmt) stmt()   {}
func (s *ReturnStmt) stmt()   {}
func (s *BlockStmt) stmt()    {}

func (s *PrintStmt) StartPos() lexer.Position    { return s.Start }
func (s *PrintfStmt) StartPos() lexer.Position   { return s.Start }
func (s *ExprStmt) StartPos() lexer.Position     { return s.Start }
func (s *IfStmt) StartPos() lexer.Position       { return s.Start }
func (s *ForStmt) StartPos() lexer.Position      { return s.Start }
func (s *ForInStmt) StartPos() lexer.Position    { return s.Start }
func (s *WhileStmt) StartPos() lexer.Position    { return s.Start }
func (s *DoWhileStmt) StartPos() lexer.Position  { return s.Start }
func (s *BreakStmt) StartPos() lexer.Position    { return s.Start }
func (s *ContinueStmt) StartPos() lexer.Position { return s.Start }
func (s *NextStmt) StartPos() lexer.Position     { return s.Start }
func (s *NextfileStmt) StartPos() lexer.Position { return s.Start }
func (s *ExitStmt) StartPos() lexer.Position     { return s.Start }
func (s *DeleteStmt) StartPos() lexer.Position   { return s.Start }
func (s *ReturnStmt) StartPos() lexer.Position   { return s.Start }
func (s *BlockStmt) StartPos() lexer.Position    { return s.Start }

func (s *PrintStmt) EndPos() lexer.Position    { return s.End }
func (s *PrintfStmt) EndPos() lexer.Position   { return s.End }
func (s *ExprStmt) EndPos() lexer.Position     { return s.End }
func (s *IfStmt) EndPos() lexer.Position       { return s.End }
func (s *ForStmt) EndPos() lexer.Position      { return s.End }
func (s *ForInStmt) EndPos() lexer.Position    { return s.End }
func (s *WhileStmt) EndPos() lexer.Position    { return s.End }
func (s *DoWhileStmt) EndPos() lexer.Position  { return s.End }
func (s *BreakStmt) EndPos() lexer.Position    { return s.End }
func (s *ContinueStmt) EndPos() lexer.Position { return s.End }
func (s *NextStmt) EndPos() lexer.Position     { return s.End }
func (s *NextfileStmt) EndPos() lexer.Position { return s.End }
func (s *ExitStmt) EndPos() lexer.Position     { return s.End }
func (s *DeleteStmt) EndPos() lexer.Position   { return s.End }
func (s *ReturnStmt) EndPos() lexer.Position   { return s.End }
func (s *BlockStmt) EndPos() lexer.Position    { return s.End }

type PrintStmt struct {
	Args     []Expr
	Redirect lexer.Token
	Dest     Expr
	Start    lexer.Position
	End      lexer.Position
}

func (s *PrintStmt) String() string {
	return printString("print", s.Args, s.Redirect, s.Dest)
}

func printString(f string, args []Expr, redirect lexer.Token, dest Expr) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.String()
	}
	str := f + " " + strings.Join(parts, ", ")
	if dest != nil {
		str += " " + redirect.String() + dest.String()
	}
	return str
}

type PrintfStmt struct {
	Args     []Expr
	Redirect lexer.Token
	Dest     Expr
	Start    lexer.Position
	End      lexer.Position
}

func (s *PrintfStmt) String() string {
	return printString("printf", s.Args, s.Redirect, s.Dest)
}

type ExprStmt struct {
	Expr  Expr
	Start lexer.Position
	End   lexer.Position
}

func (s *ExprStmt) String() string {
	return s.Expr.String()
}

type IfStmt struct {
	Cond      Expr
	BodyStart lexer.Position
	Body      Stmts
	Else      Stmts
	Start     lexer.Position
	End       lexer.Position
}

func (s *IfStmt) String() string {
	str := "if (" + s.Cond.String() + ") {\n" + s.Body.String() + "}"
	if len(s.Else) > 0 {
		str += " else {\n" + s.Else.String() + "}"
	}
	return str
}

type ForStmt struct {
	Pre       Stmt
	Cond      Expr
	Post      Stmt
	BodyStart lexer.Position
	Body      Stmts
	Start     lexer.Position
	End       lexer.Position
}

func (s *ForStmt) String() string {
	preStr := ""
	if s.Pre != nil {
		preStr = s.Pre.String()
	}
	condStr := ""
	if s.Cond != nil {
		condStr = " " + s.Cond.String()
	}
	postStr := ""
	if s.Post != nil {
		postStr = " " + s.Post.String()
	}
	return "for (" + preStr + ";" + condStr + ";" + postStr + ") {\n" + s.Body.String() + "}"
}

type ForInStmt struct {
	Var       string
	VarPos    lexer.Position
	Array     string
	ArrayPos  lexer.Position
	BodyStart lexer.Position
	Body      Stmts
	Start     lexer.Position
	End       lexer.Position
}

func (s *ForInStmt) String() string {
	return "for (" + s.Var + " in " + s.Array + ") {\n" + s.Body.String() + "}"
}

type WhileStmt struct {
	Cond      Expr
	BodyStart lexer.Position
	Body      Stmts
	Start     lexer.Position
	End       lexer.Position
}

func (s *WhileStmt) String() string {
	return "while (" + s.Cond.String() + ") {\n" + s.Body.String() + "}"
}

type DoWhileStmt struct {
	Body  Stmts
	Cond  Expr
	Start lexer.Position
	End   lexer.Position
}

func (s *DoWhileStmt) String() string {
	return "do {\n" + s.Body.String() + "} while (" + s.Cond.String() + ")"
}

type BreakStmt struct {
	Start lexer.Position
	End   lexer.Position
}

func (s *BreakStmt) String() string {
	return "break"
}

type ContinueStmt struct {
	Start lexer.Position
	End   lexer.Position
}

func (s *ContinueStmt) String() string {
	return "continue"
}

type NextStmt struct {
	Start lexer.Position
	End   lexer.Position
}

func (s *NextStmt) String() string {
	return "next"
}

type NextfileStmt struct {
	Start lexer.Position
	End   lexer.Position
}

func (s *NextfileStmt) String() string {
	return "nextfile"
}

type ExitStmt struct {
	Status Expr
	Start  lexer.Position
	End    lexer.Position
}

func (s *ExitStmt) String() string {
	var statusStr string
	if s.Status != nil {
		statusStr = " " + s.Status.String()
	}
	return "exit" + statusStr
}

type DeleteStmt struct {
	Array    string
	ArrayPos lexer.Position
	Index    []Expr
	Start    lexer.Position
	End      lexer.Position
}

func (s *DeleteStmt) String() string {
	if len(s.Index) == 0 {
		return "delete " + s.Array
	}
	indices := make([]string, len(s.Index))
	for i, index := range s.Index {
		indices[i] = index.String()
	}
	return "delete " + s.Array + "[" + strings.Join(indices, ", ") + "]"
}

type ReturnStmt struct {
	Value Expr
	Start lexer.Position
	End   lexer.Position
}

func (s *ReturnStmt) String() string {
	var valueStr string
	if s.Value != nil {
		valueStr = " " + s.Value.String()
	}
	return "return" + valueStr
}

type BlockStmt struct {
	Body  Stmts
	Start lexer.Position
	End   lexer.Position
}

func (s *BlockStmt) String() string {
	return "{\n" + s.Body.String() + "}"
}

type Function struct {
	Name   string
	Params []string
	Body   Stmts
	Pos    lexer.Position
}

func (f *Function) String() string {
	return "function " + f.Name + "(" + strings.Join(f.Params, ", ") + ") {\n" +
		f.Body.String() + "}"
}

type PositionError struct {
	Position lexer.Position

	Message string
}

func PosErrorf(pos lexer.Position, format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	return &PositionError{pos, message}
}

func (e *PositionError) Error() string {
	return fmt.Sprintf("parse error at %d:%d: %s", e.Position.Line, e.Position.Column, e.Message)
}
