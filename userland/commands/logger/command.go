package logger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const logFile = "/var/log/messages"

func Run(ctx context.Context, args []string) (closeResult0 error) {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "logger")
	file := flags.StringP("file", "f", "", "从客体文件读取消息（每行一条）")
	priority := flags.StringP("priority", "p", "", "POSIX priority（数字 1-192 或 fac.level）")
	tag := flags.StringP("tag", "t", "", "行首标签（缺省 logger）")

	pidMark := flags.BoolP("pid", "i", false, "记录进程号（客体日志行没有进程号字段）")
	datagram := flags.BoolP("datagram", "d", false, "使用数据报（客体没有 syslog 套接字）")
	msgid := flags.BoolP("msgid", "m", false, "RFC 5424 msgid（客体日志行没有该字段）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	for _, rejected := range []struct {
		enabled bool
		name    string
	}{
		{*pidMark, "i"},
		{*datagram, "d"},
		{*msgid, "m"},
	} {
		if rejected.enabled {
			_, _ = fmt.Fprintf(h.Stderr, "logger: 客体没有 syslog 设施，-%s 不支持\n", rejected.name)
			return interp.ExitStatus(1)
		}
	}
	operands := flags.Args()
	if *file != "" && len(operands) > 0 {
		_, _ = fmt.Fprintln(h.Stderr, "logger: -f 与消息操作数不能同给")
		return interp.ExitStatus(2)
	}
	name := *tag
	if name == "" {
		name = "logger"
	}
	prefix := ""
	if *priority != "" {
		value, ok := parsePriority(*priority)
		if !ok {
			_, _ = fmt.Fprintf(h.Stderr, "logger: %s: 非法优先级\n", *priority)
			return interp.ExitStatus(1)
		}
		prefix = fmt.Sprintf("<%d>", value)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	if err := mkdirAll(ctx, system, path.Dir(logFile)); err != nil {
		return err
	}
	out, err := system.Open(ctx, logFile, vmfs.Create|vmfs.Append|vmfs.WriteOnly, 0o644)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "logger: 打开系统日志失败: %v\n", err)
		return interp.ExitStatus(1)
	}
	defer func() {
		closeResult0 = errors.Join(closeResult0, out.Close())
	}()

	write := func(message string) error {
		now, err := clock.Now(ctx)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s%s %s: %s\n", prefix, now.UTC().Format("Jan 2 15:04:05"), name, message)
		if _, err := io.WriteString(out, line); err != nil {
			return fmt.Errorf("写入系统日志失败: %w", err)
		}
		return nil
	}
	fail := func(err error) error {
		_, _ = fmt.Fprintf(h.Stderr, "logger: %v\n", err)
		return interp.ExitStatus(1)
	}
	if len(operands) > 0 {

		if err := write(strings.Join(operands, " ")); err != nil {
			return fail(err)
		}
		return nil
	}
	sources := []string{"-"}
	if *file != "" {
		sources = []string{*file}
	}
	return commandio.Lines(ctx, sources, func(_ string, line string, _ bool) (bool, error) {
		if err := write(line); err != nil {
			return false, fail(err)
		}
		return true, nil
	})
}

var facilities = map[string]int{
	"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4, "syslog": 5,
	"lpr": 6, "news": 7, "uucp": 8, "cron": 9, "authpriv": 10, "ftp": 11,
	"local0": 16, "local1": 17, "local2": 18, "local3": 19,
	"local4": 20, "local5": 21, "local6": 22, "local7": 23,
}

var levels = map[string]int{
	"emerg": 0, "panic": 0, "alert": 1, "crit": 2, "err": 3, "error": 3,
	"warning": 4, "warn": 4, "notice": 5, "info": 6, "debug": 7,
}

func parsePriority(text string) (int, bool) {
	if value, err := strconv.Atoi(text); err == nil {
		if value >= 1 && value <= 192 {
			return value, true
		}
		return 0, false
	}
	facility, level, found := strings.Cut(text, ".")
	if !found {
		return 0, false
	}
	f, ok := facilities[strings.ToLower(facility)]
	if !ok {
		return 0, false
	}
	l, ok := levels[strings.ToLower(level)]
	if !ok {
		return 0, false
	}
	return f*8 + l, true
}

func mkdirAll(ctx context.Context, system vmfs.System, name string) error {
	info, err := system.Stat(ctx, name)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("logger: %s: 不是目录", name)
		}
		return nil
	}
	if parent := path.Dir(name); parent != name {
		if err := mkdirAll(ctx, system, parent); err != nil {
			return err
		}
	}
	if err := system.Mkdir(ctx, name, 0o755); err != nil {

		if info, statErr := system.Stat(ctx, name); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}
	return nil
}
