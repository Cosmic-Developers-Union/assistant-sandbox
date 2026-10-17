package uniq

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func Run(ctx context.Context, args []string) (closeResult0 error) {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "uniq")
	var count, repeated, uniqueOnly, ignoreCase bool
	var skipFields, skipChars int
	flags.BoolVarP(&count, "count", "c", false, "相邻重复行数")
	flags.BoolVarP(&repeated, "repeated", "d", false, "只输出重复行")
	flags.BoolVarP(&uniqueOnly, "unique", "u", false, "只输出不重复行")
	flags.BoolVarP(&ignoreCase, "ignore-case", "i", false, "忽略大小写")
	flags.IntVarP(&skipFields, "skip-fields", "f", 0, "比较时跳过前 N 个字段")
	flags.IntVarP(&skipChars, "skip-chars", "s", 0, "比较时跳过前 N 个字符")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if repeated && uniqueOnly {
		return fmt.Errorf("-d 与 -u 不能同时使用")
	}
	if skipFields < 0 || skipChars < 0 {
		return fmt.Errorf("跳过数不能为负")
	}
	operands := flags.Args()
	if len(operands) > 2 {
		return fmt.Errorf("最多两个操作数")
	}
	inputs := operands
	if len(inputs) > 0 {
		inputs = inputs[:1]
	}
	output := h.Stdout
	if len(operands) == 2 {
		file, err := commandio.Open(ctx, commandio.Path(h.Dir, operands[1]), vmfs.Create|vmfs.Truncate|vmfs.WriteOnly, 0o644)
		if err != nil {
			return err
		}
		defer func() {
			closeResult0 = errors.Join(closeResult0, file.Close())
		}()

		output = file
	}
	key := func(row string) string {
		value := skip(row, skipFields, skipChars)
		if ignoreCase {
			return strings.ToLower(value)
		}
		return value
	}
	return commandio.Inputs(ctx, inputs, func(_ string, input string) error {
		rows := commandio.Rows(input)
		for index := 0; index < len(rows); {
			next := index + 1
			for next < len(rows) && key(rows[next]) == key(rows[index]) {
				next++
			}
			repeats := next - index
			emit := true
			switch {
			case repeated:
				emit = repeats > 1
			case uniqueOnly:
				emit = repeats == 1
			}
			if emit {
				prefix := ""
				if count {

					prefix = fmt.Sprintf("%d ", repeats)
				}
				if _, err := fmt.Fprintln(output, prefix+rows[index]); err != nil {
					return err
				}
			}
			index = next
		}
		return nil
	})
}

func skip(row string, fields, chars int) string {
	value := row
	if fields > 0 {
		parts := strings.Fields(value)
		if fields >= len(parts) {
			return ""
		}

		index := 0
		for count := 0; count < fields; count++ {
			for index < len(value) && (value[index] == ' ' || value[index] == '\t') {
				index++
			}
			for index < len(value) && value[index] != ' ' && value[index] != '\t' {
				index++
			}
		}
		value = value[index:]
	}
	if chars > 0 {
		if chars >= len(value) {
			return ""
		}
		value = value[chars:]
	}
	return value
}
