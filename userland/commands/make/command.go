package make

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

var (
	errFailure   = errors.New("make 失败")
	errOutOfDate = errors.New("目标需要重建")
)

type options struct {
	envOverride, keep, dry, question, silent bool
}

type engine struct {
	ctx    context.Context
	h      interp.HandlerContext
	p      proc.Proc
	o      options
	m      *makefile
	dir    string
	shell  string
	env    map[string]string
	system commandioFilesystem

	visiting, done, made map[string]bool
	failed               bool
	action               bool
}

type commandioFilesystem interface {
	Stat(ctx context.Context, name string) (fs.FileInfo, error)
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "make")

	flags.SetInterspersed(false)
	envOverride := flags.BoolP("environment-overrides", "e", false, "环境变量覆盖 makefile 里的赋值")
	keep := flags.BoolP("keep-going", "k", false, "某个目标失败后继续其余目标")
	dry := flags.BoolP("just-print", "n", false, "只打印将执行的 recipe 行，不执行")
	question := flags.BoolP("question", "q", false, "只查询是否需要重建（退出码 0/1），不执行")
	silent := flags.BoolP("silent", "s", false, "不回显将执行的 recipe 行")
	files := flags.StringArrayP("file", "f", nil, "读入的 makefile（可重复，'-' 表示标准输入）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	o := options{envOverride: *envOverride, keep: *keep, dry: *dry, question: *question, silent: *silent}
	m := newMakefile(o.envOverride)

	m.loadEnvironment(p.Env)
	var targets []string
	for _, operand := range flags.Args() {
		if name, value, ok := macroOperand(operand); ok {
			m.assign(name, value, false, originCommand)
			m.commandVars = append(m.commandVars, [2]string{name, value})
		} else {
			targets = append(targets, operand)
		}
	}
	dir := commandio.Directory(ctx)
	list := *files
	if len(list) == 0 {

		for _, name := range []string{"makefile", "Makefile"} {
			if statErr := statGuest(ctx, dir, name); statErr == nil {
				list = []string{name}
				break
			}
		}
		if len(list) == 0 {
			_, _ = fmt.Fprintln(h.Stderr, "make: 没有找到 makefile 或 Makefile")
			return interp.ExitStatus(2)
		}
	}
	env := &parseEnv{ctx: ctx, dir: dir, stdin: h.Stdin}
	for _, name := range list {
		if err := m.parseFile(env, name, map[string]bool{}, 0); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "make: %v\n", err)
			return interp.ExitStatus(2)
		}
	}

	recipeEnv, err := m.recipeEnv(p.Env)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "make: %v\n", err)
		return interp.ExitStatus(2)
	}
	shell, err := m.recipeShell()
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "make: %v\n", err)
		return interp.ExitStatus(2)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(h.Stderr, "make: %v\n", err)
		return interp.ExitStatus(2)
	}
	if len(targets) == 0 {
		if len(m.order) == 0 {
			_, _ = fmt.Fprintln(h.Stderr, "make: *** 没有指定目标且 makefile 中没有目标")
			return interp.ExitStatus(2)
		}
		targets = []string{m.order[0]}
	}
	e := &engine{
		ctx: ctx, h: h, p: p, o: o, m: m, dir: dir,
		shell: shell, env: recipeEnv, system: system,
		visiting: map[string]bool{}, done: map[string]bool{}, made: map[string]bool{},
	}
	if code := e.run(targets); code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

func statGuest(ctx context.Context, dir, name string) error {
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	_, err = system.Stat(ctx, commandio.Path(dir, name))
	return err
}

func readGuestText(ctx context.Context, dir, name string) (closeResult0 string, closeResult1 error) {
	f, err := commandio.Open(ctx, commandio.Path(dir, name), vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		closeResult1 = errors.Join(closeResult1, f.Close())
	}()

	return commandio.ReadText(ctx, f)
}

