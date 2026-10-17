package at

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const (
	queueAt    = 'a'
	queueBatch = 'b'
)

var timeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"15:04:05",
	"15:04",
}

func Run(ctx context.Context, args []string) error {
	return run(ctx, args, false, "at")
}

func RunBatch(ctx context.Context, args []string) error {
	return run(ctx, args, true, "batch")
}

func run(ctx context.Context, args []string, batch bool, name string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, name)
	file := flags.StringP("file", "f", "", "从客体文件读取作业脚本")
	queue := flags.StringP("queue", "q", "a", "作业队列（a=at，b=batch）")
	mail := flags.BoolP("mail", "m", false, "作业完成后发送邮件（无输出也发）")
	list := flags.BoolP("list", "l", false, "列出排队作业（atq 惯例）")
	remove := flags.BoolP("remove", "r", false, "删除作业（atrm 惯例；-d 同义）")
	delete := flags.BoolP("delete", "d", false, "删除作业（atrm 惯例）")
	cat := flags.BoolP("cat", "c", false, "串接显示作业脚本")
	_ = flags.BoolP("interactive", "i", false, "删除前确认（非交互，与 -r 同用）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}

	if *remove || *delete {
		return removeJobs(ctx, operands)
	}

	if *list {
		return listJobs(ctx, operands)
	}

	if *cat {
		return catJobs(ctx, operands)
	}

	queueByte := byte(queueAt)
	switch {
	case batch || *queue == "b" || *queue == "batch":
		queueByte = queueBatch
	case *queue != "a":
		_, _ = fmt.Fprintf(h.Stderr, "%s: 队列 %q 不是 a 或 b\n", name, *queue)
		return interp.ExitStatus(2)
	}
	script, err := readScript(ctx, *file)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}
	if len(operands) == 0 {
		_, _ = fmt.Fprintf(h.Stderr, "%s: 需要时间操作数\n", name)
		return interp.ExitStatus(2)
	}
	now, err := clock.Now(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: 客体没有时钟能力: %v\n", err)
		return interp.ExitStatus(1)
	}
	nowNano := now.UnixNano()
	deadline, err := parseTime(strings.Join(operands, " "), nowNano)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(2)
	}
	if deadline < 0 {
		_, _ = fmt.Fprintln(h.Stderr, "at: 时间早于客体时钟范围")
		return interp.ExitStatus(2)
	}
	spec := proc.JobSpec{
		Queue:    queueByte,
		Deadline: uint64(deadline),
		Dir:      h.Dir,
		Env:      envSnapshot(p),
		Mail:     *mail,
		Script:   script,
	}

	if control, ok := p.Control.(proc.PriorityControl); ok {
		if current, err := control.Nice(p.ID); err == nil {
			spec.Nice = current
		}
	}
	scheduler, err := schedulerFrom(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}
	id, err := scheduler.QueueJob(ctx, spec)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}

	_, _ = fmt.Fprintf(h.Stdout, "job %s at %s\n", id, clock.Unix(deadline, 0).Format("2006-01-02 15:04:05 UTC"))
	return nil
}

func schedulerFrom(ctx context.Context) (proc.Scheduler, error) {
	p, err := proc.FromContext(ctx)
	if err != nil {
		return nil, err
	}
	if scheduler, ok := p.Control.(proc.Scheduler); ok {
		return scheduler, nil
	}
	return nil, fmt.Errorf("客体没有常驻调度器: %w", errno.NotImplemented)
}

func listJobs(ctx context.Context, operands []string) error {
	h := commandio.Context(ctx)
	scheduler, err := schedulerFrom(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}
	jobs, err := scheduler.ListJobs(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}
	for _, job := range jobs {
		if len(operands) > 0 && !contains(operands, job) {
			continue
		}
		if _, err := fmt.Fprintln(h.Stdout, job); err != nil {
			return err
		}
	}
	return nil
}

