package renice

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "renice")
	increment := flags.StringP("increment", "n", "", "优先级增量（相对当前值）")
	byPID := flags.BoolP("pid", "p", false, "按进程 ID 解释操作数（缺省）")
	byPGID := flags.BoolP("pgrp", "g", false, "按进程组 ID 解释操作数")
	byUser := flags.BoolP("user", "u", false, "按用户解释操作数")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	control, ok := p.Control.(proc.PriorityControl)
	if !ok {
		_, _ = fmt.Fprintln(h.Stderr, "renice: 客体没有优先级控制能力")
		return interp.ExitStatus(1)
	}
	selected := 0
	for _, enabled := range []*bool{byPID, byPGID, byUser} {
		if *enabled {
			selected++
		}
	}
	if selected > 1 {
		_, _ = fmt.Fprintln(h.Stderr, "renice: -p/-g/-u 互斥")
		return interp.ExitStatus(2)
	}

	absolute := false
	if *increment == "" && len(operands) > 0 && isInteger(operands[0]) {
		*increment = operands[0]
		absolute = true
		operands = operands[1:]
	}
	if *increment == "" || len(operands) == 0 {
		_, _ = fmt.Fprintln(h.Stderr, "renice: 需要 -n 增量与至少一个操作数")
		return interp.ExitStatus(2)
	}
	adjust, err := strconv.Atoi(strings.TrimSpace(*increment))
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "renice: %q 不是整数\n", *increment)
		return interp.ExitStatus(2)
	}
	targets, err := resolveTargets(p, control, operands, selection{*byPID, *byPGID, *byUser})
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "renice: %v\n", err)
		return interp.ExitStatus(1)
	}
	if len(targets) == 0 {
		_, _ = fmt.Fprintln(h.Stderr, "renice: 没有匹配的目标进程")
		return interp.ExitStatus(1)
	}
	failed := false
	for _, target := range targets {
		newNice := adjust
		if !absolute {
			current, err := control.Nice(target)
			if err != nil {
				_, _ = fmt.Fprintf(h.Stderr, "renice: %d: %v\n", target, err)
				failed = true
				continue
			}
			newNice = current + adjust
		}
		if err := control.SetNice(target, newNice); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "renice: %d: %v\n", target, err)
			failed = true
		}
	}
	if failed {
		return interp.ExitStatus(1)
	}
	return nil
}

type selection struct{ pid, pgid, user bool }

func resolveTargets(caller proc.Proc, control proc.PriorityControl, operands []string, mode selection) ([]proc.PID, error) {
	switch {
	case mode.user:
		for _, name := range operands {
			if name != "sandbox" && name != "0" && name != "root" {
				return nil, fmt.Errorf("%s: 没有这个用户", name)
			}
		}
		table, ok := caller.Control.(proc.TableControl)
		if !ok {
			return nil, fmt.Errorf("客体没有进程表能力")
		}
		return allPIDs(table), nil
	case mode.pgid:
		table, ok := caller.Control.(proc.TableControl)
		if !ok {
			return nil, fmt.Errorf("客体没有进程表能力")
		}
		var out []proc.PID
		for _, text := range operands {
			pgid, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: 不是进程组 ID", text)
			}
			members := membersOf(table, proc.PID(pgid))
			if len(members) == 0 {
				return nil, fmt.Errorf("%s: 没有这个进程组", text)
			}
			out = append(out, members...)
		}
		return dedupe(out), nil
	default:
		var out []proc.PID
		for _, text := range operands {
			pid, err := strconv.ParseUint(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: 不是进程 ID", text)
			}

			if _, err := control.Nice(proc.PID(pid)); err != nil {
				return nil, fmt.Errorf("%s: %w", text, errno.NoProcess)
			}
			out = append(out, proc.PID(pid))
		}
		return dedupe(out), nil
	}
}

func allPIDs(table proc.TableControl) []proc.PID {
	var out []proc.PID
	for _, p := range table.Processes() {
		out = append(out, p.ID)
	}
	return dedupe(out)
}

func membersOf(table proc.TableControl, pgid proc.PID) []proc.PID {
	var out []proc.PID
	for _, p := range table.Processes() {
		if p.PGID == pgid {
			out = append(out, p.ID)
		}
	}
	return out
}

func dedupe(pids []proc.PID) []proc.PID {
	slices.Sort(pids)
	return slices.Compact(pids)
}

func isInteger(text string) bool {
	_, err := strconv.Atoi(strings.TrimSpace(text))
	return err == nil
}
