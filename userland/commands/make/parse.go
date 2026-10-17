package make

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

type origin uint8

const (
	originBuiltin origin = iota + 1
	originFile
	originEnvironment
	originCommand
)

type variable struct {
	value  string
	simple bool
	origin origin
}

type recipeLine struct {
	text string
	file string
	line int
}

type rule struct {
	targets, deps []string
	recipes       []recipeLine
	line          int
	file          string
}

type suffixRule struct {
	from, to string
	r        *rule
}

type makefile struct {
	vars        map[string]variable
	rules       map[string][]*rule
	order       []string
	phony       map[string]bool
	suffixes    []suffixRule
	envOverride bool
	commandVars [][2]string

	envNames map[string]bool
}

func newMakefile(envOverride bool) *makefile {
	m := &makefile{
		vars:        map[string]variable{},
		rules:       map[string][]*rule{},
		phony:       map[string]bool{},
		envNames:    map[string]bool{},
		envOverride: envOverride,
	}

	m.assign("SHELL", "/bin/bash", false, originBuiltin)
	return m
}

func (m *makefile) loadEnvironment(values map[string]string) {
	for name, value := range values {
		if name == "SHELL" || name == "MAKEFLAGS" {
			continue
		}
		m.assign(name, value, false, originEnvironment)
		m.envNames[name] = true
	}
}

var rejectedSpecials = map[string]bool{
	".DEFAULT":  true,
	".IGNORE":   true,
	".PRECIOUS": true,
	".SCCS_GET": true,
	".SILENT":   true,
	".WAIT":     true,
}

func (m *makefile) assign(name, value string, simple bool, from origin) {
	old, exists := m.vars[name]
	switch {
	case !exists:
	case from == originCommand:
	case from == originEnvironment && old.origin == originBuiltin:
	case from == originFile && old.origin != originCommand &&
		(old.origin != originEnvironment || !m.envOverride):
	default:
		return
	}
	if exists && old.origin == originEnvironment {
		m.envNames[name] = true
	}
	m.vars[name] = variable{value: value, simple: simple, origin: from}
}

func (m *makefile) appendVar(name, text string, from origin) error {
	old, exists := m.vars[name]
	if !exists {
		m.assign(name, text, false, from)
		return nil
	}
	switch {
	case from == originCommand:
	case from == originFile && old.origin != originCommand &&
		(old.origin != originEnvironment || !m.envOverride):
	default:
		return nil
	}
	value := old.value + " " + text
	simple := old.simple
	if simple {
		expanded, err := m.expand(text, nil, map[string]bool{})
		if err != nil {
			return err
		}
		value = old.value + " " + expanded
	}
	m.vars[name] = variable{value: value, simple: simple, origin: old.origin}
	return nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '.' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func macroOperand(s string) (string, string, bool) {
	name, value, ok := strings.Cut(s, "=")
	return name, value, ok && validName(name)
}

type automatic struct {
	target, first, stem string
	newer               []string
}

func (m *makefile) expand(s string, a *automatic, stack map[string]bool) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			out.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			out.WriteByte('$')
			break
		}
		open := s[i+1]
		if open == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		var name string
		var end int
		if open == '(' || open == '{' {
			closer := byte(')')
			if open == '{' {
				closer = '}'
			}
			depth := 1
			j := i + 2
			for ; j < len(s); j++ {
				if s[j] == open {
					depth++
				} else if s[j] == closer {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if depth != 0 {

				out.WriteString(s[i:])
				break
			}
			name, end = s[i+2:j], j+1
		} else {
			name, end = string(open), i+2
		}
		value, err := m.lookup(name, a, stack)
		if err != nil {
			return "", err
		}
		out.WriteString(value)
		i = end
	}
	return out.String(), nil
}

func (m *makefile) lookup(name string, a *automatic, stack map[string]bool) (string, error) {
	if colon := strings.IndexByte(name, ':'); colon >= 0 && strings.IndexByte(name[colon+1:], '=') >= 0 {
		return "", fmt.Errorf("$(%s)：替换引用 $(VAR:old=new) 是 GNU 扩展，未实现", name)
	}
	if strings.ContainsAny(name, " \t\n") {
		return "", fmt.Errorf("$(%s)：客体 make 没有函数展开", name)
	}
	if a != nil {
		switch name {
		case "@":
			return a.target, nil
		case "<":
			return a.first, nil
		case "*":
			return a.stem, nil
		case "?":
			return strings.Join(a.newer, " "), nil
		case "%":

			return "", nil
		}
	}
	if stack[name] {
		return "", nil
	}
	if v, ok := m.vars[name]; ok {
		if v.simple {
			return v.value, nil
		}
		stack[name] = true
		value, err := m.expand(v.value, a, stack)
		delete(stack, name)
		return value, err
	}
	return "", nil
}

