package ast

import "fmt"

type Visitor interface {
	Visit(node Node) (w Visitor)
}

func WalkExprList(v Visitor, exprs []Expr) {
	for _, expr := range exprs {
		Walk(v, expr)
	}
}

func WalkStmtList(v Visitor, stmts []Stmt) {
	for _, stmt := range stmts {
		Walk(v, stmt)
	}
}

func Walk(v Visitor, node Node) {
	if node == nil {
		return
	}
	if v = v.Visit(node); v == nil {
		return
	}

	switch n := node.(type) {

	case *FieldExpr:
		Walk(v, n.Index)

	case *NamedFieldExpr:
		Walk(v, n.Field)

	case *UnaryExpr:
		Walk(v, n.Value)

	case *BinaryExpr:
		Walk(v, n.Left)
		Walk(v, n.Right)

	case *InExpr:
		WalkExprList(v, n.Index)

	case *CondExpr:
		Walk(v, n.Cond)
		Walk(v, n.True)
		Walk(v, n.False)

	case *NumExpr:
	case *StrExpr:
	case *RegExpr:
	case *VarExpr:
	case *IndexExpr:
		WalkExprList(v, n.Index)

	case *AssignExpr:
		Walk(v, n.Left)
		Walk(v, n.Right)

	case *AugAssignExpr:
		Walk(v, n.Left)
		Walk(v, n.Right)

	case *IncrExpr:
		Walk(v, n.Expr)

	case *CallExpr:
		WalkExprList(v, n.Args)

	case *UserCallExpr:
		WalkExprList(v, n.Args)

	case *MultiExpr:
		WalkExprList(v, n.Exprs)

	case *GetlineExpr:
		Walk(v, n.Command)
		Walk(v, n.Target)
		Walk(v, n.File)

	case *GroupingExpr:
		Walk(v, n.Expr)

	case *PrintStmt:
		WalkExprList(v, n.Args)
		Walk(v, n.Dest)

	case *PrintfStmt:
		WalkExprList(v, n.Args)
		Walk(v, n.Dest)

	case *ExprStmt:
		Walk(v, n.Expr)

	case *IfStmt:
		Walk(v, n.Cond)
		WalkStmtList(v, n.Body)
		WalkStmtList(v, n.Else)

	case *ForStmt:
		Walk(v, n.Pre)
		Walk(v, n.Cond)
		Walk(v, n.Post)
		WalkStmtList(v, n.Body)

	case *ForInStmt:
		WalkStmtList(v, n.Body)

	case *WhileStmt:
		Walk(v, n.Cond)
		WalkStmtList(v, n.Body)

	case *DoWhileStmt:
		WalkStmtList(v, n.Body)
		Walk(v, n.Cond)

	case *BreakStmt:
	case *ContinueStmt:
	case *NextStmt:
	case *NextfileStmt:
	case *ExitStmt:
		Walk(v, n.Status)

	case *DeleteStmt:
		WalkExprList(v, n.Index)

	case *ReturnStmt:
		Walk(v, n.Value)

	case *BlockStmt:
		WalkStmtList(v, n.Body)

	case *Program:
		for _, stmts := range n.Begin {
			WalkStmtList(v, stmts)
		}
		for _, action := range n.Actions {
			Walk(v, action)
		}
		for _, function := range n.Functions {
			Walk(v, function)
		}
		for _, stmts := range n.End {
			WalkStmtList(v, stmts)
		}

	case *Action:
		WalkExprList(v, n.Pattern)
		WalkStmtList(v, n.Stmts)

	case *Function:
		WalkStmtList(v, n.Body)

	default:
		panic(fmt.Sprintf("ast.Walk: unexpected node type %T", n))
	}

	v.Visit(nil)
}
