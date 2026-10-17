package interp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/ast"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/compiler"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/internal/resolver"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/parser"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/goawk/regex"
)

var (
	errExit     = errors.New("exit")
	errBreak    = errors.New("break")
	errNext     = errors.New("next")
	errNextfile = errors.New("nextfile")

	errCSVSeparator = errors.New("invalid CSV field separator or comment delimiter")

	varRegex = regexp.MustCompile(`^([_a-zA-Z][_a-zA-Z0-9]*)=(.*)`)

	defaultShellCommand = getDefaultShellCommand()
)

type Error struct {
	message string
}

func (e *Error) Error() string {
	return e.message
}

func newError(format string, args ...any) error {
	return &Error{fmt.Sprintf(format, args...)}
}

type returnValue struct {
	Value value
}

func (r returnValue) Error() string {
	return "<return " + r.Value.str("%.6g") + ">"
}

type interp struct {
	output        io.Writer
	errorOutput   io.Writer
	scanner       *bufio.Scanner
	scanners      map[string]*bufio.Scanner
	stdin         io.Reader
	filenameIndex int
	hadFiles      bool
	input         io.Reader
	inputBuffer   []byte
	inputStreams  map[string]inputStream
	outputStreams map[string]outputStream
	noExec        bool
	noFileWrites  bool
	noFileReads   bool
	shellCommand  []string
	csvOutput     *bufio.Writer
	noArgVars     bool
	splitBuffer   []byte
	openFile      OpenFileFunc
	openInput     OpenInputFunc
	decimalPoint  byte
	stringCompare StringCompareFunc
	toLower       CaseMapFunc
	toUpper       CaseMapFunc
	commandDir    string
	commandEnv    []string

	globals       []value
	stack         []value
	sp            int
	frame         []value
	arrays        []map[string]value
	localArrays   [][]int
	callDepth     int
	nativeFuncs   []nativeFunc
	scalarIndexes map[string]int
	arrayIndexes  map[string]int

	filename        value
	line            string
	lineIsTrueStr   bool
	lineNum         value
	fileLineNum     value
	fields          []string
	fieldsIsTrueStr []bool
	numFields       value
	haveFields      bool
	fieldNames      []string
	fieldIndexes    map[string]int
	reparseCSV      bool

	argc             value
	convertFormat    string
	outputFormat     string
	fieldSep         string
	fieldSepRegex    regex.Regexp
	recordSep        string
	recordSepRegex   regex.Regexp
	recordTerminator string
	outputFieldSep   string
	outputRecordSep  string
	subscriptSep     string
	matchLength      value
	matchStart       value
	inputMode        IOMode
	csvInputConfig   CSVInputConfig
	outputMode       IOMode
	csvOutputConfig  CSVOutputConfig

	savedFieldSep      string
	savedFieldSepRegex regex.Regexp

	program   *parser.Program
	functions []compiler.Function
	nums      []float64
	strs      []string
	regexes   []regex.Regexp

	checkCtx bool
	ctx      context.Context
	ctxDone  <-chan struct{}
	ctxOps   int

	random            *rand.Rand
	randSeed          float64
	exitStatus        int
	regexCache        map[string]regex.Regexp
	regexCompiler     regex.Compiler
	formatCache       map[string]cachedFormat
	csvJoinFieldsBuf  bytes.Buffer
	chars             bool
	newlineOutputCRLF bool
}

const (
	maxCachedRegexes = 100
	maxCachedFormats = 100
	maxRecordLength  = 10 * 1024 * 1024
	maxFieldIndex    = 1000000
	maxCallDepth     = 1000
	initialStackSize = 100
	outputBufSize    = 64 * 1024
	inputBufSize     = 64 * 1024
)

type NewlineMode int

const (
	SmartNewlineMode NewlineMode = 0

	RawNewlineMode NewlineMode = 1

	CRLFNewlineMode NewlineMode = 2
)

