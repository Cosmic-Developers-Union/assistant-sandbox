package wc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "wc")
	var lines, words, bytes, chars bool
	flags.BoolVarP(&lines, "lines", "l", false, "行数")
	flags.BoolVarP(&words, "words", "w", false, "词数")
	flags.BoolVarP(&bytes, "bytes", "c", false, "字节数")
	flags.BoolVarP(&chars, "chars", "m", false, "Unicode 字符数")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	all := !lines && !words && !bytes && !chars
	totals := make([]int, 4)
	inputs := 0
	err := commandio.Inputs(ctx, files, func(file, input string) error {
		values := make([]string, 0, 5)
		counts := []int{strings.Count(input, "\n"), len(strings.Fields(input)), len(input), utf8.RuneCountInString(input)}
		if lines || all {
			values = append(values, strconv.Itoa(counts[0]))
		}
		if words || all {
			values = append(values, strconv.Itoa(counts[1]))
		}
		if chars {
			values = append(values, strconv.Itoa(counts[3]))
		}
		if bytes || all {
			values = append(values, strconv.Itoa(counts[2]))
		}
		for index := range totals {
			totals[index] += counts[index]
		}
		inputs++
		if file != "-" {
			values = append(values, file)
		}
		_, err := fmt.Fprintln(h.Stdout, strings.Join(values, " "))
		return err
	})
	if err != nil {
		return err
	}
	if inputs <= 1 {
		return nil
	}
	values := make([]string, 0, 5)
	if lines || all {
		values = append(values, strconv.Itoa(totals[0]))
	}
	if words || all {
		values = append(values, strconv.Itoa(totals[1]))
	}
	if chars {
		values = append(values, strconv.Itoa(totals[3]))
	}
	if bytes || all {
		values = append(values, strconv.Itoa(totals[2]))
	}
	values = append(values, "total")
	_, err = fmt.Fprintln(h.Stdout, strings.Join(values, " "))
	return err
}
