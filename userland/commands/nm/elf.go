package nm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	ehdrSize     = 64
	shdrSize     = 64
	symEntrySize = 24

	offShoff    = 0x28
	offShent    = 0x3A
	offShnum    = 0x3C
	offShstrndx = 0x3E

	shName    = 0
	shType    = 4
	shFlags   = 8
	shOffset  = 24
	shSize    = 32
	shLink    = 40
	shEntsize = 56

	symName  = 0
	symInfo  = 4
	symShndx = 6
	symValue = 8
)

const (
	identMagic0      = 0x7F
	identMagicE      = 'E'
	identMagicL      = 'L'
	identMagicF      = 'F'
	classELF64       = 2
	dataLittleEndian = 1
	versionCurrent   = 1

	sectionNull   = 0
	sectionSymtab = 2
	sectionStrtab = 3
	sectionNoBits = 8

	flagWrite = 0x1
	flagAlloc = 0x2
	flagExec  = 0x4

	shnUndef  = 0
	shnAbs    = 0xfff1
	shnCommon = 0xfff2

	bindingLocal  = 0
	bindingGlobal = 1
	bindingWeak   = 2

	typeObject = 1
)

var errNotELF = errors.New("不是 ELF64")

var errNoSymtab = errors.New("没有符号表")

var errMalformed = errors.New("ELF 格式损坏")

type section struct {
	name    string
	typ     uint32
	flags   uint64
	link    uint32
	entsize uint64
	offset  uint64
	size    uint64
}

type symbol struct {
	name  string
	value uint64
	info  uint8
	shndx uint16
}

type image struct {
	sections []section
	symbols  []symbol
}

func parseImage(raw []byte) (*image, error) {
	if len(raw) < 4 || raw[0] != identMagic0 || raw[1] != identMagicE || raw[2] != identMagicL || raw[3] != identMagicF {
		return nil, errNotELF
	}
	if len(raw) < ehdrSize {
		return nil, fmt.Errorf("%w：%s", errNotELF, "文件头被截断")
	}
	if class := raw[4]; class != classELF64 {
		return nil, fmt.Errorf("%w：映像不是 64 位（class %d）", errNotELF, class)
	}
	if data := raw[5]; data != dataLittleEndian {
		return nil, fmt.Errorf("%w：映像不是小端（data %d）", errNotELF, data)
	}
	if raw[6] != versionCurrent {
		return nil, fmt.Errorf("%w：e_ident 版本 %d 不是 %d", errMalformed, raw[6], versionCurrent)
	}
	shoff := binary.LittleEndian.Uint64(raw[offShoff:])
	shentsize := binary.LittleEndian.Uint16(raw[offShent:])
	shnum := binary.LittleEndian.Uint16(raw[offShnum:])
	shstrndx := int(binary.LittleEndian.Uint16(raw[offShstrndx:]))
	if shnum == 0 {

		return &image{}, errNoSymtab
	}
	if shentsize != shdrSize {
		return nil, fmt.Errorf("%w：e_shentsize %d 不是 %d", errMalformed, shentsize, shdrSize)
	}
	tableEnd, ok := addOverflow(shoff, uint64(shnum)*shdrSize)
	if !ok || tableEnd > uint64(len(raw)) {
		return nil, fmt.Errorf("%w：节头表 [%#x,%#x) 超出映像", errMalformed, shoff, tableEnd)
	}
	img := &image{sections: make([]section, int(shnum))}
	for i := range img.sections {
		head := raw[shoff+uint64(i)*shdrSize:]
		img.sections[i] = section{
			typ:     binary.LittleEndian.Uint32(head[shType:]),
			flags:   binary.LittleEndian.Uint64(head[shFlags:]),
			link:    binary.LittleEndian.Uint32(head[shLink:]),
			entsize: binary.LittleEndian.Uint64(head[shEntsize:]),
			offset:  binary.LittleEndian.Uint64(head[shOffset:]),
			size:    binary.LittleEndian.Uint64(head[shSize:]),
		}
	}
	img.applySectionNames(raw, shoff, shstrndx)

	for i, sec := range img.sections {
		if sec.typ != sectionSymtab {
			continue
		}
		symbols, err := img.parseSymbols(raw, i)
		if err != nil {
			return nil, err
		}
		img.symbols = symbols
		return img, nil
	}
	return img, errNoSymtab
}

