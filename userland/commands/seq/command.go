package seq

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

const maxValues = 1 << 20

func Run(ctx context.Context, args []string) (closeResult0 error) {
	flagArgs, operands := splitArgs(args)
	flags := commandio.Flags(ctx, "seq")
	format := flags.StringP("format", "f", "", "use printf style floating-point FORMAT")
	separator := flags.StringP("separator", "s", "\n", "use STRING to separate numbers")
	equalWidth := flags.BoolP("equal-width", "w", false, "equalize width by padding with leading zeroes")
	terminator := flags.StringP("terminator", "t", "\n", "use STRING to terminate each line")
	if err := commandio.Parse(flags, flagArgs); err != nil {
		return err
	}
	operands = append(operands, flags.Args()...)

	firstStr, incrStr := "1", "1"
	var lastStr string
	switch len(operands) {
	case 1:
		lastStr = operands[0]
	case 2:
		firstStr, lastStr = operands[0], operands[1]
	case 3:
		firstStr, incrStr, lastStr = operands[0], operands[1], operands[2]
	case 0:
		return fmt.Errorf("missing operand")
	default:
		return fmt.Errorf("extra operand %q", operands[3])
	}

	first, err := strconv.ParseFloat(firstStr, 64)
	if err != nil {
		return fmt.Errorf("invalid floating point argument: %q", firstStr)
	}
	incr, err := strconv.ParseFloat(incrStr, 64)
	if err != nil {
		return fmt.Errorf("invalid floating point argument: %q", incrStr)
	}
	last, err := strconv.ParseFloat(lastStr, 64)
	if err != nil {
		return fmt.Errorf("invalid floating point argument: %q", lastStr)
	}
	if incr == 0 {
		return fmt.Errorf("invalid zero increment value: %q", incrStr)
	}
	if flags.Changed("format") && *equalWidth {
		return fmt.Errorf("format string may not be specified when printing equal width strings")
	}

	out := bufio.NewWriter(commandio.Context(ctx).Stdout)
	defer func() {
		closeResult0 = errors.Join(closeResult0, out.Flush())
	}()

	if flags.Changed("format") {
		layout, errMsg := normalizeFormat(*format)
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return printFloats(ctx, out, layout, *separator, *terminator, first, incr, last)
	}

	pFirst, pIncr, pLast := precisionOf(firstStr), precisionOf(incrStr), precisionOf(lastStr)
	scientific := pFirst < 0 || pIncr < 0 || pLast < 0
	prec := max(pFirst, pIncr)

	if !scientific && pFirst == 0 && pIncr == 0 && pLast == 0 {
		fi, err1 := strconv.ParseInt(firstStr, 10, 64)
		ii, err2 := strconv.ParseInt(incrStr, 10, 64)
		li, err3 := strconv.ParseInt(lastStr, 10, 64)
		if err1 == nil && err2 == nil && err3 == nil {
			width := 0
			if *equalWidth {
				width = max(len(firstStr), len(lastStr))
			}
			return printInts(ctx, out, *separator, *terminator, width, fi, ii, li)
		}
	}

	var layout string
	switch {
	case scientific:

		layout = "%.6g"
	case *equalWidth:
		adjust := func(w, p int) int {
			fw := w + (prec - p)
			if p > 0 && prec == 0 {
				fw--
			}
			if p == 0 && prec > 0 {
				fw++
			}
			return fw
		}
		width := max(adjust(len(firstStr), pFirst), adjust(len(lastStr), pLast))
		layout = fmt.Sprintf("%%0%d.%df", width, prec)
	default:
		if prec == 0 {

			layout = "%.6g"
		} else {
			layout = fmt.Sprintf("%%.%df", prec)
		}
	}
	return printFloats(ctx, out, layout, *separator, *terminator, first, incr, last)
}

