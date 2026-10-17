package mailx

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const inboxPath = "/var/mail/"

const mailxTemp = ".mailx-tmp"

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "mailx")
	flags.SetInterspersed(false)
	flags.StringP("subject", "s", "", "主题")
	exist := flags.BoolP("exist", "e", false, "只检查是否有邮件")
	fileMode := flags.BoolP("file", "f", false, "读取指定信箱")

	userOpt := flags.BoolP("user", "u", false, "不支持（声明拒绝）")
	ignoreOpt := flags.BoolP("ignore", "i", false, "不支持（声明拒绝）")
	norcOpt := flags.BoolP("norc", "n", false, "不支持（声明拒绝）")
	noheadersOpt := flags.BoolP("noheaders", "N", false, "不支持（声明拒绝）")
	recordOpt := flags.BoolP("record", "F", false, "不支持（声明拒绝）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	for _, bad := range []struct {
		short string
		set   *bool
	}{
		{"u", userOpt}, {"i", ignoreOpt}, {"n", norcOpt}, {"N", noheadersOpt}, {"F", recordOpt},
	} {
		if *bad.set {
			_, _ = fmt.Fprintf(h.Stderr, "mailx: 不支持的选项: -%s\n", bad.short)
			return interp.ExitStatus(2)
		}
	}
	operands := flags.Args()
	switch {
	case *exist:
		return checkOnly(ctx, operands, *fileMode)
	case *fileMode:
		if len(operands) > 1 {
			_, _ = fmt.Fprintln(h.Stderr, "mailx: -f 至多一个信箱操作数")
			return interp.ExitStatus(2)
		}
		return readMailboxPath(ctx, operands, *fileMode)
	case len(operands) > 0:
		return send(ctx, operands)
	default:
		return readMailboxPath(ctx, nil, false)
	}
}

func procUserLookup(ctx context.Context) (string, error) {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return "", err
	}
	source, err := p.Resources.Credentials()
	if err != nil {
		return "", err
	}
	who, err := source.Lookup(ctx, uint64(p.ID))
	if err != nil {
		return "", err
	}
	return who.Name, nil
}

func mailboxTarget(ctx context.Context, operands []string, explicit bool) (string, error) {
	if explicit && len(operands) == 1 {
		return operands[0], nil
	}
	user, err := procUserLookup(ctx)
	if err != nil {
		return "", err
	}
	return inboxPath + user, nil
}

func checkOnly(ctx context.Context, operands []string, fileMode bool) error {
	path, err := mailboxTarget(ctx, operands, fileMode)
	if err != nil {
		return err
	}
	messages, err := loadMailbox(ctx, path, false)
	if err != nil || len(messages) == 0 {
		return interp.ExitStatus(1)
	}
	return nil
}

func resolveMailboxPath(ctx context.Context, name string) string {
	if path.IsAbs(name) {
		return strings.TrimPrefix(name, "/")
	}
	return commandio.Path(commandio.Directory(ctx), name)
}

func loadMailbox(ctx context.Context, mailbox string, wantDiagnostic bool) (closeResult0 []*message, closeResult1 error) {
	file, err := commandio.Open(ctx, resolveMailboxPath(ctx, mailbox), vmfs.ReadOnly, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeResult1 = errors.Join(closeResult1, file.Close())
	}()

	data, err := commandio.ReadText(ctx, file)
	if err != nil {
		return nil, err
	}
	messages, skipped := parseMailbox(data)
	if skipped > 0 && wantDiagnostic {
		_, _ = fmt.Fprintf(commandio.Context(ctx).Stderr, "mailx: %d 条消息解析失败，已跳过\n", skipped)
	}
	return messages, nil
}

