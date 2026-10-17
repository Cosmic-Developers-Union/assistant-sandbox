package xargs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const guestArgMax = 128 << 10

const argMaxHeadroom = 2048

const lineMax = 2048

type options struct {
	null       bool
	noRunEmpty bool
	trace      bool
	maxArgs    int
	maxLines   int
	maxChars   int
	exactSize  bool
	replace    string
	replaceSet bool
	strictSize bool
	eof        string
	delim      string
}

func Run(ctx context.Context, args []string) error {
	o, command, err := parseOptions(ctx, args)
	if err != nil {
		return err
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	if p.Control == nil {
		return fmt.Errorf("执行 %s: 缺少进程控制端口", command[0])
	}
	h := commandio.Context(ctx)
	o, err = applySizeLimit(o, p.Env)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "xargs: %v\n", err)
		return interp.ExitStatus(1)
	}
	items, err := readItems(h.Stdin, o)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "xargs: %v\n", err)
		return interp.ExitStatus(1)
	}
	batches, err := plan(command, items, o)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "xargs: %v\n", err)
		return interp.ExitStatus(1)
	}

	code := execute(ctx, p, batches, o)
	if code == 0 {
		return nil
	}
	return interp.ExitStatus(code)
}

func parseOptions(ctx context.Context, args []string) (options, []string, error) {
	o := options{maxArgs: -1}
	posixMode := false
	if p, err := proc.FromContext(ctx); err == nil {
		_, posixMode = p.Env["POSIXLY_CORRECT"]
	}

	prefix := 0
	for prefix < len(args) {
		a := args[prefix]
		if a == "--" || a == "" || a[0] != '-' || a == "-" {
			break
		}
		prefix++
	}
	args = append(expandShortOptionClusters(args[:prefix]), args[prefix:]...)
	value := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("选项 %s 需要一个参数", name)
		}
		*i++
		return args[*i], nil
	}
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if a == "" || a[0] != '-' || a == "-" {
			break
		}
		switch {
		case a == "-0" || a == "--null":
			o.null = true
		case a == "-r" || a == "--no-run-if-empty":
			o.noRunEmpty = true
		case a == "-t" || a == "--verbose":
			o.trace = true
		case a == "-p" || a == "--interactive":
			return o, nil, fmt.Errorf("暂不支持 -p：客体没有控制终端（/dev/tty）可用来确认")
		case a == "-n" || a == "--max-args":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			o.maxArgs, o.maxLines, o.replace, o.replaceSet = atoiOr(v), 0, "", false
		case strings.HasPrefix(a, "--max-args="):
			o.maxArgs, o.maxLines, o.replace, o.replaceSet = atoiOr(a[len("--max-args="):]), 0, "", false
		case strings.HasPrefix(a, "-n") && len(a) > 2:
			o.maxArgs, o.maxLines, o.replace, o.replaceSet = atoiOr(a[2:]), 0, "", false
		case a == "-L":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			n, err := positiveInt(v, "-L")
			if err != nil {
				return o, nil, err
			}
			o.maxLines, o.maxArgs, o.replace, o.replaceSet = n, -1, "", false
		case strings.HasPrefix(a, "-L") && len(a) > 2:
			n, err := positiveInt(a[2:], "-L")
			if err != nil {
				return o, nil, err
			}
			o.maxLines, o.maxArgs, o.replace, o.replaceSet = n, -1, "", false
		case a == "-s":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			n, err := positiveInt(v, "-s")
			if err != nil {
				return o, nil, err
			}
			o.maxChars, o.strictSize = n, posixMode
		case strings.HasPrefix(a, "-s") && len(a) > 2:
			n, err := positiveInt(a[2:], "-s")
			if err != nil {
				return o, nil, err
			}
			o.maxChars, o.strictSize = n, posixMode
		case a == "-x":
			o.exactSize = true
		case a == "-I":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			o.replace, o.replaceSet, o.maxArgs, o.maxLines, o.exactSize = v, true, -1, 1, true
		case strings.HasPrefix(a, "-I") && len(a) > 2:
			o.replace, o.replaceSet, o.maxArgs, o.maxLines, o.exactSize = a[2:], true, -1, 1, true
		case a == "-i" || a == "--replace":
			o.replace, o.replaceSet, o.maxArgs, o.maxLines, o.exactSize = "{}", true, -1, 1, true
		case strings.HasPrefix(a, "--replace="):
			o.replace, o.replaceSet, o.maxArgs, o.maxLines, o.exactSize = a[len("--replace="):], true, -1, 1, true
		case a == "-E" || a == "--eof":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			o.eof = v
		case strings.HasPrefix(a, "--eof="):
			o.eof = a[len("--eof="):]
		case strings.HasPrefix(a, "-E") && len(a) > 2:
			o.eof = a[2:]
		case strings.HasPrefix(a, "-e"):
			o.eof = a[2:]
		case a == "-d" || a == "--delimiter":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			o.delim = v
		case strings.HasPrefix(a, "-d") && len(a) > 2:
			o.delim = a[2:]
		case strings.HasPrefix(a, "--delimiter="):
			o.delim = a[len("--delimiter="):]
		case a == "-P" || a == "--max-procs":
			v, err := value(&i, a)
			if err != nil {
				return o, nil, err
			}
			if atoiOr(v) != 1 {
				return o, nil, fmt.Errorf("暂不支持 -P：客体按确定性调度顺序执行，只接受 -P 1")
			}
		case strings.HasPrefix(a, "-P") && len(a) > 2:
			if atoiOr(a[2:]) != 1 {
				return o, nil, fmt.Errorf("暂不支持 -P：客体按确定性调度顺序执行，只接受 -P 1")
			}
		case strings.HasPrefix(a, "--max-procs="):
			if atoiOr(a[len("--max-procs="):]) != 1 {
				return o, nil, fmt.Errorf("暂不支持 -P：客体按确定性调度顺序执行，只接受 -P 1")
			}
		default:
			return o, nil, fmt.Errorf("未知选项 %q", a)
		}
	}
	if o.maxArgs != -1 && o.maxArgs < 1 {
		return o, nil, fmt.Errorf("-n 需要一个正数")
	}
	if o.maxLines < 0 {
		return o, nil, fmt.Errorf("-L 需要一个正数")
	}
	command := args[i:]
	if len(command) == 0 {
		command = []string{"echo"}
	}
	return o, command, nil
}

