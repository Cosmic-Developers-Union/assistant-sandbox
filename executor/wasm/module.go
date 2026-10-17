package wasm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

var magic = []byte{0x00, 0x61, 0x73, 0x6d}
var version = []byte{0x01, 0x00, 0x00, 0x00}

type SectionID byte

const (
	SectionCustom   SectionID = 0
	SectionType     SectionID = 1
	SectionImport   SectionID = 2
	SectionFunction SectionID = 3
	SectionTable    SectionID = 4
	SectionMemory   SectionID = 5
	SectionGlobal   SectionID = 6
	SectionExport   SectionID = 7
	SectionStart    SectionID = 8
	SectionElement  SectionID = 9
	SectionCode     SectionID = 10
	SectionData     SectionID = 11
)

func (s SectionID) String() string {
	switch s {
	case SectionCustom:
		return "custom"
	case SectionType:
		return "type"
	case SectionImport:
		return "import"
	case SectionFunction:
		return "function"
	case SectionTable:
		return "table"
	case SectionMemory:
		return "memory"
	case SectionGlobal:
		return "global"
	case SectionExport:
		return "export"
	case SectionStart:
		return "start"
	case SectionElement:
		return "element"
	case SectionCode:
		return "code"
	case SectionData:
		return "data"
	default:
		return fmt.Sprintf("section(%d)", byte(s))
	}
}

type Import struct {
	Module string
	Name   string
	Kind   byte
}

type Export struct {
	Name string
	Kind byte
}

type Budgets struct {
	MaxBytes int

	MaxImports int

	MaxExports int

	MaxSections int
}

func DefaultBudgets() Budgets {
	return Budgets{MaxBytes: 8 << 20, MaxImports: 64, MaxExports: 256, MaxSections: 32}
}

type Module struct {
	Digest   string
	Size     int
	Sections []SectionID
	Imports  []Import
	Exports  []Export
}

type Analyzer struct {
	budgets Budgets
	cache   map[string]*Module
}

func New(budgets Budgets) *Analyzer {
	return &Analyzer{budgets: budgets, cache: map[string]*Module{}}
}

func (a *Analyzer) Analyze(ctx context.Context, module []byte) (*Module, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.budgets.MaxBytes > 0 && len(module) > a.budgets.MaxBytes {
		return nil, errno.Wrap(errno.Limit, "WASM 模块超出体积预算")
	}
	digest := contentDigest(module)
	if cached, ok := a.cache[digest]; ok {

		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return cached, nil
	}
	parsed, err := a.parse(ctx, module)
	if err != nil {
		return nil, err
	}
	parsed.Digest = digest
	parsed.Size = len(module)
	a.cache[digest] = parsed
	return parsed, nil
}

func (a *Analyzer) parse(ctx context.Context, module []byte) (*Module, error) {
	if len(module) < len(magic)+len(version) {
		return nil, errno.Wrap(errno.NoExec, "WASM 模块过短")
	}
	if !bytes.Equal(module[:len(magic)], magic) {
		return nil, errno.Wrap(errno.NoExec, "WASM 魔数不匹配")
	}
	if !bytes.Equal(module[len(magic):len(magic)+len(version)], version) {
		return nil, errno.Wrap(errno.NoExec, "WASM 版本不支持")
	}
	result := &Module{}
	offset := len(magic) + len(version)
	lastSection := -1
	for offset < len(module) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if a.budgets.MaxSections > 0 && len(result.Sections) >= a.budgets.MaxSections {
			return nil, errno.Wrap(errno.Limit, "WASM 段数超出预算")
		}
		id := SectionID(module[offset])
		offset++

		if id != SectionCustom {
			if int(id) < lastSection {
				return nil, errno.Wrap(errno.NoExec, "WASM 段顺序非法")
			}
			if int(id) == lastSection {
				return nil, errno.Wrap(errno.NoExec, "WASM 段重复")
			}
			lastSection = int(id)
		}
		size, read, err := readUvarint(module[offset:])
		if err != nil {
			return nil, errno.Wrap(errno.NoExec, "WASM 段长度损坏")
		}
		offset += read
		if size > uint64(len(module)-offset) {
			return nil, errno.Wrap(errno.NoExec, "WASM 段长度越界")
		}
		body := module[offset : offset+int(size)]
		offset += int(size)
		result.Sections = append(result.Sections, id)
		switch id {
		case SectionImport:
			imports, err := parseImports(body)
			if err != nil {
				return nil, err
			}
			if a.budgets.MaxImports > 0 && len(imports) > a.budgets.MaxImports {
				return nil, errno.Wrap(errno.Limit, "WASM import 条数超出预算")
			}
			result.Imports = append(result.Imports, imports...)
		case SectionExport:
			exports, err := parseExports(body)
			if err != nil {
				return nil, err
			}
			if a.budgets.MaxExports > 0 && len(exports) > a.budgets.MaxExports {
				return nil, errno.Wrap(errno.Limit, "WASM export 条数超出预算")
			}
			result.Exports = append(result.Exports, exports...)
		}
	}
	return result, nil
}