type Config struct {
	Stdin io.Reader

	Output io.Writer

	Error io.Writer

	DecimalPoint byte

	StringCompare StringCompareFunc

	ToLower CaseMapFunc
	ToUpper CaseMapFunc

	Argv0 string

	Args []string

	NoArgVars bool

	Vars []string

	Funcs map[string]any

	NoExec       bool
	NoFileWrites bool
	NoFileReads  bool

	ShellCommand []string

	CommandDir string
	CommandEnv []string

	Environ []string

	InputMode IOMode

	CSVInput CSVInputConfig

	OutputMode IOMode

	CSVOutput CSVOutputConfig

	Chars bool

	NewlineOutput NewlineMode

	OpenFile OpenFileFunc

	OpenInput OpenInputFunc
}

type OpenFileFunc func(name string, flag int, perm os.FileMode) (*os.File, error)

type StringCompareFunc func(a, b string) (int, error)

type CaseMapFunc func(s string) (string, error)

type IOMode int

const (
	DefaultMode IOMode = 0

	CSVMode IOMode = 1

	TSVMode IOMode = 2
)

type CSVInputConfig struct {
	Separator rune

	Comment rune

	Header bool
}

type CSVOutputConfig struct {
	Separator rune
}

func ExecProgram(program *parser.Program, config *Config) (int, error) {
	p := newInterp(program)
	err := p.setExecuteConfig(config)
	if err != nil {
		return 0, err
	}
	return p.executeAll()
}

func newInterp(program *parser.Program) *interp {
	p := &interp{
		program:       program,
		functions:     program.Compiled.Functions,
		nums:          program.Compiled.Nums,
		strs:          program.Compiled.Strs,
		regexes:       program.Compiled.Regexes,
		regexCompiler: program.Compiled.RegexCompiler,
	}

	p.scalarIndexes = make(map[string]int)
	p.arrayIndexes = make(map[string]int)
	program.IterVars("", func(name string, info resolver.VarInfo) {
		if info.Type == resolver.Array {
			p.arrayIndexes[name] = info.Index
		} else {
			p.scalarIndexes[name] = info.Index
		}
	})
	p.globals = make([]value, len(p.scalarIndexes))
	p.stack = make([]value, initialStackSize)
	p.arrays = make([]map[string]value, len(p.arrayIndexes), len(p.arrayIndexes)+initialStackSize)
	for i := 0; i < len(p.arrayIndexes); i++ {
		p.arrays[i] = make(map[string]value)
	}

	p.regexCache = make(map[string]regex.Regexp, 10)
	p.formatCache = make(map[string]cachedFormat, 10)
	p.randSeed = 1.0
	seed := math.Float64bits(p.randSeed)
	p.random = rand.New(rand.NewSource(int64(seed)))
	p.convertFormat = "%.6g"
	p.outputFormat = "%.6g"
	p.fieldSep = " "
	p.savedFieldSep = " "
	p.recordSep = "\n"
	p.outputFieldSep = " "
	p.outputRecordSep = "\n"
	p.subscriptSep = "\x1c"
	p.lineNum = num(0)
	p.fileLineNum = num(0)
	p.numFields = num(0)
	p.matchStart = num(0)
	p.matchLength = num(0)

	p.inputStreams = make(map[string]inputStream)
	p.outputStreams = make(map[string]outputStream)
	p.scanners = make(map[string]*bufio.Scanner)

	return p
}