func readMailboxPath(ctx context.Context, operands []string, fileMode bool) error {
	h := commandio.Context(ctx)
	user, err := procUserLookup(ctx)
	if err != nil {
		return err
	}
	path, err := mailboxTarget(ctx, operands, fileMode)
	if err != nil {
		return err
	}
	messages, err := loadMailbox(ctx, path, true)
	if err != nil || len(messages) == 0 {
		_, _ = fmt.Fprintf(h.Stderr, "mailx: No mail for %s\n", user)
		return interp.ExitStatus(1)
	}
	session := &reader{messages: messages, path: path}
	return session.loop(ctx)
}

type reader struct {
	messages   []*message
	path       string
	current    int
	failed     bool
	saveOnExit bool
}

func (r *reader) loop(ctx context.Context) error {
	h := commandio.Context(ctx)
	input := bufio.NewReader(h.Stdin)
	for {
		line, err := input.ReadString('\n')

		if text := strings.TrimSpace(line); text != "" || err == nil {
			if quit := r.command(ctx, text); quit {
				if r.saveOnExit {
					return r.quit(ctx)
				}
				if r.failed {
					return interp.ExitStatus(1)
				}
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {

				r.saveOnExit = true
				return r.quit(ctx)
			}
			return err
		}
	}
}

func (r *reader) command(ctx context.Context, text string) bool {
	h := commandio.Context(ctx)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		r.printCurrent(h)
		return false
	}
	switch fields[0] {
	default:
		if number, err := strconv.Atoi(fields[0]); err == nil {
			r.gotoMessage(h, number)
			return false
		}
		r.fail(h, fmt.Sprintf("未知命令: %s", fields[0]))
	case "p":
		r.printCurrent(h)
	case "+":
		r.stepMessage(h, 1)
	case "-":
		r.stepMessage(h, -1)
	case "h":
		r.printHeaders(h)
	case "d":
		if len(fields) > 1 {
			number, err := strconv.Atoi(fields[1])
			if err != nil || number < 1 || number > len(r.messages) {
				r.fail(h, "删除的消息号无效")
				return false
			}
			r.messages[number-1].deleted = true
			return false
		}

		r.messages[r.current].deleted = true
		if r.current+1 < len(r.messages) {
			r.current++
		}
	case "s", "w":
		if len(fields) != 2 {
			r.fail(h, fmt.Sprintf("%s 需要一个文件操作数", fields[0]))
			return false
		}
		r.save(ctx, h, fields[1], fields[0] == "s")
	case "q":

		r.saveOnExit = true
		return true
	case "x":

		return true
	}
	return false
}

func (r *reader) fail(h interp.HandlerContext, detail string) {
	r.failed = true
	_, _ = fmt.Fprintf(h.Stderr, "mailx: %s\n", detail)
}

func (r *reader) gotoMessage(h interp.HandlerContext, number int) {
	if number < 1 || number > len(r.messages) {
		r.fail(h, "消息号超出范围")
		return
	}
	r.current = number - 1
	r.printCurrent(h)
}

func (r *reader) stepMessage(h interp.HandlerContext, delta int) {
	next := r.current + delta
	if next < 0 || next >= len(r.messages) {
		r.fail(h, "没有更多消息")
		return
	}
	r.current = next
	r.printCurrent(h)
}

func (r *reader) printCurrent(h interp.HandlerContext) {
	msg := r.messages[r.current]
	_, _ = fmt.Fprintf(h.Stdout, "Message %d:\n", r.current+1)
	_, _ = fmt.Fprintf(h.Stdout, "From: %s\n", msg.sender())
	if subject := msg.subject(); subject != "" {
		_, _ = fmt.Fprintf(h.Stdout, "Subject: %s\n", subject)
	}
	_, _ = fmt.Fprintln(h.Stdout)
	body := msg.body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if _, err := io.WriteString(h.Stdout, body); err != nil {
		r.fail(h, err.Error())
	}
}

func (r *reader) printHeaders(h interp.HandlerContext) {
	for i, msg := range r.messages {
		marker := "  "
		if i == r.current {
			marker = "> "
		}
		deleted := ""
		if msg.deleted {
			deleted = "*"
		}
		_, _ = fmt.Fprintf(h.Stdout, "%s%3d %s %s%s\n", marker, i+1, msg.sender(), msg.subject(), deleted)
	}
}

