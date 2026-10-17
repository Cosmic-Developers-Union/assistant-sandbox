package interp

import (
	"context"
	"math"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/resolver"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/parser"
)

const checkContextOps = 1000

type Interpreter struct {
	interp *interp
}

func New(program *parser.Program) (*Interpreter, error) {
	p := newInterp(program)
	return &Interpreter{interp: p}, nil
}

func (p *Interpreter) Execute(config *Config) (int, error) {
	p.interp.resetCore()
	p.interp.checkCtx = false

	err := p.interp.setExecuteConfig(config)
	if err != nil {
		return 0, err
	}

	return p.interp.executeAll()
}

func (p *Interpreter) Array(name string) map[string]any {
	index, exists := p.interp.arrayIndexes[name]
	if !exists {
		return nil
	}
	array := p.interp.array(resolver.Global, index)
	result := make(map[string]any, len(array))
	for k, v := range array {
		switch v.typ {
		case typeNum:
			result[k] = v.n
		case typeStr, typeNumStr:
			result[k] = v.s
		default:
			result[k] = ""
		}
	}
	return result
}

func (p *interp) resetCore() {
	p.scanner = nil
	for k := range p.scanners {
		delete(p.scanners, k)
	}
	p.input = nil
	for k := range p.inputStreams {
		delete(p.inputStreams, k)
	}
	for k := range p.outputStreams {
		delete(p.outputStreams, k)
	}

	p.sp = 0
	p.localArrays = p.localArrays[:0]
	p.callDepth = 0

	p.filename = null()
	p.line = ""
	p.lineIsTrueStr = false
	p.lineNum = num(0)
	p.fileLineNum = num(0)
	p.fields = nil
	p.fieldsIsTrueStr = nil
	p.numFields = num(0)
	p.haveFields = false

	p.matchStart = num(0)
	p.matchLength = num(0)
	p.argc = num(0)

	p.exitStatus = 0
}

func (p *interp) resetVars() {

	for i := range p.globals {
		p.globals[i] = null()
	}

	for _, array := range p.arrays {
		for k := range array {
			delete(array, k)
		}
	}

	p.convertFormat = "%.6g"
	p.outputFormat = "%.6g"
	p.fieldSep = " "
	p.fieldSepRegex = nil
	p.savedFieldSep = " "
	p.savedFieldSepRegex = nil
	p.recordSep = "\n"
	p.recordSepRegex = nil
	p.recordTerminator = ""
	p.outputFieldSep = " "
	p.outputRecordSep = "\n"
	p.subscriptSep = "\x1c"
}

func (p *Interpreter) ResetVars() {
	p.interp.resetVars()
}

func (p *Interpreter) ResetRand() {
	p.interp.randSeed = 1.0
	p.interp.random.Seed(int64(math.Float64bits(p.interp.randSeed)))
}

func (p *Interpreter) ExecuteContext(ctx context.Context, config *Config) (int, error) {
	p.interp.resetCore()
	p.interp.checkCtx = ctx != context.Background() && ctx != context.TODO()
	p.interp.ctx = ctx
	p.interp.ctxDone = ctx.Done()
	p.interp.ctxOps = 0

	err := p.interp.setExecuteConfig(config)
	if err != nil {
		return 0, err
	}

	return p.interp.executeAll()
}

func (p *interp) checkContext() error {
	p.ctxOps++
	if p.ctxOps < checkContextOps {
		return nil
	}
	p.ctxOps = 0
	return p.checkContextNow()
}

func (p *interp) checkContextNow() error {
	select {
	case <-p.ctxDone:
		return p.ctx.Err()
	default:
		return nil
	}
}