func (p *interp) setExecuteConfig(config *Config) error {
	if config == nil {
		config = &Config{}
	}
	if len(config.Vars)%2 != 0 {
		return newError("length of config.Vars must be a multiple of 2, not %d", len(config.Vars))
	}
	if len(config.Environ)%2 != 0 {
		return newError("length of config.Environ must be a multiple of 2, not %d", len(config.Environ))
	}
	p.decimalPoint = config.DecimalPoint
	if p.decimalPoint == 0 {
		p.decimalPoint = '.'
	}
	p.stringCompare = config.StringCompare
	p.toLower = config.ToLower
	if p.toLower == nil {
		p.toLower = func(s string) (string, error) { return strings.ToLower(s), nil }
	}
	p.toUpper = config.ToUpper
	if p.toUpper == nil {
		p.toUpper = func(s string) (string, error) { return strings.ToUpper(s), nil }
	}
	p.commandDir = config.CommandDir
	p.commandEnv = config.CommandEnv

	p.inputMode = config.InputMode
	p.csvInputConfig = config.CSVInput
	switch p.inputMode {
	case CSVMode:
		if p.csvInputConfig.Separator == 0 {
			p.csvInputConfig.Separator = ','
		}
	case TSVMode:
		if p.csvInputConfig.Separator == 0 {
			p.csvInputConfig.Separator = '\t'
		}
	case DefaultMode:
		if p.csvInputConfig != (CSVInputConfig{}) {
			return newError("input mode configuration not valid in default input mode")
		}
	}
	p.outputMode = config.OutputMode
	p.csvOutputConfig = config.CSVOutput
	switch p.outputMode {
	case CSVMode:
		if p.csvOutputConfig.Separator == 0 {
			p.csvOutputConfig.Separator = ','
		}
	case TSVMode:
		if p.csvOutputConfig.Separator == 0 {
			p.csvOutputConfig.Separator = '\t'
		}
	case DefaultMode:
		if p.csvOutputConfig != (CSVOutputConfig{}) {
			return newError("output mode configuration not valid in default output mode")
		}
	}
	if config.OpenFile == nil {
		p.openFile = os.OpenFile
	} else {
		p.openFile = config.OpenFile
	}
	p.openInput = config.OpenInput

	argvIndex := p.arrayIndexes["ARGV"]
	p.setArrayValue(resolver.Global, argvIndex, "0", localeStr(config.Argv0, p.decimalPoint))
	p.argc = num(float64(len(config.Args) + 1))
	for i, arg := range config.Args {
		p.setArrayValue(resolver.Global, argvIndex, strconv.Itoa(i+1), localeNumStr(arg, p.decimalPoint))
	}
	p.noArgVars = config.NoArgVars
	p.filenameIndex = 1
	p.hadFiles = false
	for i := 0; i < len(config.Vars); i += 2 {
		err := p.setVarByName(config.Vars[i], config.Vars[i+1])
		if err != nil {
			return err
		}
	}
	p.chars = config.Chars

	err := validateCSVInputConfig(p.inputMode, p.csvInputConfig)
	if err != nil {
		return err
	}
	err = validateCSVOutputConfig(p.outputMode, p.csvOutputConfig)
	if err != nil {
		return err
	}

	environIndex := p.arrayIndexes["ENVIRON"]
	if config.Environ != nil {
		for i := 0; i < len(config.Environ); i += 2 {
			p.setArrayValue(resolver.Global, environIndex, config.Environ[i], localeNumStr(config.Environ[i+1], p.decimalPoint))
		}
	} else {
		for _, kv := range os.Environ() {
			key, val, ok := strings.Cut(kv, "=")
			if ok {
				p.setArrayValue(resolver.Global, environIndex, key, localeNumStr(val, p.decimalPoint))
			}
		}
	}

	if len(config.ShellCommand) != 0 {
		p.shellCommand = config.ShellCommand
	} else {
		p.shellCommand = defaultShellCommand
	}

	p.noExec = config.NoExec
	p.noFileWrites = config.NoFileWrites
	p.noFileReads = config.NoFileReads
	p.stdin = config.Stdin
	if p.stdin == nil {
		p.stdin = os.Stdin
	}
	p.output = config.Output
	if p.output == nil {
		p.output = bufio.NewWriterSize(os.Stdout, outputBufSize)
	}
	p.errorOutput = config.Error
	if p.errorOutput == nil {
		p.errorOutput = os.Stderr
	}

	if p.nativeFuncs == nil {
		err := p.initNativeFuncs(config.Funcs)
		if err != nil {
			return err
		}
	}

	switch config.NewlineOutput {
	case SmartNewlineMode:
		p.newlineOutputCRLF = (runtime.GOOS == "windows")
	case RawNewlineMode:
		p.newlineOutputCRLF = false
	case CRLFNewlineMode:
		p.newlineOutputCRLF = true
	default:
		return fmt.Errorf("invalid newline output mode %d", config.NewlineOutput)
	}

	return nil
}

func validateCSVInputConfig(mode IOMode, config CSVInputConfig) error {
	if mode != CSVMode && mode != TSVMode {
		return nil
	}
	if config.Separator == config.Comment || !validCSVSeparator(config.Separator) ||
		config.Comment != 0 && !validCSVSeparator(config.Comment) {
		return errCSVSeparator
	}
	return nil
}

func validateCSVOutputConfig(mode IOMode, config CSVOutputConfig) error {
	if mode != CSVMode && mode != TSVMode {
		return nil
	}
	if !validCSVSeparator(config.Separator) {
		return errCSVSeparator
	}
	return nil
}

