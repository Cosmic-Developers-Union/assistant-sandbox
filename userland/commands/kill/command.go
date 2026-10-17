package killcmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/signal"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	list := false
	signalText := ""
	var operands []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--":
			operands = append(operands, args[index+1:]...)
			index = len(args)
		case arg == "-l" || arg == "--list":
			list = true
		case arg == "-s" || arg == "--signal":
			index++
			if index >= len(args) {
				return fmt.Errorf("kill: -s 缺少信号")
			}
			if signalText != "" {
				return fmt.Errorf("kill: 重复给出信号: %s", arg)
			}
			signalText = args[index]
		case strings.HasPrefix(arg, "--signal="):
			if signalText != "" {
				return fmt.Errorf("kill: 重复给出信号: %s", arg)
			}
			signalText = strings.TrimPrefix(arg, "--signal=")
		case len(arg) > 1 && arg[0] == '-':

			if signalText != "" {
				return fmt.Errorf("kill: 重复给出信号: %s", arg)
			}
			signalText = arg[1:]
		default:
			operands = append(operands, arg)
		}
	}
	if list {
		return runList(h, operands)
	}
	if len(operands) == 0 {
		return fmt.Errorf("kill: 缺少 pid 操作数")
	}
	n, err := parseSignal(signalText)
	if err != nil {
		return err
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	control, ok := p.Control.(proc.SignalControl)
	if !ok {
		return fmt.Errorf("kill: 客体没有信号投递能力")
	}
	failed := false
	for _, operand := range operands {
		pid, err := parsePID(operand)
		if err != nil {
			failed = true
			_, _ = fmt.Fprintf(h.Stderr, "kill: %v\n", err)
			continue
		}

		if n == 0 {
			exists, err := control.Exists(pid)
			if err != nil {
				failed = true
				_, _ = fmt.Fprintf(h.Stderr, "kill: %d: %v\n", pid, err)
				continue
			}
			if !exists {
				failed = true
				_, _ = fmt.Fprintf(h.Stderr, "kill: %d: %v\n", pid, errno.NoProcess)
			}
			continue
		}
		if err := control.Signal(pid, n); err != nil {
			failed = true
			_, _ = fmt.Fprintf(h.Stderr, "kill: %d: %v\n", pid, err)
		}
	}
	if failed {
		return interp.ExitStatus(1)
	}
	return nil
}

func runList(h interp.HandlerContext, operands []string) error {
	if len(operands) > 1 {
		return fmt.Errorf("kill: -l 只接受一个退出状态")
	}
	if len(operands) == 0 {
		for n := signal.Number(1); n <= signal.Max; n++ {
			if name := n.Name(); name != "" {
				if _, err := fmt.Fprintln(h.Stdout, name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	status, err := strconv.Atoi(operands[0])
	if err != nil || status < 0 {
		return fmt.Errorf("kill: -l 的退出状态无效: %s", operands[0])
	}

	value := status
	if value > int(signal.Max) {
		value -= 128
	}
	if value < 1 || value > int(signal.Max) {
		return fmt.Errorf("kill: %d 不是已知的退出状态", status)
	}
	number := signal.Number(value)
	if name := number.Name(); name != "" {
		_, err := fmt.Fprintln(h.Stdout, name)
		return err
	}
	return fmt.Errorf("kill: %d 不是已知的退出状态", status)
}

func parseSignal(text string) (signal.Number, error) {
	if text == "" {
		return signal.TERM, nil
	}
	if value, err := strconv.Atoi(text); err == nil {
		if value == 0 {
			return 0, nil
		}
		if value < 1 || value > int(signal.Max) {
			return 0, fmt.Errorf("kill: 信号编号越界: %s", text)
		}
		number := signal.Number(value)
		if !number.Valid() {
			return 0, fmt.Errorf("kill: 信号编号越界: %s", text)
		}
		return number, nil
	}
	name := strings.ToUpper(text)
	if !strings.HasPrefix(name, "SIG") {
		name = "SIG" + name
	}
	number, err := signal.ByName(name)
	if err != nil {
		return 0, fmt.Errorf("kill: 未知信号: %s", text)
	}
	return number, nil
}

func parsePID(text string) (proc.PID, error) {
	value, err := strconv.ParseUint(text, 10, 32)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("无效的 pid: %s", text)
	}
	return proc.PID(value), nil
}
