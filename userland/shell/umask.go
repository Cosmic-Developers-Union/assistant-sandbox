package shell

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func builtinUmask(ctx context.Context, args []string) error {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	symbolic := false
	var operands []string
	for _, arg := range args {
		switch arg {
		case "-S":
			symbolic = true
		case "-p":

		case "-":
			operands = append(operands, arg)
		default:
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("umask: 未知选项 %s", arg)
			}
			operands = append(operands, arg)
		}
	}
	h := interp.HandlerCtx(ctx)
	if len(operands) == 0 {

		current, err := p.Resources.Umask()
		if err != nil {
			return err
		}
		if symbolic {
			_, err = fmt.Fprintln(h.Stdout, symbolicMask(current))
			return err
		}
		_, err = fmt.Fprintf(h.Stdout, "%04o\n", uint32(current&fs.ModePerm))
		return err
	}
	if len(operands) > 1 {
		return fmt.Errorf("umask: 只接受一个模式操作数")
	}
	mask, err := parseUmask(operands[0])
	if err != nil {
		return err
	}
	control, ok := p.Control.(proc.UmaskControl)
	if !ok {
		return fmt.Errorf("umask: 客体没有创建掩码能力")
	}
	return control.SetUmask(p.ID, mask)
}

func parseUmask(text string) (fs.FileMode, error) {
	if text == "" {
		return 0, fmt.Errorf("umask: 空模式")
	}
	value := uint32(0)
	for _, c := range text {
		if c < '0' || c > '7' {
			return 0, fmt.Errorf("umask: 模式只能是八进制数字: %s", text)
		}
		value = value*8 + uint32(c-'0')
	}
	if value > 0o777 {
		return 0, fmt.Errorf("umask: 模式越界: %s", text)
	}
	return fs.FileMode(value), nil
}

func symbolicMask(mask fs.FileMode) string {
	permissions := fs.FileMode(0o777) &^ (mask & fs.ModePerm)
	parts := make([]string, 0, 3)
	for index, who := range []byte{'u', 'g', 'o'} {
		shift := uint(6 - 3*index)
		bits := (permissions >> shift) & 0o7
		text := string(who) + "="
		if bits&0o4 != 0 {
			text += "r"
		}
		if bits&0o2 != 0 {
			text += "w"
		}
		if bits&0o1 != 0 {
			text += "x"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ",")
}