func validCSVSeparator(r rune) bool {
	return r != 0 && r != '"' && r != '\r' && r != '\n' && utf8.ValidRune(r) && r != utf8.RuneError
}

func (p *interp) executeAll() (int, error) {
	defer p.closeAll()

	err := p.execute(p.program.Compiled.Begin)
	if err != nil && !errors.Is(err, errExit) {
		if p.checkCtx {
			ctxErr := p.checkContextNow()
			if ctxErr != nil {
				return 0, ctxErr
			}
		}
		return 0, err
	}
	if len(p.program.Compiled.Actions) == 0 && len(p.program.Compiled.End) == 0 {
		return p.exitStatus, nil
	}
	if !errors.Is(err, errExit) {
		err = p.execActions(p.program.Compiled.Actions)
		if err != nil && !errors.Is(err, errExit) {
			if p.checkCtx {
				ctxErr := p.checkContextNow()
				if ctxErr != nil {
					return 0, ctxErr
				}
			}
			return 0, err
		}
	}
	err = p.execute(p.program.Compiled.End)
	if err != nil && !errors.Is(err, errExit) {
		if p.checkCtx {
			ctxErr := p.checkContextNow()
			if ctxErr != nil {
				return 0, ctxErr
			}
		}
		return 0, err
	}
	return p.exitStatus, nil
}

func Exec(source, fieldSep string, input io.Reader, output io.Writer) error {
	prog, err := parser.ParseProgram([]byte(source), nil)
	if err != nil {
		return err
	}
	config := &Config{
		Stdin:  input,
		Output: output,
		Error:  io.Discard,
		Vars:   []string{"FS", fieldSep},
	}
	_, err = ExecProgram(prog, config)
	return err
}

func (p *interp) execActions(actions []compiler.Action) error {
	var inRange []bool
lineLoop:
	for {

		line, err := p.nextLine()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		p.setLine(line, false)
		p.reparseCSV = false

		for i, action := range actions {

			matched := false
			switch len(action.Pattern) {
			case 0:

				matched = true
			case 1:

				err := p.execute(action.Pattern[0])
				if err != nil {
					return err
				}
				matched = p.pop().boolean()
			case 2:

				if inRange == nil {
					inRange = make([]bool, len(actions))
				}
				if !inRange[i] {
					err := p.execute(action.Pattern[0])
					if err != nil {
						return err
					}
					inRange[i] = p.pop().boolean()
				}
				matched = inRange[i]
				if inRange[i] {
					err := p.execute(action.Pattern[1])
					if err != nil {
						return err
					}
					inRange[i] = !p.pop().boolean()
				}
			}
			if !matched {
				continue
			}

			if len(action.Body) == 0 {
				err := p.printLine(p.output, p.line)
				if err != nil {
					return err
				}
				continue
			}

			err := p.execute(action.Body)
			switch {
			case errors.Is(err, errNext):

				continue lineLoop
			case errors.Is(err, errNextfile):

				p.scanner = nil
				continue lineLoop
			case err != nil:
				return err
			}
		}
	}
	return nil
}

func (p *interp) getSpecial(index int) (value, error) {
	switch index {
	case ast.V_NF:
		if err := p.ensureFields(); err != nil {
			return null(), err
		}
		return p.numFields, nil
	case ast.V_NR:
		return p.lineNum, nil
	case ast.V_RLENGTH:
		return p.matchLength, nil
	case ast.V_RSTART:
		return p.matchStart, nil
	case ast.V_FNR:
		return p.fileLineNum, nil
	case ast.V_ARGC:
		return p.argc, nil
	case ast.V_CONVFMT:
		return localeStr(p.convertFormat, p.decimalPoint), nil
	case ast.V_FILENAME:
		return p.filename, nil
	case ast.V_FS:
		return localeStr(p.fieldSep, p.decimalPoint), nil
	case ast.V_OFMT:
		return localeStr(p.outputFormat, p.decimalPoint), nil
	case ast.V_OFS:
		return localeStr(p.outputFieldSep, p.decimalPoint), nil
	case ast.V_ORS:
		return localeStr(p.outputRecordSep, p.decimalPoint), nil
	case ast.V_RS:
		return localeStr(p.recordSep, p.decimalPoint), nil
	case ast.V_RT:
		return localeStr(p.recordTerminator, p.decimalPoint), nil
	case ast.V_SUBSEP:
		return localeStr(p.subscriptSep, p.decimalPoint), nil
	case ast.V_INPUTMODE:
		return localeStr(inputModeString(p.inputMode, p.csvInputConfig), p.decimalPoint), nil
	case ast.V_OUTPUTMODE:
		return localeStr(outputModeString(p.outputMode, p.csvOutputConfig), p.decimalPoint), nil
	default:
		panic(fmt.Sprintf("unexpected special variable index: %d", index))
	}
}