func parseImports(body []byte) ([]Import, error) {
	count, read, err := readUvarint(body)
	if err != nil {
		return nil, errno.Wrap(errno.NoExec, "WASM import 段损坏")
	}
	offset := read
	imports := make([]Import, 0, count)
	for index := uint64(0); index < count; index++ {
		entry := Import{}
		if entry.Module, offset, err = readName(body, offset); err != nil {
			return nil, err
		}
		if entry.Name, offset, err = readName(body, offset); err != nil {
			return nil, err
		}
		if offset >= len(body) {
			return nil, errno.Wrap(errno.NoExec, "WASM import 类型缺失")
		}
		entry.Kind = body[offset]
		offset++
		if entry.Kind > 3 {
			return nil, errno.Wrap(errno.NoExec, "WASM import 类型未知")
		}

		if offset, err = skipImportType(body, offset, entry.Kind); err != nil {
			return nil, err
		}
		imports = append(imports, entry)
	}
	return imports, nil
}

func skipImportType(body []byte, offset int, kind byte) (int, error) {
	switch kind {
	case 0:
		_, read, err := readUvarint(body[offset:])
		if err != nil {
			return 0, errno.Wrap(errno.NoExec, "WASM 函数 import 描述损坏")
		}
		return offset + read, nil
	case 1:
		if offset >= len(body) {
			return 0, errno.Wrap(errno.NoExec, "WASM 表 import 描述缺失")
		}
		return skipLimits(body, offset+1)
	case 2:
		return skipLimits(body, offset)
	case 3:
		if offset+1 >= len(body) {
			return 0, errno.Wrap(errno.NoExec, "WASM 全局 import 描述缺失")
		}
		return offset + 2, nil
	default:
		return 0, errno.Wrap(errno.NoExec, "WASM import 类型未知")
	}
}

func skipLimits(body []byte, offset int) (int, error) {
	if offset >= len(body) {
		return 0, errno.Wrap(errno.NoExec, "WASM limits 缺失")
	}
	flags := body[offset]
	offset++
	_, read, err := readUvarint(body[offset:])
	if err != nil {
		return 0, errno.Wrap(errno.NoExec, "WASM limits 下界损坏")
	}
	offset += read
	if flags&0x01 != 0 {
		_, read, err = readUvarint(body[offset:])
		if err != nil {
			return 0, errno.Wrap(errno.NoExec, "WASM limits 上界损坏")
		}
		offset += read
	}
	return offset, nil
}

func parseExports(body []byte) ([]Export, error) {
	count, read, err := readUvarint(body)
	if err != nil {
		return nil, errno.Wrap(errno.NoExec, "WASM export 段损坏")
	}
	offset := read
	exports := make([]Export, 0, count)
	for index := uint64(0); index < count; index++ {
		entry := Export{}
		if entry.Name, offset, err = readName(body, offset); err != nil {
			return nil, err
		}
		if offset >= len(body) {
			return nil, errno.Wrap(errno.NoExec, "WASM export 类型缺失")
		}
		entry.Kind = body[offset]
		offset++
		if entry.Kind > 3 {
			return nil, errno.Wrap(errno.NoExec, "WASM export 类型未知")
		}
		_, read, err = readUvarint(body[offset:])
		if err != nil {
			return nil, errno.Wrap(errno.NoExec, "WASM export 索引损坏")
		}
		offset += read
		exports = append(exports, entry)
	}
	return exports, nil
}

func readName(body []byte, offset int) (string, int, error) {
	length, read, err := readUvarint(body[offset:])
	if err != nil {
		return "", 0, errno.Wrap(errno.NoExec, "WASM 名字长度损坏")
	}
	offset += read
	if length > uint64(len(body)-offset) {
		return "", 0, errno.Wrap(errno.NoExec, "WASM 名字越界")
	}
	return string(body[offset : offset+int(length)]), offset + int(length), nil
}

func readUvarint(body []byte) (uint64, int, error) {
	value, read := binary.Uvarint(body)
	if read <= 0 {
		return 0, 0, errno.Wrap(errno.NoExec, "LEB128 编码非法")
	}
	return value, read, nil
}

func contentDigest(module []byte) string {
	sum := sha256.Sum256(module)
	return "sha256:" + hex.EncodeToString(sum[:])
}
