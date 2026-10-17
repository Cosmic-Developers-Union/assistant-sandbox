package sort

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

type keySpec struct {
	startField int
	startChar  int
	endField   int
	endChar    int

	mods string
}

type comparator struct {
	spec         keySpec
	separator    string
	blanks       bool
	dictionary   bool
	fold         bool
	nonPrintable bool
	numeric      bool
	reverse      bool
}

func Run(ctx context.Context, args []string) (closeResult0 error) {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "sort")
	var reverse, unique, number bool
	var ignoreBlanks, dictionary, fold, ignoreNonPrintable bool
	var merge, zero bool
	flags.BoolVarP(&reverse, "reverse", "r", false, "逆序")
	flags.BoolVarP(&unique, "unique", "u", false, "去重")
	flags.BoolVarP(&number, "numeric-sort", "n", false, "数字排序")
	flags.BoolVarP(&ignoreBlanks, "ignore-leading-blanks", "b", false, "比较时忽略前导空白")
	flags.BoolVarP(&dictionary, "dictionary-order", "d", false, "字典序：只比较字母数字与空白")
	flags.BoolVarP(&fold, "ignore-case", "f", false, "折叠大小写")
	flags.BoolVarP(&ignoreNonPrintable, "ignore-nonprinting", "i", false, "忽略不可打印字符")
	flags.BoolVarP(&merge, "merge", "m", false, "合并已排序输入")
	flags.BoolVarP(&zero, "zero-terminated", "z", false, "以 NUL 而不是换行分隔记录")
	separator := flags.StringP("field-separator", "t", "", "字段分隔符")
	key := flags.StringP("key", "k", "", "排序键 N[.C][mods][,M[.C][mods]]")
	output := flags.StringP("output", "o", "", "写入文件而不是标准输出")
	var check bool
	flags.BoolVarP(&check, "check", "c", false, "只检查输入是否已排序")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	_ = merge
	spec, err := parseKey(*key)
	if err != nil {
		return err
	}
	recordSep := "\n"
	if zero {
		recordSep = "\x00"
	}
	var rows []string
	if err := commandio.Inputs(ctx, flags.Args(), func(_ string, input string) error {
		rows = append(rows, splitRecords(input, recordSep)...)
		return nil
	}); err != nil {
		return err
	}
	rules := comparator{spec: spec, separator: *separator, reverse: reverse}

	rules.blanks = ignoreBlanks || strings.ContainsRune(spec.mods, 'b')
	rules.dictionary = dictionary || strings.ContainsRune(spec.mods, 'd')
	rules.fold = fold || strings.ContainsRune(spec.mods, 'f')
	rules.nonPrintable = ignoreNonPrintable || strings.ContainsRune(spec.mods, 'i')
	rules.numeric = number || strings.ContainsRune(spec.mods, 'n')
	rules.reverse = reverse || strings.ContainsRune(spec.mods, 'r')
	if check {

		for index := 1; index < len(rows); index++ {
			compared := rules.compare(rows[index-1], rows[index])
			if compared > 0 || (compared == 0 && unique) {
				_, _ = fmt.Fprintf(h.Stderr, "sort: 第 %d 行顺序错误\n", index+1)
				return interp.ExitStatus(1)
			}
		}
		return nil
	}
	slices.SortStableFunc(rows, rules.compare)
	if unique {
		rows = slices.Compact(rows)
	}
	writer := h.Stdout
	if *output != "" {
		file, err := commandio.Open(ctx, commandio.Path(h.Dir, *output), vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, 0o666)
		if err != nil {
			return fmt.Errorf("sort: 无法写入 %s: %w", *output, err)
		}
		defer func() {
			closeResult0 = errors.Join(closeResult0, file.Close())
		}()

		writer = file
	}
	for _, row := range rows {
		if _, err := io.WriteString(writer, row+recordSep); err != nil {
			return err
		}
	}
	return nil
}

func splitRecords(input, separator string) []string {
	if input == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(input, separator), separator)
}

func (c comparator) compare(left, right string) int {
	compared := c.compareKeys(c.key(left), c.key(right))
	if c.reverse {
		compared = -compared
	}
	if compared != 0 {
		return compared
	}
	if c.spec.empty() {
		return 0
	}
	compared = strings.Compare(left, right)
	if c.reverse {
		compared = -compared
	}
	return compared
}

