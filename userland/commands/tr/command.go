package tr

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "tr")
	complement := flags.BoolP("complement", "c", false, "取补集")
	del := flags.BoolP("delete", "d", false, "删除集合中的字符")
	squeeze := flags.BoolP("squeeze-repeats", "s", false, "压缩重复字符")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) == 0 || len(operands) > 2 {
		return fmt.Errorf("需要 SET1 与可选 SET2")
	}
	if *del && len(operands) > 1 {
		return fmt.Errorf("删除模式不接受 SET2")
	}
	if !*del && len(operands) < 2 {
		return fmt.Errorf("翻译模式需要 SET2")
	}
	first, err := expand(operands[0])
	if err != nil {
		return err
	}
	selected := membership(first, *complement)
	var second []byte
	if len(operands) == 2 {
		if second, err = expand(operands[1]); err != nil {
			return err
		}
	}

	var mapped [256]byte
	for index := range mapped {
		mapped[index] = byte(index)
	}
	if !*del {
		for index, value := range first {
			target := second[len(second)-1]
			if index < len(second) {
				target = second[index]
			}
			mapped[value] = target
		}
	}
	squeezed := selected
	if len(second) > 0 {
		squeezed = membership(second, false)
	}
	input, err := commandio.ReadText(ctx, h.Stdin)
	if err != nil {
		return err
	}
	var out strings.Builder
	previous := -1
	for index := 0; index < len(input); index++ {
		value := input[index]
		if *del && selected[value] {
			continue
		}
		if !*del {
			value = mapped[value]
		}
		if *squeeze && previous >= 0 && value == byte(previous) && squeezed[value] {
			continue
		}
		out.WriteByte(value)
		previous = int(value)
	}
	_, err = fmt.Fprint(h.Stdout, out.String())
	return err
}

func membership(set []byte, complement bool) [256]bool {
	var table [256]bool
	for _, value := range set {
		table[value] = true
	}
	if complement {
		for index := range table {
			table[index] = !table[index]
		}
	}
	return table
}

func expand(set string) ([]byte, error) {
	var out []byte
	for index := 0; index < len(set); index++ {
		value := set[index]
		if value == '\\' && index+1 < len(set) {
			index++
			switch set[index] {
			case 'n':
				out = append(out, '\n')
			case 't':
				out = append(out, '\t')
			case 'r':
				out = append(out, '\r')
			case '\\':
				out = append(out, '\\')
			default:
				if set[index] >= '0' && set[index] <= '7' {
					number, count := 0, 0
					for count < 3 && index < len(set) && set[index] >= '0' && set[index] <= '7' {
						number = number*8 + int(set[index]-'0')
						index++
						count++
					}
					index--
					out = append(out, byte(number))
					continue
				}
				return nil, fmt.Errorf("不支持的转义: \\%c", set[index])
			}
			continue
		}
		if index+2 < len(set) && set[index+1] == '-' {
			start, end := set[index], set[index+2]
			if start > end {
				return nil, fmt.Errorf("范围起止颠倒: %c-%c", start, end)
			}
			for current := int(start); current <= int(end); current++ {
				out = append(out, byte(current))
			}
			index += 2
			continue
		}
		out = append(out, value)
	}
	return out, nil
}
