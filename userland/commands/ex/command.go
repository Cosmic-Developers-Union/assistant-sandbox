package ex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/internal/editor"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "ex")
	flags.SetInterspersed(false)
	silent := flags.BoolP("silent", "s", false, "批处理：不写提示与交互消息")
	commandMode := flags.StringP("command", "c", "", "启动后先执行一条命令")
	visual := flags.BoolP("visual", "v", false, "屏幕编辑模式")
	recover := flags.BoolP("recover", "r", false, "从恢复文件恢复会话")
	readonly := flags.BoolP("readonly", "R", false, "只读模式")
	window := flags.StringP("window", "w", "", "窗口尺寸（行数）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}

	reject := func(what string) error {
		_, _ = fmt.Fprintf(h.Stderr, "ex: 不支持%s（客体没有对应设施）\n", what)
		return interp.ExitStatus(1)
	}
	if *visual {
		return reject("屏幕编辑模式（-v）")
	}
	if *recover {
		return reject("恢复模式（-r）")
	}
	if *readonly {
		return reject("只读模式（-R）")
	}
	if flags.Changed("window") {
		return reject("窗口尺寸（-w " + *window + "）")
	}
	operands := flags.Args()
	if len(operands) > 1 {
		_, _ = fmt.Fprintln(h.Stderr, "ex: 至多一个文件操作数")
		return interp.ExitStatus(2)
	}
	eng := &editor.Engine{
		Out: h.Stdout, Diag: h.Stderr,
		Silent: *silent, Prompt: ":",
		Files: guestFiles(ctx),

		ReportError: func(detail string) { _, _ = fmt.Fprintf(h.Stderr, "ex: %s\n", detail) },
	}
	if len(operands) == 1 {
		if _, err := eng.Load(operands[0]); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "ex: %v\n", err)
			eng.Buffer.Reset(nil, false)
			eng.Filename = operands[0]
		}
	}
	eng.Start(h.Stdin)
	if cmd := strings.TrimSpace(*commandMode); cmd != "" {
		if eng.Execute(cmd) {
			return finish(eng)
		}
	}
	for {
		line, err := eng.NextLine(!*silent)
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {

				if eng.Buffer.Dirty {
					_, _ = fmt.Fprintln(h.Stderr, "ex: 上次完整写出之后缓冲已修改（q! 放弃修改）")
				}
				break
			}
			eng.Fail(err.Error())
			break
		}
		if handled, quit := exCommand(eng, line); handled {
			if quit {
				break
			}
			continue
		}
		if eng.Execute(line) {
			break
		}
	}
	return finish(eng)
}

func finish(eng *editor.Engine) error {
	if eng.HadError() {
		return interp.ExitStatus(1)
	}
	return nil
}

func exCommand(eng *editor.Engine, line string) (handled, quit bool) {
	word, rest := splitWord(line)
	switch word {
	case "q!":

		return true, eng.Execute("Q")
	case "wq!":

		eng.Execute("w")
		return true, true
	case "visual", "vi":
		eng.Fail("客体终端是字节流（vm/tty），没有光标寻址与屏幕模型，屏幕编辑器的绘制与按键协议无法成立")
		return true, false
	case "set":
		setOptions(eng, rest)
		return true, false
	case "args":

		if eng.Filename != "" {
			_, _ = fmt.Fprintf(eng.Out, "[%s]\n", eng.Filename)
		}
		return true, false
	case "next":
		eng.Fail("没有更多要编辑的文件")
		return true, false
	case "rewind":

		if eng.Filename == "" {
			eng.Fail("没有当前文件名")
			return true, false
		}
		return true, eng.Execute("e " + eng.Filename)
	case "file":
		if rest != "" {

			eng.Execute("f " + rest)
			return true, false
		}
		if eng.Filename == "" {
			eng.Fail("没有当前文件名")
			return true, false
		}
		fileStatus(eng)
		return true, false
	}
	return false, false
}

func fileStatus(eng *editor.Engine) {
	last := eng.Buffer.Last()
	pct := 0
	if last > 0 {
		pct = (eng.Buffer.Current - 1) * 100 / last
	}
	modified := ""
	if eng.Buffer.Dirty {
		modified = " [modified]"
	}
	_, _ = fmt.Fprintf(eng.Out, "\"%s\"%s line %d of %d --%d%%--\n",
		eng.Filename, modified, eng.Buffer.Current, last, pct)
}

func setOptions(eng *editor.Engine, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		writeChangedOptions(eng)
		return
	}
	for _, field := range fields {
		query := false
		enable := true
		name := field
		if strings.HasSuffix(name, "?") {
			query = true
			name = strings.TrimSuffix(name, "?")
		} else if strings.HasPrefix(name, "no") {
			enable = false
			name = strings.TrimPrefix(name, "no")
		}
		switch name {
		case "number", "nu":
			if !query {
				eng.Number = enable
			}
			reportOption(eng, "number", eng.Number)
		case "list":
			if !query {
				eng.List = enable
			}
			reportOption(eng, "list", eng.List)
		default:
			eng.Fail(fmt.Sprintf("不支持的 set 选项: %s", field))
			return
		}
	}
}

func reportOption(eng *editor.Engine, name string, enabled bool) {
	if enabled {
		_, _ = fmt.Fprintln(eng.Out, name)
	} else {
		_, _ = fmt.Fprintln(eng.Out, "no"+name)
	}
}

func writeChangedOptions(eng *editor.Engine) {
	if eng.Number {
		_, _ = fmt.Fprintln(eng.Out, "number")
	}
	if eng.List {
		_, _ = fmt.Fprintln(eng.Out, "list")
	}
}

func splitWord(line string) (string, string) {
	line = strings.TrimLeft(line, " \t")
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		return line[:i], strings.TrimLeft(line[i+1:], " \t")
	}
	return line, ""
}

func guestFiles(ctx context.Context) editor.Files {
	return editor.Files{
		Read: func(name string) (closeResult0 string, closeResult1 error) {
			file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.ReadOnly, 0)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			defer func() {
				closeResult1 = errors.Join(closeResult1, file.Close())
			}()

			data, err := commandio.ReadText(ctx, file)
			if err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			return data, nil
		},
		Write: func(name string, data string) error {
			file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, 0o666)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if _, err := io.WriteString(file, data); err != nil {
				err = errors.Join(err, file.Close())
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		},
		Append: func(name string, data string) error {
			file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.Create|vmfs.WriteOnly|vmfs.Append, 0o666)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if _, err := io.WriteString(file, data); err != nil {
				err = errors.Join(err, file.Close())
				return fmt.Errorf("%s: %w", name, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		},
	}
}