func (c comparator) compareKeys(left, right string) int {
	left = c.transform(left)
	right = c.transform(right)
	if c.numeric {
		leftValue, _ := strconv.ParseFloat(strings.Fields(left + " 0")[0], 64)
		rightValue, _ := strconv.ParseFloat(strings.Fields(right + " 0")[0], 64)
		switch {
		case leftValue < rightValue:
			return -1
		case leftValue > rightValue:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(left, right)
}

func (c comparator) transform(key string) string {
	if c.blanks {
		key = strings.TrimLeft(key, " \t")
	}
	if c.nonPrintable || c.dictionary {
		var builder strings.Builder
		for index := 0; index < len(key); index++ {
			value := key[index]
			printable := value >= 0x20 && value < 0x7f
			if c.nonPrintable && !printable {
				continue
			}
			if c.dictionary && (!printable || (!isAlnum(value) && value != ' ' && value != '\t')) {
				continue
			}
			builder.WriteByte(value)
		}
		key = builder.String()
	}
	if c.fold {
		key = strings.ToUpper(key)
	}
	return key
}

func isAlnum(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func (k keySpec) empty() bool {
	return k.startField <= 1 && k.endField == 0 && k.startChar == 0 && k.endChar == 0
}

type field struct{ start, end int }

func fields(row, separator string) []field {
	result := make([]field, 0, 8)
	if separator != "" {
		start := 0
		for index := 0; index <= len(row); index++ {
			if index == len(row) || row[index] == separator[0] {
				result = append(result, field{start: start, end: index})
				start = index + 1
			}
		}
		return result
	}
	index := 0
	for index < len(row) {
		for index < len(row) && (row[index] == ' ' || row[index] == '\t') {
			index++
		}
		if index >= len(row) {
			break
		}
		start := index
		for index < len(row) && row[index] != ' ' && row[index] != '\t' {
			index++
		}
		result = append(result, field{start: start, end: index})
	}
	return result
}

func (c comparator) key(row string) string {
	spec := c.spec
	if spec.empty() {
		return row
	}
	parts := fields(row, c.separator)
	if len(parts) == 0 || spec.startField > len(parts) {
		return ""
	}
	start := parts[spec.startField-1].start
	if spec.startChar > 0 {
		start = min(start+spec.startChar-1, parts[spec.startField-1].end)
	}
	end := len(row)
	if spec.endField > 0 {
		if spec.endField > len(parts) {
			end = len(row)
		} else {
			end = parts[spec.endField-1].end
			if spec.endChar > 0 {
				end = min(parts[spec.endField-1].start+spec.endChar, parts[spec.endField-1].end)
			}
		}
	}
	if end < start {
		return ""
	}
	return row[start:end]
}

func parseKey(text string) (keySpec, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return keySpec{}, nil
	}
	startText, endText, hasEnd := strings.Cut(text, ",")
	spec, err := parseKeyField(startText)
	if err != nil {
		return keySpec{}, err
	}
	if hasEnd && strings.TrimSpace(endText) != "" {
		end, err := parseKeyField(endText)
		if err != nil {
			return keySpec{}, err
		}
		if end.startField < spec.startField {
			return keySpec{}, fmt.Errorf("sort 键区间起止颠倒: %s", text)
		}
		spec.endField, spec.endChar = end.startField, end.startChar
		spec.mods += end.mods
	}
	return spec, nil
}

func parseKeyField(text string) (keySpec, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return keySpec{}, fmt.Errorf("sort 键缺少字段号")
	}
	index := 0
	for index < len(text) && text[index] >= '0' && text[index] <= '9' {
		index++
	}
	if index == 0 {
		return keySpec{}, fmt.Errorf("sort 键须为 1 起的字段号: %s", text)
	}
	fieldNumber, err := strconv.Atoi(text[:index])
	if err != nil || fieldNumber < 1 {
		return keySpec{}, fmt.Errorf("sort 键须为 1 起的字段号: %s", text)
	}
	spec := keySpec{startField: fieldNumber}
	if index < len(text) && text[index] == '.' {
		index++
		start := index
		for index < len(text) && text[index] >= '0' && text[index] <= '9' {
			index++
		}
		if start == index {
			return keySpec{}, fmt.Errorf("sort 键的字符偏移无效: %s", text)
		}
		offset, err := strconv.Atoi(text[start:index])
		if err != nil || offset < 1 {
			return keySpec{}, fmt.Errorf("sort 键的字符偏移无效: %s", text)
		}
		spec.startChar = offset
	}
	for ; index < len(text); index++ {
		if !strings.ContainsRune("bdfinr", rune(text[index])) {
			return keySpec{}, fmt.Errorf("sort 键修饰字母无效: %c", text[index])
		}
		spec.mods += string(text[index])
	}
	return spec, nil
}
