package cut

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

const maxField = 1 << 20

type span struct{ low, high int }

type selection struct {
	mode       byte
	delimiter  string
	spans      []span
	complement bool
	suppress   bool
}

func (s selection) positions(limit int) []int {
	chosen := make([]bool, limit+2)
	for _, item := range s.spans {
		for value := item.low; value <= min(item.high, limit); value++ {
			chosen[value] = true
		}
	}
	out := make([]int, 0, len(s.spans))
	for value := 1; value <= limit; value++ {
		if chosen[value] != s.complement {
			out = append(out, value)
		}
	}
	return out
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "cut")
	delimiter := flags.StringP("delimiter", "d", "\t", "单字符分隔符")
	fields := flags.StringP("fields", "f", "", "字段编号列表，支持区间 1-3、3-")
	bytes := flags.StringP("bytes", "b", "", "字节位置列表")
	characters := flags.StringP("characters", "c", "", "字符位置列表（C/POSIX 下与 -b 同义）")
	suppress := flags.BoolP("only-delimited", "s", false, "只输出含分隔符的行（仅 -f）")
	noSplit := flags.BoolP("no-split", "n", false, "与 -b 同用时不切分多字节字符（字节语义下是空约束）")
	complement := flags.Bool("complement", false, "取反：输出未选中的部分")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	_ = noSplit
	files := flags.Args()
	given := 0
	for _, text := range []string{*fields, *bytes, *characters} {
		if text != "" {
			given++
		}
	}
	if given != 1 {
		return fmt.Errorf("cut 需要且只需要 -b/-c/-f 之一")
	}
	if len([]rune(*delimiter)) != 1 {
		return fmt.Errorf("cut 需要单字符 -d")
	}
	list := *fields
	mode := byte('f')
	if *bytes != "" {
		list, mode = *bytes, 'b'
	}
	if *characters != "" {
		list, mode = *characters, 'c'
	}
	spans, err := parseList(list)
	if err != nil {
		return err
	}
	instance := selection{mode: mode, delimiter: *delimiter, spans: spans, complement: *complement, suppress: *suppress}
	return commandio.Inputs(ctx, files, func(_, input string) error {
		rows := commandio.Rows(input)
		for _, row := range rows {
			out, emit, err := instance.apply(row)
			if err != nil {
				return err
			}
			if !emit {
				continue
			}

			if _, err := io.WriteString(h.Stdout, out+"\n"); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s selection) apply(row string) (string, bool, error) {
	if s.mode == 'f' {
		if !strings.Contains(row, s.delimiter) {

			return row, !s.suppress, nil
		}
		parts := strings.Split(row, s.delimiter)
		out := make([]string, 0, len(parts))
		for _, position := range s.positions(len(parts)) {
			out = append(out, parts[position-1])
		}
		return strings.Join(out, s.delimiter), true, nil
	}
	data := []byte(row)
	out := make([]byte, 0, len(data))
	for _, position := range s.positions(len(data)) {
		out = append(out, data[position-1])
	}
	return string(out), true, nil
}

func parseList(text string) ([]span, error) {
	spans := make([]span, 0, 4)
	total := 0
	for _, item := range strings.Split(text, ",") {
		if item == "" {
			return nil, fmt.Errorf("cut 列表有空项")
		}
		var low, high int
		if dash := strings.IndexByte(item, '-'); dash >= 0 {
			lowText, highText := item[:dash], item[dash+1:]
			if lowText == "" {
				return nil, fmt.Errorf("cut 区间缺少起点: %s", item)
			}
			parsed, err := listNumber(lowText)
			if err != nil {
				return nil, err
			}
			low = parsed
			high = maxField
			if highText != "" {
				parsed, err := listNumber(highText)
				if err != nil {
					return nil, err
				}
				high = parsed
			}
			if high < low {
				return nil, fmt.Errorf("cut 区间起止颠倒: %s", item)
			}
		} else {
			parsed, err := listNumber(item)
			if err != nil {
				return nil, err
			}
			low, high = parsed, parsed
		}
		total += high - low + 1
		if total > maxField {
			return nil, fmt.Errorf("cut 列表超过 %d 项", maxField)
		}
		spans = append(spans, span{low: low, high: high})
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("cut 列表为空")
	}
	return spans, nil
}

func listNumber(text string) (int, error) {
	value, err := strconv.Atoi(text)
	if err != nil || value < 1 || value > maxField {
		return 0, fmt.Errorf("cut 位置须为 1..%d: %s", maxField, text)
	}
	return value, nil
}
