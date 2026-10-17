package compiler

//go:generate go run golang.org/x/tools/cmd/stringer@v0.43.0 -type=Opcode,AugOp,BuiltinOp

type Opcode int32

const (
	Nop Opcode = iota

	Num
	Str
	Dupe
	Drop
	Swap
	Rote

	Field
	FieldInt
	FieldByName
	FieldByNameStr
	Global
	Local
	Special
	ArrayGlobal
	ArrayLocal
	InGlobal
	InLocal

	AssignField
	AssignFieldSub
	AssignGlobal
	AssignLocal
	AssignSpecial
	AssignArrayGlobal
	AssignArrayLocal

	Delete
	DeleteAll

	IncrField
	IncrGlobal
	IncrLocal
	IncrSpecial
	IncrArrayGlobal
	IncrArrayLocal

	AugAssignField
	AugAssignGlobal
	AugAssignLocal
	AugAssignSpecial
	AugAssignArrayGlobal
	AugAssignArrayLocal

	Regex

	IndexMulti

	ConcatMulti

	Add
	Subtract
	Multiply
	Divide
	Power
	Modulo
	Equals
	NotEquals
	Less
	Greater
	LessOrEqual
	GreaterOrEqual
	Concat
	Match
	NotMatch

	Not
	UnaryMinus
	UnaryPlus
	Boolean

	Jump
	JumpFalse
	JumpTrue
	JumpEquals
	JumpNotEquals
	JumpLess
	JumpGreater
	JumpLessOrEqual
	JumpGreaterOrEqual
	Next
	Nextfile
	Exit
	ExitStatus
	ForIn
	BreakForIn

	CallBuiltin
	CallLengthArray
	CallSplit
	CallSplitSep
	CallSprintf

	CallUser
	CallNative
	Return
	ReturnNull
	Nulls

	Print
	Printf
	Getline
	GetlineField
	GetlineGlobal
	GetlineLocal
	GetlineSpecial
	GetlineArray

	EndOpcode
)

type AugOp Opcode

const (
	AugOpAdd AugOp = iota
	AugOpSub
	AugOpMul
	AugOpDiv
	AugOpPow
	AugOpMod
)

type BuiltinOp Opcode

const (
	BuiltinAtan2 BuiltinOp = iota
	BuiltinClose
	BuiltinCos
	BuiltinExp
	BuiltinFflush
	BuiltinFflushAll
	BuiltinGsub
	BuiltinIndex
	BuiltinInt
	BuiltinLength
	BuiltinLengthArg
	BuiltinLog
	BuiltinMatch
	BuiltinRand
	BuiltinSin
	BuiltinSqrt
	BuiltinSrand
	BuiltinSrandSeed
	BuiltinSub
	BuiltinSubstr
	BuiltinSubstrLength
	BuiltinSystem
	BuiltinTolower
	BuiltinToupper
)