func (p *interp) setVarByName(name, value string) error {
	index := ast.SpecialVarIndex(name)
	if index > 0 {
		return p.setSpecial(index, localeNumStr(value, p.decimalPoint))
	}
	index, ok := p.scalarIndexes[name]
	if ok {
		p.globals[index] = localeNumStr(value, p.decimalPoint)
		return nil
	}

	return nil
}

func (p *interp) setSpecial(index int, v value) error {
	switch index {
	case ast.V_NF:
		numFields := int(v.num())
		if numFields < 0 {
			return newError("NF set to negative value: %d", numFields)
		}
		if numFields > maxFieldIndex {
			return newError("NF set too large: %d", numFields)
		}
		if err := p.ensureFields(); err != nil {
			return err
		}
		p.numFields = v
		if numFields < len(p.fields) {
			p.fields = p.fields[:numFields]
			p.fieldsIsTrueStr = p.fieldsIsTrueStr[:numFields]
		}
		for i := len(p.fields); i < numFields; i++ {
			p.fields = append(p.fields, "")
			p.fieldsIsTrueStr = append(p.fieldsIsTrueStr, false)
		}
		p.line = p.joinFields(p.fields)
		p.lineIsTrueStr = true
	case ast.V_NR:
		p.lineNum = v
	case ast.V_RLENGTH:
		p.matchLength = v
	case ast.V_RSTART:
		p.matchStart = v
	case ast.V_FNR:
		p.fileLineNum = v
	case ast.V_ARGC:
		argc := int(v.num())
		if argc > maxFieldIndex {
			return newError("ARGC set too large: %d", argc)
		}
		p.argc = v
	case ast.V_CONVFMT:
		p.convertFormat = p.toString(v)
	case ast.V_FILENAME:
		p.filename = v
	case ast.V_FS:
		p.fieldSep = p.toString(v)
		if utf8.RuneCountInString(p.fieldSep) > 1 {
			re, err := p.compileRegexStd(p.fieldSep)
			if err != nil {
				return err
			}
			p.fieldSepRegex = re
		}
	case ast.V_OFMT:
		p.outputFormat = p.toString(v)
	case ast.V_OFS:
		p.outputFieldSep = p.toString(v)
	case ast.V_ORS:
		p.outputRecordSep = p.toString(v)
	case ast.V_RS:
		p.recordSep = p.toString(v)
		switch {
		case len(p.recordSep) <= 1:

			sep := regexp.QuoteMeta(p.recordSep)
			re, err := p.compileRegexStd(sep)
			if err != nil {
				return err
			}
			p.recordSepRegex = re
		case utf8.RuneCountInString(p.recordSep) == 1:

			sep := regexp.QuoteMeta(p.recordSep)
			re, err := p.compileRegexStd(sep)
			if err != nil {
				return err
			}
			p.recordSepRegex = re
		default:
			re, err := p.compileRegexStd(p.recordSep)
			if err != nil {
				return err
			}
			p.recordSepRegex = re
		}
	case ast.V_RT:
		p.recordTerminator = p.toString(v)
	case ast.V_SUBSEP:
		p.subscriptSep = p.toString(v)
	case ast.V_INPUTMODE:
		var err error
		p.inputMode, p.csvInputConfig, err = parseInputMode(p.toString(v))
		if err != nil {
			return err
		}
		err = validateCSVInputConfig(p.inputMode, p.csvInputConfig)
		if err != nil {
			return err
		}
	case ast.V_OUTPUTMODE:
		var err error
		p.outputMode, p.csvOutputConfig, err = parseOutputMode(p.toString(v))
		if err != nil {
			return err
		}
		err = validateCSVOutputConfig(p.outputMode, p.csvOutputConfig)
		if err != nil {
			return err
		}
	default:
		panic(fmt.Sprintf("unexpected special variable index: %d", index))
	}
	return nil
}

