package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"mvdan.cc/sh/v3/interp"
)

type shellState struct {
	mu sync.Mutex

	history []historyEntry
	hits    map[string]int

	remembered []string
}

type historyEntry struct{ words []string }

func (e historyEntry) line() string { return strings.Join(e.words, " ") }

func newShellState() *shellState { return &shellState{hits: map[string]int{}} }

func (s *shellState) record(words []string) {
	if len(words) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) >= maxHistoryEntries {
		s.history = s.history[1:]
	}
	s.history = append(s.history, historyEntry{words: append([]string(nil), words...)})
	if len(s.remembered) > 0 {
		for _, name := range s.remembered {
			if name == words[0] {
				s.hits[name]++
			}
		}
	}
}

const maxHistoryEntries = 500

func builtinHash(ctx context.Context, kernel *vm.Kernel, state *shellState, args []string) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	h := interp.HandlerCtx(ctx)
	clear := false
	var operands []string
	for _, arg := range args {
		switch {
		case arg == "-r":
			clear = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("hash: 未知选项 %s", arg)
		default:
			operands = append(operands, arg)
		}
	}
	if clear && len(operands) > 0 {
		return fmt.Errorf("hash: -r 不能与操作数同用")
	}
	if clear {
		state.remembered = nil
		state.hits = map[string]int{}
	}
	if len(operands) == 0 {
		for _, name := range state.remembered {
			if _, err := fmt.Fprintf(h.Stdout, "%d\t%s\n", state.hits[name], displayPath(name)); err != nil {
				return err
			}
		}
		return nil
	}
	missing := false
	for _, name := range operands {
		if !kernel.HasProgram(name) {
			_, _ = fmt.Fprintf(h.Stderr, "hash: %s: 未找到\n", name)
			missing = true
			continue
		}
		if !contains(state.remembered, name) {
			state.remembered = append(state.remembered, name)
			state.hits[name] = 0
		}
	}
	if missing {
		return interp.ExitStatus(1)
	}
	return nil
}

func displayPath(name string) string {
	if strings.Contains(name, "/") {
		return name
	}
	return "/bin/" + name
}

func contains(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func builtinFC(ctx context.Context, state *shellState, args []string) ([]string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	h := interp.HandlerCtx(ctx)
	list, silent, reverse, suppressNumbers := false, false, false, false
	var substitutions [][2]string
	var operands []string
	for _, arg := range args {
		switch {
		case arg == "-l":
			list = true
		case arg == "-n":
			suppressNumbers = true
		case arg == "-r":
			reverse = true
		case arg == "-s":
			silent = true
		case arg == "-e":
			return nil, fmt.Errorf("fc: 客体没有可用编辑器，-e 不支持")
		case strings.HasPrefix(arg, "-e"):
			return nil, fmt.Errorf("fc: 客体没有可用编辑器，-e 不支持")
		case strings.HasPrefix(arg, "-") && !isNumber(arg):
			return nil, fmt.Errorf("fc: 未知选项 %s", arg)
		case silent && strings.Contains(arg, "="):

			parts := strings.SplitN(arg, "=", 2)
			substitutions = append(substitutions, [2]string{parts[0], parts[1]})
		default:
			operands = append(operands, arg)
		}
	}
	if list && silent {
		return nil, fmt.Errorf("fc: -l 与 -s 不能同用")
	}
	if len(operands) > 2 {
		return nil, fmt.Errorf("fc: 操作数过多")
	}
	if len(state.history) == 0 {
		if list {
			return nil, nil
		}
		return nil, fmt.Errorf("fc: 历史为空")
	}
	if list {
		first, last, err := resolveRange(state, operands, defaultListCount)
		if err != nil {
			return nil, err
		}
		entries := make([]historyEntry, 0, last-first+1)
		for i := first; i <= last; i++ {
			entries = append(entries, state.history[i-1])
		}
		numbers := make([]int, 0, len(entries))
		for number := first; number <= last; number++ {
			numbers = append(numbers, number)
		}
		if reverse {
			for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
				entries[i], entries[j] = entries[j], entries[i]
				numbers[i], numbers[j] = numbers[j], numbers[i]
			}
		}
		for index, entry := range entries {
			number := numbers[index]
			if suppressNumbers {
				if _, err := fmt.Fprintf(h.Stdout, "\t%s\n", entry.line()); err != nil {
					return nil, err
				}
				continue
			}
			if _, err := fmt.Fprintf(h.Stdout, "%d\t%s\n", number, entry.line()); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}

	index := len(state.history)
	if len(operands) > 0 {
		var err error
		index, err = resolveOperand(state, operands[0])
		if err != nil {
			return nil, err
		}
		if len(operands) == 2 {
			return nil, fmt.Errorf("fc: -s 只接受一个操作数")
		}
	}
	entry := state.history[index-1]
	words := append([]string(nil), entry.words...)
	for _, sub := range substitutions {
		for i, word := range words {
			words[i] = strings.ReplaceAll(word, sub[0], sub[1])
		}
	}
	if len(words) > 0 && (words[0] == "fc" || words[0] == "/bin/fc") {
		return nil, fmt.Errorf("fc: 不能重放 fc 自身")
	}
	return rewriteCommand(words)
}

func isNumber(text string) bool {
	_, err := strconv.Atoi(text)
	return err == nil
}

const defaultListCount = 16

func resolveRange(state *shellState, operands []string, count int) (int, int, error) {
	last := len(state.history)
	var first int
	if len(operands) == 0 {
		first = last - count + 1
		if first < 1 {
			first = 1
		}
		return first, last, nil
	}
	var err error
	first, err = resolveOperand(state, operands[0])
	if err != nil {
		return 0, 0, err
	}
	if len(operands) == 2 {
		last, err = resolveOperand(state, operands[1])
		if err != nil {
			return 0, 0, err
		}
	}
	if first > last {
		first, last = last, first
	}
	return first, last, nil
}

func resolveOperand(state *shellState, operand string) (int, error) {
	if value, err := strconv.Atoi(operand); err == nil {
		if value < 0 {
			value = len(state.history) + 1 + value
		}
		if value < 1 || value > len(state.history) {
			return 0, fmt.Errorf("fc: %s: 历史序号越界", operand)
		}
		return value, nil
	}
	for index := len(state.history); index >= 1; index-- {
		if words := state.history[index-1].words; len(words) > 0 && strings.HasPrefix(words[0], operand) {
			return index, nil
		}
	}
	return 0, fmt.Errorf("fc: %s: 没有匹配的历史条目", operand)
}

func rewriteCommand(words []string) ([]string, error) {
	if len(words) == 0 {
		return nil, fmt.Errorf("fc: 重放条目为空")
	}
	return words, nil
}