func (m *makefile) recipeEnv(base map[string]string) (map[string]string, error) {
	env := map[string]string{}
	for name, value := range base {
		env[name] = value
	}
	for name, v := range m.vars {
		if v.origin != originCommand && !m.envNames[name] {
			continue
		}
		value, err := m.expand(v.value, nil, map[string]bool{})
		if err != nil {
			return nil, fmt.Errorf("展开变量 %s: %w", name, err)
		}
		env[name] = value
	}
	return env, nil
}

func (m *makefile) recipeShell() (string, error) {
	v, ok := m.vars["SHELL"]
	if !ok {
		return "/bin/bash", nil
	}
	value, err := m.expand(v.value, nil, map[string]bool{})
	if err != nil {
		return "", fmt.Errorf("展开 SHELL: %w", err)
	}
	if value == "" {
		return "/bin/bash", nil
	}
	return value, nil
}

func (e *engine) run(goals []string) int {
	outdated := false
	for _, goal := range goals {
		made, err := e.update(goal)
		if errors.Is(err, errOutOfDate) {
			outdated = true
			continue
		}
		if err != nil {
			e.failed = true
			if !e.o.keep {
				return 2
			}
			continue
		}
		if !made && !e.o.dry && !e.o.question {
			_, _ = fmt.Fprintf(e.h.Stdout, "make: '%s' 是最新的\n", goal)
		}
	}
	if e.failed {
		return 2
	}
	if e.o.question && outdated {
		return 1
	}
	return 0
}

func (e *engine) update(target string) (bool, error) {
	if e.done[target] {
		return e.made[target], nil
	}
	if e.ctx.Err() != nil {
		_, _ = fmt.Fprintf(e.h.Stderr, "make: 已取消\n")
		return false, errFailure
	}
	if e.visiting[target] {
		_, _ = fmt.Fprintf(e.h.Stderr, "make: *** 依赖循环涉及 '%s'\n", target)
		return false, errFailure
	}
	e.visiting[target] = true
	defer delete(e.visiting, target)

	stem := ""
	var rules []*rule
	if rs := e.m.rules[target]; len(rs) > 0 {
		rules = rs
	} else if inferred, infStem := e.infer(target); inferred != nil {
		rules = []*rule{inferred}
		stem = infStem
	}
	if len(rules) == 0 {
		switch {
		case e.m.phony[target]:

			rules = []*rule{{targets: []string{target}, file: "makefile"}}
		default:
			if _, statErr := e.stat(target); statErr == nil {

				e.done[target] = true
				return false, nil
			}
			_, _ = fmt.Fprintf(e.h.Stderr, "make: *** 没有规则可以制作目标 '%s'\n", target)
			return false, errFailure
		}
	}

	var deps []string
	var recipes []recipeLine
	for _, r := range rules {
		for _, dep := range r.deps {
			if !slices.Contains(deps, dep) {
				deps = append(deps, dep)
			}
		}
		if len(r.recipes) > 0 {
			recipes = r.recipes
		}
	}

	depChanged, depFailed := false, false
	for _, dep := range deps {
		changed, err := e.update(dep)
		if err != nil {
			if !e.o.keep {
				return false, err
			}
			e.failed = true
			depFailed = true
			continue
		}
		depChanged = depChanged || changed
	}
	if depFailed {
		_, _ = fmt.Fprintf(e.h.Stderr, "make: *** 目标 '%s' 因先决目标失败而未重建\n", target)
		return false, errFailure
	}

	targetInfo, statErr := e.stat(target)
	needed := statErr != nil
	var newer []string
	for _, dep := range deps {
		depInfo, err := e.stat(dep)
		if err == nil && (statErr != nil || depInfo.ModTime().After(targetInfo.ModTime())) {
			newer = append(newer, dep)
			needed = true
		}
		if e.made[dep] {
			needed = true
			if !slices.Contains(newer, dep) {
				newer = append(newer, dep)
			}
		}
	}
	if e.m.phony[target] || depChanged && statErr != nil {
		needed = true
	}
	if !needed {
		e.done[target] = true
		return false, nil
	}
	first := ""
	if len(deps) > 0 {
		first = deps[0]
	}
	a := &automatic{target: target, first: first, stem: stem, newer: newer}
	if err := e.runRecipes(target, recipes, a); err != nil {
		if errors.Is(err, errOutOfDate) {
			e.done[target] = true
			e.made[target] = true
		}
		return false, err
	}
	e.done[target] = true
	e.made[target] = true
	return true, nil
}