func (p *interp) arrayIndex(scope resolver.Scope, index int) int {
	if scope == resolver.Global {
		return index
	} else {
		return p.localArrays[len(p.localArrays)-1][index]
	}
}

func (p *interp) array(scope resolver.Scope, index int) map[string]value {
	return p.arrays[p.arrayIndex(scope, index)]
}

func (p *interp) localArray(index int) map[string]value {
	return p.arrays[p.localArrays[len(p.localArrays)-1][index]]
}

func (p *interp) setArrayValue(scope resolver.Scope, arrayIndex int, index string, v value) {
	array := p.array(scope, arrayIndex)
	array[index] = v
}

func (p *interp) getField(index int) (value, error) {
	if index == 0 {
		if p.lineIsTrueStr {
			return localeStr(p.line, p.decimalPoint), nil
		} else {
			return localeNumStr(p.line, p.decimalPoint), nil
		}
	}
	if err := p.ensureFields(); err != nil {
		return null(), err
	}
	if index < 1 {
		index = len(p.fields) + 1 + index
		if index < 1 {
			return localeStr("", p.decimalPoint), nil
		}
	}
	if index > len(p.fields) {
		return localeStr("", p.decimalPoint), nil
	}
	if p.fieldsIsTrueStr[index-1] {
		return localeStr(p.fields[index-1], p.decimalPoint), nil
	} else {
		return localeNumStr(p.fields[index-1], p.decimalPoint), nil
	}
}

func (p *interp) getFieldByName(name string) (value, error) {
	if p.fieldIndexes == nil {

		if p.fieldNames == nil {
			return null(), newError(`no field names for @; use -H or add "header" to INPUTMODE, and use "getline" first if in BEGIN`)
		}
		p.fieldIndexes = make(map[string]int, len(p.fieldNames))
		for i, n := range p.fieldNames {
			p.fieldIndexes[n] = i + 1
		}
	}
	index := p.fieldIndexes[name]
	if index == 0 {
		return localeStr("", p.decimalPoint), nil
	}
	return p.getField(index)
}

func (p *interp) setField(index int, value string) error {
	if index == 0 {
		p.setLine(value, true)
		return nil
	}
	if index > maxFieldIndex {
		return newError("field index too large: %d", index)
	}

	if err := p.ensureFields(); err != nil {
		return err
	}
	if index < 1 {
		index = len(p.fields) + 1 + index
		if index < 1 {
			return nil
		}
	}
	for i := len(p.fields); i < index; i++ {
		p.fields = append(p.fields, "")
		p.fieldsIsTrueStr = append(p.fieldsIsTrueStr, true)
	}
	p.fields[index-1] = value
	p.fieldsIsTrueStr[index-1] = true
	p.numFields = num(float64(len(p.fields)))
	p.line = p.joinFields(p.fields)
	p.lineIsTrueStr = true
	return nil
}

func (p *interp) joinFields(fields []string) string {
	switch p.outputMode {
	case CSVMode, TSVMode:
		p.csvJoinFieldsBuf.Reset()
		_ = p.writeCSV(&p.csvJoinFieldsBuf, fields)
		line := p.csvJoinFieldsBuf.Bytes()
		line = line[:len(line)-lenNewline(line)]
		return string(line)
	default:
		return strings.Join(fields, p.outputFieldSep)
	}
}

func (p *interp) toString(v value) string {
	return p.formatValue(v, p.convertFormat)
}

func (p *interp) formatValue(v value, format string) string {
	s := v.str(format)
	if v.typ == typeNum && p.decimalPoint != 0 && p.decimalPoint != '.' {
		s = localizeDecimal(s, p.decimalPoint)
	}
	return s
}

func localizeDecimal(s string, radix byte) string {
	if radix == '.' {
		return s
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i] + string(radix) + s[i+1:]
	}
	return s
}

func (p *interp) compileRegexStd(regexStr string) (regex.Regexp, error) {
	return p.compileRegex(regexStr)
}

