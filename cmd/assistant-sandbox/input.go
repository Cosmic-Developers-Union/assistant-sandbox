package main

import (
	"bufio"
	"errors"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const maxShellHistory = 200

// bridgeInput 为 --noediting 的 Bash 补上最基本的上/下历史选择。
// 其余输入原样送进 PTY，行内编辑仍由客体 TTY 处理。
func bridgeInput(input io.Reader, output io.Writer) error {
	return bridgeInputWithCompletion(input, output, io.Discard, nil, nil)
}

func bridgeInputWithCompletion(
	input io.Reader,
	output io.Writer,
	display io.Writer,
	completer func(string) []string,
	prompt func() string,
) error {
	reader := bufio.NewReader(input)
	bridge := shellInputBridge{
		output: output, display: display, completer: completer, prompt: prompt,
	}
	for {
		value, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch value {
		case 0x1b:
			switch readEscapeSequence(reader) {
			case "[A", "OA":
				if err := bridge.recall(-1); err != nil {
					return err
				}
			case "[B", "OB":
				if err := bridge.recall(1); err != nil {
					return err
				}
			}
		case '\r', '\n':
			if err := bridge.write([]byte{value}); err != nil {
				return err
			}
			bridge.commit()
		case 0x7f, 0x08:
			bridge.erase()
			if err := bridge.write([]byte{value}); err != nil {
				return err
			}
		case 0x15:
			bridge.line = bridge.line[:0]
			if err := bridge.write([]byte{value}); err != nil {
				return err
			}
		case 0x03:
			bridge.line = bridge.line[:0]
			if err := bridge.write([]byte{value}); err != nil {
				return err
			}
		case 0x04:
			if err := bridge.write([]byte{value}); err != nil {
				return err
			}
		case '\t':
			if err := bridge.complete(); err != nil {
				return err
			}
		default:
			bridge.line = append(bridge.line, value)
			if err := bridge.write([]byte{value}); err != nil {
				return err
			}
		}
	}
}

type shellInputBridge struct {
	output    io.Writer
	display   io.Writer
	line      []byte
	draft     []byte
	history   []string
	index     int
	completer func(string) []string
	prompt    func() string
}

func (b *shellInputBridge) write(data []byte) error {
	_, err := b.output.Write(data)
	return err
}

func (b *shellInputBridge) commit() {
	if len(b.line) > 0 {
		b.history = append(b.history, string(b.line))
		if len(b.history) > maxShellHistory {
			b.history = b.history[len(b.history)-maxShellHistory:]
		}
	}
	b.line = b.line[:0]
	b.draft = b.draft[:0]
	b.index = len(b.history)
}

func (b *shellInputBridge) erase() {
	if len(b.line) == 0 {
		return
	}
	_, size := utf8.DecodeLastRune(b.line)
	b.line = b.line[:len(b.line)-size]
}

func (b *shellInputBridge) recall(direction int) error {
	if len(b.history) == 0 {
		return nil
	}
	if direction < 0 {
		if b.index == len(b.history) {
			b.draft = append(b.draft[:0], b.line...)
		}
		if b.index == 0 {
			return nil
		}
		b.index--
	} else {
		if b.index >= len(b.history) {
			return nil
		}
		b.index++
	}
	var replacement string
	if b.index == len(b.history) {
		replacement = string(b.draft)
	} else {
		replacement = b.history[b.index]
	}
	b.line = append(b.line[:0], replacement...)
	if err := b.write([]byte{0x15}); err != nil {
		return err
	}
	return b.write([]byte(replacement))
}

func (b *shellInputBridge) complete() error {
	if b.completer == nil || len(b.line) == 0 || strings.ContainsAny(string(b.line), " \t") {
		return b.write([]byte{'\t'})
	}
	prefix := string(b.line)
	candidates := b.completer(prefix)
	if len(candidates) == 0 {
		return nil
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	if len(candidates) == 1 {
		suffix := candidates[0][len(prefix):]
		b.line = append(b.line, suffix...)
		return b.write([]byte(suffix))
	}
	common := candidates[0]
	for _, candidate := range candidates[1:] {
		for !strings.HasPrefix(candidate, common) {
			common = common[:len(common)-1]
		}
	}
	if len(common) > len(prefix) {
		suffix := common[len(prefix):]
		b.line = append(b.line, suffix...)
		return b.write([]byte(suffix))
	}
	if b.display == nil {
		return nil
	}
	prompt := ""
	if b.prompt != nil {
		prompt = b.prompt()
	}
	_, err := io.WriteString(b.display, "\r\n"+strings.Join(candidates, "  ")+"\r\n"+prompt+string(b.line))
	return err
}

func readEscapeSequence(reader *bufio.Reader) string {
	next, err := reader.ReadByte()
	if err != nil {
		return ""
	}
	if next != '[' && next != 'O' {
		return ""
	}
	sequence := []byte{next}
	for len(sequence) < 8 {
		value, err := reader.ReadByte()
		if err != nil {
			break
		}
		sequence = append(sequence, value)
		if value >= 0x40 && value <= 0x7e {
			break
		}
	}
	return string(sequence)
}