func splitArgs(args []string) (flagArgs, operands []string) {
	expectValue := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case expectValue:
			flagArgs = append(flagArgs, a)
			expectValue = false
		case a == "--":
			operands = append(operands, args[i+1:]...)
			return
		case len(a) > 1 && a[0] == '-' && (a[1] == '.' || (a[1] >= '0' && a[1] <= '9')):
			operands = append(operands, a)
		case len(a) > 1 && a[0] == '-':
			flagArgs = append(flagArgs, a)
			if a == "-s" || a == "-f" || a == "-t" || a == "--separator" || a == "--format" || a == "--terminator" {
				expectValue = true
			}
		default:
			operands = append(operands, a)
		}
	}
	return
}

func precisionOf(s string) int {
	if strings.ContainsAny(s, "eE") {
		return -1
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

func normalizeFormat(f string) (string, string) {
	directives := 0
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			b.WriteByte(f[i])
			continue
		}
		if i+1 < len(f) && f[i+1] == '%' {
			b.WriteString("%%")
			i++
			continue
		}
		j := i + 1
		for j < len(f) && strings.IndexByte("+-# 0", f[j]) >= 0 {
			j++
		}
		hasPrec := false
		if j < len(f) && f[j] == '.' {
			hasPrec = true
			j++
			for j < len(f) && f[j] >= '0' && f[j] <= '9' {
				j++
			}
		}
		for j < len(f) && f[j] >= '0' && f[j] <= '9' {
			j++
		}
		if j >= len(f) {
			return "", fmt.Sprintf("format %q ends in %%", f)
		}
		switch f[j] {
		case 'e', 'f', 'g', 'E', 'F', 'G':
			directives++
			b.WriteString(f[i:j])
			if !hasPrec {
				b.WriteString(".6")
			}
			b.WriteByte(f[j])
		case 'a', 'A':
			return "", fmt.Sprintf("format %q uses the %%%c directive, which is not supported", f, f[j])
		default:
			return "", fmt.Sprintf("format %q has unknown %%%c directive", f, f[j])
		}
		i = j
	}
	if directives == 0 {
		return "", fmt.Sprintf("format %q has no %% directive", f)
	}
	if directives > 1 {
		return "", fmt.Sprintf("format %q has too many %% directives", f)
	}
	return b.String(), ""
}

func printInts(ctx context.Context, out *bufio.Writer, sep, term string, width int, first, incr, last int64) error {
	printed := false
	for v, n := first, 0; (incr > 0 && v <= last) || (incr < 0 && v >= last); {
		if err := budgetCheck(ctx, n); err != nil {
			return err
		}
		if printed {
			if _, err := out.WriteString(sep); err != nil {
				return err
			}
		}
		if width > 0 {
			if _, err := fmt.Fprintf(out, "%0*d", width, v); err != nil {
				return err
			}
		} else if _, err := out.WriteString(strconv.FormatInt(v, 10)); err != nil {
			return err
		}
		printed = true
		next := v + incr
		if (incr > 0 && next < v) || (incr < 0 && next > v) {
			break
		}
		v = next
		n++
	}
	if printed {
		if _, err := out.WriteString(term); err != nil {
			return err
		}
	}
	return nil
}

func printFloats(ctx context.Context, out *bufio.Writer, format, sep, term string, first, incr, last float64) error {
	printed := false
	for i := 0; ; i++ {
		x := first + float64(i)*incr
		if incr > 0 && x > last || incr < 0 && x < last || math.IsInf(x, 0) {
			break
		}
		if err := budgetCheck(ctx, i); err != nil {
			return err
		}
		if printed {
			if _, err := out.WriteString(sep); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(out, format, x); err != nil {
			return err
		}
		printed = true
	}
	if printed {
		if _, err := out.WriteString(term); err != nil {
			return err
		}
	}
	return nil
}

func budgetCheck(ctx context.Context, count int) error {
	if count >= maxValues {
		return fmt.Errorf("输出超过 %d 项预算", maxValues)
	}
	if count%1024 == 0 && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}
