package messages

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"

	"github.com/spf13/pflag"
	"mvdan.cc/sh/v3/interp"
)

type messagesKey struct{}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := pflag.NewFlagSet("messages", pflag.ContinueOnError)
	flags.SetOutput(h.Stderr)
	role := flags.String("role", "assistant", "assistant、user、system、tool 或 all")
	format := flags.String("format", "text", "text 正文或 jsonl 完整消息对象")
	last := flags.Int("last", 0, "只取最近 N 条；0 取全部")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return nil
		}
		return interp.ExitStatus(2)
	}
	if flags.NArg() != 0 || *last < 0 || *format != "text" && *format != "jsonl" || !strings.Contains("|assistant|user|system|tool|all|", "|"+*role+"|") || *role == "" {
		_, _ = fmt.Fprintln(h.Stderr, "messages 参数无效，使用 --help 查看用法")
		return interp.ExitStatus(2)
	}
	records, _ := ctx.Value(messagesKey{}).([]jsontext.Value)
	var selected []string
	for _, raw := range records {
		var message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return fmt.Errorf("解码消息快照: %w", err)
		}
		if *role != "all" && message.Role != *role {
			continue
		}
		if *format == "jsonl" {
			selected = append(selected, string(raw))
			continue
		}
		var body strings.Builder
		for _, part := range message.Content {
			if part.Type == "text" {
				body.WriteString(part.Text)
			}
		}

		if body.Len() > 0 {
			selected = append(selected, body.String())
		}
	}
	if *last > 0 && len(selected) > *last {
		selected = selected[len(selected)-*last:]
	}
	separator := "\n\n"
	suffix := ""
	if *format == "jsonl" {
		separator = "\n"
		if len(selected) > 0 {
			suffix = "\n"
		}
	}
	_, err := io.WriteString(h.Stdout, strings.Join(selected, separator)+suffix)
	return err
}

func WithSnapshot(ctx context.Context, records []jsontext.Value) context.Context {
	return context.WithValue(ctx, messagesKey{}, records)
}