func (e *engine) runRecipes(target string, recipes []recipeLine, a *automatic) error {
	for _, rl := range recipes {
		line, prefixSilent, ignore, force := splitPrefix(rl.text)
		silent := e.o.silent || prefixSilent
		expanded, err := e.m.expand(line, a, map[string]bool{})
		if err != nil {
			_, _ = fmt.Fprintf(e.h.Stderr, "make: *** [%s:%d: %s] %v\n", rl.file, rl.line, target, err)
			return errFailure
		}
		if e.o.question {

			if force {
				if err := e.runShell(target, rl, expanded, ignore); err != nil {
					return err
				}
			}
			continue
		}
		if e.o.dry {

			_, _ = fmt.Fprintln(e.h.Stdout, expanded)
			if !force {
				e.action = true
				continue
			}
		} else if !silent {
			_, _ = fmt.Fprintln(e.h.Stdout, expanded)
		}
		if err := e.runShell(target, rl, expanded, ignore); err != nil {
			return err
		}
	}
	if e.o.question {
		return errOutOfDate
	}
	e.action = true
	return nil
}

func (e *engine) runShell(target string, rl recipeLine, line string, ignore bool) error {
	if e.p.Control == nil {
		_, _ = fmt.Fprintf(e.h.Stderr, "make: *** [%s:%d: %s] 缺少进程控制端口，无法执行 recipe\n", rl.file, rl.line, target)
		return errFailure
	}
	terminal, err := e.p.Control.Run(e.ctx, proc.RunSpec{
		Parent:  e.p.ID,
		Name:    e.shell,
		Args:    []string{"-c", line},
		Dir:     e.dir,
		Env:     e.env,
		Streams: fd.Streams{Input: e.h.Stdin, Output: e.h.Stdout, Error: e.h.Stderr},
	})
	e.action = true
	if err != nil {
		code := 127
		if errors.Is(err, errno.NoExec) || errors.Is(err, errno.Permission) {
			code = 126
		}
		_, _ = fmt.Fprintf(e.h.Stderr, "make: *** [%s:%d: %s] 错误 %d（无法启动 %s: %v）\n", rl.file, rl.line, target, code, e.shell, err)
		if ignore {
			return nil
		}
		return errFailure
	}
	if terminal.ExitCode != 0 {
		if ignore {
			_, _ = fmt.Fprintf(e.h.Stderr, "make: [%s:%d: %s] 错误 %d（已忽略）\n", rl.file, rl.line, target, terminal.ExitCode)
			return nil
		}
		_, _ = fmt.Fprintf(e.h.Stderr, "make: *** [%s:%d: %s] 错误 %d\n", rl.file, rl.line, target, terminal.ExitCode)
		return errFailure
	}
	return nil
}

func (e *engine) infer(target string) (*rule, string) {
	for _, sr := range e.m.suffixes {
		if !strings.HasSuffix(target, sr.to) {
			continue
		}
		stem := strings.TrimSuffix(target, sr.to)
		source := stem + sr.from
		if _, err := e.stat(source); err != nil {
			continue
		}
		return &rule{targets: []string{target}, deps: []string{source}, recipes: sr.r.recipes}, stem
	}
	return nil, ""
}

func (e *engine) stat(name string) (fs.FileInfo, error) {
	return e.system.Stat(e.ctx, commandio.Path(e.dir, name))
}

func splitPrefix(line string) (text string, silent, ignore, force bool) {
	for len(line) > 0 {
		switch line[0] {
		case '@':
			silent = true
		case '-':
			ignore = true
		case '+':
			force = true
		default:
			return line, silent, ignore, force
		}
		line = line[1:]
	}
	return line, silent, ignore, force
}