func removeJobs(ctx context.Context, operands []string) error {
	h := commandio.Context(ctx)
	if len(operands) == 0 {
		if _, err := fmt.Fprintln(h.Stderr, "at: 需要作业号"); err != nil {
			return err
		}
		return interp.ExitStatus(2)
	}
	scheduler, err := schedulerFrom(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}
	failed := false
	for _, operand := range operands {
		removed, err := scheduler.RemoveJob(ctx, operand)
		if err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
			failed = true
			continue
		}
		if !removed {
			_, _ = fmt.Fprintf(h.Stderr, "at: %s: 没有这个作业\n", operand)
			failed = true
		}
	}
	if failed {
		return interp.ExitStatus(1)
	}
	return nil
}

func catJobs(ctx context.Context, operands []string) error {
	h := commandio.Context(ctx)
	if len(operands) == 0 {
		if _, err := fmt.Fprintln(h.Stderr, "at: 需要作业号"); err != nil {
			return err
		}
		return interp.ExitStatus(2)
	}
	scheduler, err := schedulerFrom(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", err)
		return interp.ExitStatus(1)
	}
	failed := false
	for _, operand := range operands {
		jobs, listErr := scheduler.ListJobs(ctx)
		if listErr != nil || !contains(jobs, operand) {
			_, _ = fmt.Fprintf(h.Stderr, "at: %s: 没有这个作业\n", operand)
			failed = true
			continue
		}
		data, readErr := scheduler.ReadJob(ctx, operand)
		if readErr != nil {
			_, _ = fmt.Fprintf(h.Stderr, "at: %v\n", readErr)
			failed = true
			continue
		}
		_, _ = fmt.Fprint(h.Stdout, data)
	}
	if failed {
		return interp.ExitStatus(1)
	}
	return nil
}

func readScript(ctx context.Context, file string) (script string, err error) {
	if file != "" {
		system, err := commandio.Filesystem(ctx)
		if err != nil {
			return "", err
		}
		handle, err := system.Open(ctx, commandio.Path(commandio.Context(ctx).Dir, file), vmfs.ReadOnly, 0)
		if err != nil {
			return "", fmt.Errorf("%s: 读不到", file)
		}
		defer func() {
			err = errors.Join(err, handle.Close())
		}()
		data, err := io.ReadAll(handle)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := io.ReadAll(commandio.Context(ctx).Stdin)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func parseTime(text string, nowNano int64) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, fmt.Errorf("时间为空")
	}
	if strings.HasPrefix(text, "now") {
		rest := strings.TrimSpace(strings.TrimPrefix(text, "now"))
		if rest == "" {
			return nowNano, nil
		}
		if plus, ok := strings.CutPrefix(rest, "+"); ok {
			return relative(strings.TrimSpace(plus), nowNano)
		}
		return 0, fmt.Errorf("时间 %q 无效", text)
	}

	if value, err := relative(text, nowNano); err == nil {
		return value, nil
	}

	for _, layout := range timeLayouts {
		if parsed, err := clock.Parse(layout, text); err == nil {
			deadline := parsed.UnixNano()

			if layout == "15:04" || layout == "15:04:05" {
				for deadline <= nowNano {
					deadline += 86400_000_000_000
				}
			}
			return deadline, nil
		}
	}
	return 0, fmt.Errorf("时间 %q 无法解析（客体只接受 HH:MM、YYYY-MM-DD [HH:MM[:SS]] 与相对形式）", text)
}

func relative(text string, nowNano int64) (int64, error) {
	fields := strings.Fields(text)
	if len(fields) != 2 {
		return 0, fmt.Errorf("相对时间 %q 应为「N 单位」", text)
	}
	var count int64
	if _, err := fmt.Sscanf(fields[0], "%d", &count); err != nil || count <= 0 {
		return 0, fmt.Errorf("相对时间 %q 的数量无效", text)
	}
	var unit int64
	switch singular(fields[1]) {
	case "minute":
		unit = 60_000_000_000
	case "hour":
		unit = 3600_000_000_000
	case "day":
		unit = 86400_000_000_000
	case "week":
		unit = 7 * 86400_000_000_000
	default:
		return 0, fmt.Errorf("相对时间 %q 的单位无效", text)
	}
	return nowNano + count*unit, nil
}

func singular(word string) string {
	return strings.TrimSuffix(word, "s")
}

func envSnapshot(p proc.Proc) map[string]string {
	env := map[string]string{}
	for key, value := range p.Env {
		env[key] = value
	}
	return env
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

var _ = errno.Invalid