type parseEnv struct {
	ctx   context.Context
	dir   string
	stdin io.Reader
}

func (m *makefile) parseFile(env *parseEnv, name string, seen map[string]bool, depth int) error {
	if depth > 16 {
		return fmt.Errorf("include 嵌套超过 16 层")
	}
	var content string
	var err error
	if name == "-" {
		content, err = commandio.ReadText(env.ctx, env.stdin)
	} else {
		content, err = readGuestText(env.ctx, env.dir, name)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if seen[name] {
		return fmt.Errorf("%s: 递归 include", name)
	}
	seen[name] = true
	defer delete(seen, name)
	return m.parse(env, name, content, seen, depth)
}

func (m *makefile) parse(env *parseEnv, name, content string, seen map[string]bool, depth int) error {
	lines := commandio.Rows(content)
	var current *rule
	special := ""
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		lineNo := i + 1
		if strings.HasPrefix(raw, "\t") {
			if current == nil {
				return fmt.Errorf("%s:%d: recipe 出现在任何规则之前", name, lineNo)
			}
			if special != "" {
				return fmt.Errorf("%s:%d: 特殊目标 %s 不能定义 recipe", name, lineNo, special)
			}
			recipe := raw[1:]
			start := lineNo
			for oddTrailingSlash(recipe) && i+1 < len(lines) {
				i++
				next := lines[i]
				next = strings.TrimPrefix(next, "\t")
				recipe += "\n" + next
			}
			current.recipes = append(current.recipes, recipeLine{text: recipe, file: name, line: start})
			continue
		}
		for oddTrailingSlash(raw) && i+1 < len(lines) {

			raw = strings.TrimRight(strings.TrimSuffix(raw, "\\"), " \t") + " " +
				strings.TrimLeft(lines[i+1], " \t")
			i++
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {

			continue
		}
		if strings.HasPrefix(trimmed, "include ") || strings.HasPrefix(trimmed, "include\t") {
			expanded, err := m.expandAt(name, lineNo, stripComment(trimmed[len("include"):]))
			if err != nil {
				return err
			}
			fields := strings.Fields(strings.TrimSpace(expanded))
			if len(fields) == 0 {
				return fmt.Errorf("%s:%d: include 需要文件名", name, lineNo)
			}
			for _, field := range fields {
				if err := m.parseFile(env, field, seen, depth+1); err != nil {
					return err
				}
			}
			current, special = nil, ""
			continue
		}
		text := stripComment(trimmed)
		if eq, op, ok := findAssignment(text); ok {
			nameText := text[:eq]
			if len(op) == 2 {
				nameText = trimmed[:eq-1]
			}
			expandedName, err := m.expandAt(name, lineNo, nameText)
			if err != nil {
				return err
			}
			varName := strings.TrimSpace(expandedName)
			if !validName(varName) {
				return fmt.Errorf("%s:%d: 无效的变量名 %q", name, lineNo, varName)
			}
			value := strings.TrimLeft(text[eq+1:], " \t")
			switch op {
			case "=":
				m.assign(varName, value, false, originFile)
			case ":=":
				expanded, err := m.expand(value, nil, map[string]bool{})
				if err != nil {
					return fmt.Errorf("%s:%d: %w", name, lineNo, err)
				}
				m.assign(varName, expanded, true, originFile)
			case "?=":
				if _, exists := m.vars[varName]; !exists {
					m.assign(varName, value, false, originFile)
				}
			case "+=":
				if err := m.appendVar(varName, value, originFile); err != nil {
					return fmt.Errorf("%s:%d: %w", name, lineNo, err)
				}
			}
			current, special = nil, ""
			continue
		}
		if err := m.parseRule(env, name, lineNo, text, &current, &special); err != nil {
			return err
		}
	}
	return nil
}

func (m *makefile) parseRule(env *parseEnv, name string, lineNo int, text string, current **rule, special *string) error {
	colon := strings.IndexByte(text, ':')
	if colon < 0 {
		return fmt.Errorf("%s:%d: 预期规则行或变量赋值", name, lineNo)
	}
	if colon+1 < len(text) && text[colon+1] == ':' {
		return fmt.Errorf("%s:%d: 暂不支持双冒号规则 '::'", name, lineNo)
	}
	left, right := text[:colon], text[colon+1:]
	inline := ""
	if semi := strings.IndexByte(right, ';'); semi >= 0 {
		inline = strings.TrimLeft(right[semi+1:], " \t")
		right = right[:semi]
	}
	expandedTargets, err := m.expandAt(name, lineNo, left)
	if err != nil {
		return err
	}
	expandedDeps, err := m.expandAt(name, lineNo, strings.TrimSpace(right))
	if err != nil {
		return err
	}
	targets := strings.Fields(expandedTargets)
	deps := strings.Fields(expandedDeps)
	if len(targets) == 0 {
		return fmt.Errorf("%s:%d: 空目标", name, lineNo)
	}
	for _, item := range append(slices.Clone(targets), deps...) {
		if strings.ContainsRune(item, '%') {
			return fmt.Errorf("%s:%d: 暂不支持模式规则 '%s'：POSIX make 没有模式规则", name, lineNo, item)
		}
	}
	r := &rule{targets: targets, deps: deps, line: lineNo, file: name}
	if inline != "" {
		r.recipes = append(r.recipes, recipeLine{text: inline, file: name, line: lineNo})
	}
	*current, *special = r, ""
	for _, target := range targets {
		switch {
		case target == ".PHONY":
			if len(r.recipes) > 0 {
				return fmt.Errorf("%s:%d: 特殊目标 .PHONY 不能定义 recipe", name, lineNo)
			}
			for _, dep := range deps {
				m.phony[dep] = true
			}
			*special = target
		case target == ".POSIX":

			if len(deps) > 0 || len(r.recipes) > 0 {
				return fmt.Errorf("%s:%d: 特殊目标 .POSIX 不能有先决条件或 recipe", name, lineNo)
			}
			*special = target
		case target == ".SUFFIXES":
			if len(deps) > 0 {
				return fmt.Errorf("%s:%d: 暂不支持 .SUFFIXES 依赖列表：后缀规则按定义顺序结构化识别", name, lineNo)
			}
			*special = target
		case rejectedSpecials[target]:
			return fmt.Errorf("%s:%d: 暂不支持特殊目标 %s：超出声明的 POSIX 子集", name, lineNo, target)
		case isSingleSuffix(target):
			return fmt.Errorf("%s:%d: 暂不支持单后缀规则 '%s'，只声明 .c.o 双后缀形式", name, lineNo, target)
		case len(targets) == 1 && isDoubleSuffix(target):
			if len(deps) > 0 {
				return fmt.Errorf("%s:%d: 后缀规则 %s 不能有先决条件", name, lineNo, target)
			}
			dot := strings.IndexByte(target[1:], '.')
			m.addSuffixRule(suffixRule{from: target[:dot+1], to: target[dot+1:], r: r})
			*current, *special = r, ""
		default:
			m.rules[target] = append(m.rules[target], r)
			if !strings.HasPrefix(target, ".") {
				if !slices.Contains(m.order, target) {
					m.order = append(m.order, target)
				}
			}
		}
	}
	return nil
}

func (m *makefile) addSuffixRule(sr suffixRule) {
	for i := range m.suffixes {
		if m.suffixes[i].from == sr.from && m.suffixes[i].to == sr.to {
			m.suffixes[i] = sr
			return
		}
	}
	m.suffixes = append(m.suffixes, sr)
}

func (m *makefile) expandAt(name string, lineNo int, text string) (string, error) {
	value, err := m.expand(text, nil, map[string]bool{})
	if err != nil {
		return "", fmt.Errorf("%s:%d: %w", name, lineNo, err)
	}
	return value, nil
}

func findAssignment(s string) (int, string, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			if i+1 < len(s) && s[i+1] == '=' {
				return i + 1, ":=", true
			}
			return 0, "", false
		case '?', '+':
			if i+1 < len(s) && s[i+1] == '=' {
				return i + 1, string(s[i]) + "=", true
			}
		case '=':
			return i, "=", true
		}
	}
	return 0, "", false
}

func oddTrailingSlash(s string) bool {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

func stripComment(s string) string {
	escaped := false
	for i := 0; i < len(s); i++ {
		if s[i] == '#' && !escaped {
			return s[:i]
		}
		if s[i] == '\\' {
			escaped = !escaped
		} else {
			escaped = false
		}
	}
	return s
}

func isDoubleSuffix(name string) bool {
	if len(name) < 4 || name[0] != '.' || strings.ContainsRune(name, '/') {
		return false
	}
	return strings.IndexByte(name[1:], '.') >= 1 && strings.Count(name, ".") == 2
}

func isSingleSuffix(name string) bool {
	return len(name) > 1 && name[0] == '.' && !strings.ContainsRune(name, '/') &&
		strings.Count(name, ".") == 1
}
