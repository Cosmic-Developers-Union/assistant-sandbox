package sleep

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "sleep")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) == 0 {
		return fmt.Errorf("缺少时长")
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	clock, err := p.Resources.Time()
	if err != nil {
		return err
	}
	for _, operand := range operands {
		nanoseconds, err := duration(operand)
		if err != nil {
			return err
		}
		if err := clock.Sleep(ctx, nanoseconds); err != nil {
			return err
		}
	}
	return nil
}

func duration(text string) (uint64, error) {
	scale := uint64(1_000_000_000)
	if len(text) > 0 {
		switch text[len(text)-1] {
		case 's':
			text = text[:len(text)-1]
		case 'm':
			text, scale = text[:len(text)-1], 60*1_000_000_000
		case 'h':
			text, scale = text[:len(text)-1], 3600*1_000_000_000
		case 'd':
			text, scale = text[:len(text)-1], 86400*1_000_000_000
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("时长无效")
	}
	whole, fraction := text, ""
	if index := strings.IndexByte(text, '.'); index >= 0 {
		whole, fraction = text[:index], text[index+1:]
	}
	value, err := parseDigits(whole)
	if err != nil {
		return 0, fmt.Errorf("时长无效: %s", text)
	}
	nanoseconds := value * scale
	if fraction != "" {
		parsed, err := parseDigits(fraction)
		if err != nil {
			return 0, fmt.Errorf("时长无效: %s", text)
		}
		for count := len(fraction); count < 9; count++ {
			parsed *= 10
		}
		nanoseconds += parsed * (scale / 1_000_000_000)
	}
	return nanoseconds, nil
}

func parseDigits(text string) (uint64, error) {
	if text == "" {
		return 0, fmt.Errorf("空数字")
	}
	var value uint64
	for index := 0; index < len(text); index++ {
		if text[index] < '0' || text[index] > '9' {
			return 0, fmt.Errorf("非数字")
		}
		value = value*10 + uint64(text[index]-'0')
	}
	return value, nil
}