func atoiOr(s string) int {
	n := 0
	negative := false
	for i, c := range s {
		if i == 0 && (c == '-' || c == '+') {
			negative = c == '-'
			continue
		}
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	if negative {
		return -n
	}
	return n
}

func execute(ctx context.Context, p proc.Proc, batches [][]string, o options) uint8 {
	if len(batches) == 0 {
		return 0
	}
	h := commandio.Context(ctx)
	code := uint8(0)
	for _, argv := range batches {
		if o.trace {
			_, _ = fmt.Fprintln(h.Stderr, strings.Join(argv, " "))
		}

		terminal, err := p.Control.Run(ctx, proc.RunSpec{
			Parent: p.ID, Name: argv[0], Args: argv[1:], Dir: p.Dir, Env: environmentOf(p),
			Streams: fd.Streams{Input: strings.NewReader(""), Output: h.Stdout, Error: h.Stderr},
		})
		if err != nil {
			switch {
			case errors.Is(err, errno.NotFound):
				_, _ = fmt.Fprintf(h.Stderr, "xargs: %s: 未注册沙箱命令\n", argv[0])
				code = max(code, 127)
			case errors.Is(err, errno.NoExec), errors.Is(err, errno.Permission):
				_, _ = fmt.Fprintf(h.Stderr, "xargs: %s: 无法执行\n", argv[0])
				code = max(code, 126)
			default:
				_, _ = fmt.Fprintf(h.Stderr, "xargs: %s: %v\n", argv[0], err)
				code = max(code, 1)
			}
			continue
		}
		code = max(code, mappedStatus(terminal))
	}
	return code
}

func mappedStatus(terminal proc.Proc) uint8 {
	switch {
	case terminal.ExitSignal != 0:
		return 125
	case terminal.ExitCode == 255:
		return 124
	case terminal.ExitCode >= 1 && terminal.ExitCode <= 125:
		return 123
	}
	return 0
}

func environmentOf(p proc.Proc) map[string]string { return p.Env }

func expandShortOptionClusters(args []string) []string {
	const valueTaking = "nLsIEdPe"
	out := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) < 3 || a[0] != '-' || a[1] == '-' {
			out = append(out, a)
			continue
		}
		for i := 1; i < len(a); i++ {
			if strings.ContainsRune(valueTaking, rune(a[i])) {
				out = append(out, "-"+string(a[i])+a[i+1:])
				break
			}
			out = append(out, "-"+string(a[i]))
		}
	}
	return out
}

func positiveInt(text, name string) (int, error) {
	if text == "" {
		return 0, fmt.Errorf("选项 %s 需要一个正数", name)
	}
	n := 0
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("选项 %s 需要一个正数", name)
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 {
		return 0, fmt.Errorf("选项 %s 需要一个正数", name)
	}
	return n, nil
}

func applySizeLimit(o options, environment map[string]string) (options, error) {
	maxSupported := guestArgMax - environmentSize(environment) - argMaxHeadroom
	if maxSupported <= 0 {
		return o, fmt.Errorf("环境太大，装不下任何命令行")
	}
	switch {
	case o.maxChars == 0:

		o.maxChars = maxSupported
		if o.maxArgs > 0 && o.maxChars > lineMax {
			o.maxChars = lineMax
		}
	case o.maxChars > maxSupported:
		o.maxChars = maxSupported
		o.strictSize = false
	}
	return o, nil
}