func (p *interp) compileRegex(regexStr string) (regex.Regexp, error) {
	if re, ok := p.regexCache[regexStr]; ok {
		return re, nil
	}

	re, err := p.regexCompiler.Compile(regexStr)
	if err != nil {
		return nil, newError("invalid regex %q: %s", regexStr, err)
	}
	if re == nil {
		return nil, newError("invalid regex %q: compiler returned nil", regexStr)
	}

	if len(p.regexCache) < maxCachedRegexes {
		p.regexCache[regexStr] = re
	}
	return re, nil
}

func getDefaultShellCommand() []string {
	executable := "/bin/sh"
	if runtime.GOOS == "windows" {
		executable = "sh"
	}
	return []string{executable, "-c"}
}

func inputModeString(mode IOMode, csvConfig CSVInputConfig) string {
	var s string
	var defaultSep rune
	switch mode {
	case CSVMode:
		s = "csv"
		defaultSep = ','
	case TSVMode:
		s = "tsv"
		defaultSep = '\t'
	case DefaultMode:
		return ""
	}
	if csvConfig.Separator != defaultSep {
		s += " separator=" + string([]rune{csvConfig.Separator})
	}
	if csvConfig.Comment != 0 {
		s += " comment=" + string([]rune{csvConfig.Comment})
	}
	if csvConfig.Header {
		s += " header"
	}
	return s
}

func parseInputMode(s string) (mode IOMode, csvConfig CSVInputConfig, err error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return DefaultMode, CSVInputConfig{}, nil
	}
	switch fields[0] {
	case "csv":
		mode = CSVMode
		csvConfig.Separator = ','
	case "tsv":
		mode = TSVMode
		csvConfig.Separator = '\t'
	default:
		return DefaultMode, CSVInputConfig{}, newError("invalid input mode %q", fields[0])
	}
	for _, field := range fields[1:] {
		key, val, _ := strings.Cut(field, "=")
		switch key {
		case "separator":
			r, n := utf8.DecodeRuneInString(val)
			if n == 0 || n < len(val) {
				return DefaultMode, CSVInputConfig{}, newError("invalid CSV/TSV separator %q", val)
			}
			csvConfig.Separator = r
		case "comment":
			r, n := utf8.DecodeRuneInString(val)
			if n == 0 || n < len(val) {
				return DefaultMode, CSVInputConfig{}, newError("invalid CSV/TSV comment character %q", val)
			}
			csvConfig.Comment = r
		case "header":
			if val != "" && val != "true" && val != "false" {
				return DefaultMode, CSVInputConfig{}, newError("invalid header value %q", val)
			}
			csvConfig.Header = val == "" || val == "true"
		default:
			return DefaultMode, CSVInputConfig{}, newError("invalid input mode key %q", key)
		}
	}
	return mode, csvConfig, nil
}

func outputModeString(mode IOMode, csvConfig CSVOutputConfig) string {
	var s string
	var defaultSep rune
	switch mode {
	case CSVMode:
		s = "csv"
		defaultSep = ','
	case TSVMode:
		s = "tsv"
		defaultSep = '\t'
	case DefaultMode:
		return ""
	}
	if csvConfig.Separator != defaultSep {
		s += " separator=" + string([]rune{csvConfig.Separator})
	}
	return s
}

func parseOutputMode(s string) (mode IOMode, csvConfig CSVOutputConfig, err error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return DefaultMode, CSVOutputConfig{}, nil
	}
	switch fields[0] {
	case "csv":
		mode = CSVMode
		csvConfig.Separator = ','
	case "tsv":
		mode = TSVMode
		csvConfig.Separator = '\t'
	default:
		return DefaultMode, CSVOutputConfig{}, newError("invalid output mode %q", fields[0])
	}
	for _, field := range fields[1:] {
		key, val, _ := strings.Cut(field, "=")
		switch key {
		case "separator":
			r, n := utf8.DecodeRuneInString(val)
			if n == 0 || n < len(val) {
				return DefaultMode, CSVOutputConfig{}, newError("invalid CSV/TSV separator %q", val)
			}
			csvConfig.Separator = r
		default:
			return DefaultMode, CSVOutputConfig{}, newError("invalid output mode key %q", key)
		}
	}
	return mode, csvConfig, nil
}