func (img *image) applySectionNames(raw []byte, shoff uint64, shstrndx int) {
	if shstrndx <= 0 || shstrndx >= len(img.sections) {
		return
	}
	data, ok := img.sectionData(raw, shstrndx)
	if !ok {
		return
	}
	for i := range img.sections {
		head := raw[shoff+uint64(i)*shdrSize:]
		img.sections[i].name = cString(data, int(binary.LittleEndian.Uint32(head[shName:])))
	}
}

func (img *image) sectionData(raw []byte, index int) ([]byte, bool) {
	sec := img.sections[index]
	end, ok := addOverflow(sec.offset, sec.size)
	if !ok || end > uint64(len(raw)) {
		return nil, false
	}
	return raw[sec.offset:end], true
}

func (img *image) parseSymbols(raw []byte, symtabIndex int) ([]symbol, error) {
	symtab := img.sections[symtabIndex]
	if uint64(symtab.link) >= uint64(len(img.sections)) {
		return nil, fmt.Errorf("%w：符号表的字符串表索引 %d 越界", errMalformed, symtab.link)
	}
	strtab, ok := img.sectionData(raw, int(symtab.link))
	if !ok {
		return nil, fmt.Errorf("%w：字符串表数据越界", errMalformed)
	}
	entsize := symtab.entsize
	if entsize == 0 {
		entsize = symEntrySize
	}
	if entsize < symEntrySize {
		return nil, fmt.Errorf("%w：符号项大小 %d 小于 %d", errMalformed, entsize, symEntrySize)
	}
	symbolCount := symtab.size / entsize
	if symbolCount > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("%w：符号表项数量超出本机索引范围", errMalformed)
	}
	count := int(symbolCount)
	data, ok := img.sectionData(raw, symtabIndex)
	if !ok {
		return nil, fmt.Errorf("%w：符号表数据越界", errMalformed)
	}
	symbols := make([]symbol, 0, count)
	for i := 0; i < count; i++ {
		entry := data[uint64(i)*entsize:]
		symbols = append(symbols, symbol{
			name:  cString(strtab, int(binary.LittleEndian.Uint32(entry[symName:]))),
			info:  entry[symInfo],
			shndx: binary.LittleEndian.Uint16(entry[symShndx:]),
			value: binary.LittleEndian.Uint64(entry[symValue:]),
		})
	}
	return symbols, nil
}

func cString(strtab []byte, offset int) string {
	if offset <= 0 || offset >= len(strtab) {
		return ""
	}
	if end := indexByte(strtab[offset:], 0); end >= 0 {
		return string(strtab[offset : offset+end])
	}
	return string(strtab[offset:])
}

func indexByte(data []byte, c byte) int {
	for i, b := range data {
		if b == c {
			return i
		}
	}
	return -1
}

func typeLetter(img *image, s symbol) byte {
	lower := s.info>>4 == bindingLocal
	switch {
	case s.shndx == shnUndef:
		return letter('U', lower)
	case s.shndx == shnAbs:
		return letter('A', lower)
	case s.shndx == shnCommon:
		return letter('C', lower)
	case s.info>>4 == bindingWeak && s.info&0xf == typeObject:
		return letter('V', lower)
	case s.info>>4 == bindingWeak:
		return letter('W', lower)
	}
	if int(s.shndx) >= len(img.sections) {
		return '?'
	}
	sec := img.sections[s.shndx]
	switch {
	case sec.flags&flagAlloc == 0:
		return 'n'
	case sec.flags&flagExec != 0:
		return letter('T', lower)
	case sec.flags&flagWrite == 0:
		return letter('R', lower)
	case sec.typ == sectionNoBits:
		return letter('B', lower)
	default:
		return letter('D', lower)
	}
}

func letter(c byte, lower bool) byte {
	if lower {
		return c + ('a' - 'A')
	}
	return c
}

func isExternal(s symbol) bool {
	binding := s.info >> 4
	return binding == bindingGlobal || binding == bindingWeak
}

func addOverflow(left, right uint64) (uint64, bool) {
	sum := left + right
	return sum, sum >= left
}