func (r *reader) save(ctx context.Context, h interp.HandlerContext, name string, withHeaders bool) {
	msg := r.messages[r.current]
	data := msg.bodyText()
	if withHeaders {
		data = msg.mboxText()
	}
	flags := vmfs.Create | vmfs.WriteOnly | vmfs.Append
	file, err := commandio.Open(ctx, commandio.Path(h.Dir, name), flags, 0o666)
	if err != nil {
		r.fail(h, fmt.Sprintf("无法打开 %s: %v", name, err))
		return
	}
	if _, err := io.WriteString(file, data); err != nil {
		err = errors.Join(err, file.Close())
		r.fail(h, fmt.Sprintf("无法写入 %s: %v", name, err))
		return
	}
	if err := file.Close(); err != nil {
		r.fail(h, fmt.Sprintf("无法写入 %s: %v", name, err))
	}
}

func (r *reader) quit(ctx context.Context) error {
	h := commandio.Context(ctx)
	kept, deleted := 0, 0
	var data strings.Builder
	for _, msg := range r.messages {
		if msg.deleted {
			deleted++
			continue
		}
		kept++
		data.WriteString(msg.mboxText())
	}
	if deleted > 0 {
		tmp := r.path + mailxTemp
		file, err := commandio.Open(ctx, resolveMailboxPath(ctx, tmp), vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, 0o666)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "mailx: 删除落盘失败: %v\n", err)
			return interp.ExitStatus(1)
		}
		if _, err := io.WriteString(file, data.String()); err != nil {
			err = errors.Join(err, file.Close())
			_, _ = fmt.Fprintf(h.Stderr, "mailx: 删除落盘失败: %v\n", err)
			return interp.ExitStatus(1)
		}
		if err := file.Close(); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "mailx: 删除落盘失败: %v\n", err)
			return interp.ExitStatus(1)
		}
		system, err := commandio.Filesystem(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "mailx: 删除落盘失败: %v\n", err)
			return interp.ExitStatus(1)
		}
		if err := system.Rename(ctx, resolveMailboxPath(ctx, tmp), resolveMailboxPath(ctx, r.path)); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "mailx: 删除落盘失败: %v\n", err)
			return interp.ExitStatus(1)
		}
	}

	_, _ = fmt.Fprintf(h.Stdout, "保留 %d 条消息，删除 %d 条\n", kept, deleted)
	if r.failed {
		return interp.ExitStatus(1)
	}
	return nil
}

func send(ctx context.Context, operands []string) error {
	h := commandio.Context(ctx)
	addresses := splitAddresses(operands)
	if len(addresses) == 0 {
		_, _ = fmt.Fprintln(h.Stderr, "mailx: 缺少收件人")
		return interp.ExitStatus(2)
	}
	body, err := commandio.ReadText(ctx, h.Stdin)
	if err != nil {
		return err
	}
	dead := commandio.Path(h.Dir, "dead.letter")
	file, err := commandio.Open(ctx, dead, vmfs.Create|vmfs.WriteOnly|vmfs.Append, 0o666)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "mailx: 无法写入 dead.letter: %v\n", err)
		return interp.ExitStatus(1)
	}
	if _, err := io.WriteString(file, body); err != nil {
		err = errors.Join(err, file.Close())
		_, _ = fmt.Fprintf(h.Stderr, "mailx: 无法写入 dead.letter: %v\n", err)
		return interp.ExitStatus(1)
	}
	if err := file.Close(); err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "mailx: 无法写入 dead.letter: %v\n", err)
		return interp.ExitStatus(1)
	}
	_, _ = fmt.Fprintln(h.Stderr, "mailx: 客体没有邮件传输设施，正文已存 dead.letter")
	return interp.ExitStatus(1)
}

func splitAddresses(items []string) []string {
	var out []string
	for _, item := range items {
		for _, part := range strings.FieldsFunc(item, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}
