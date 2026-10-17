package locale

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	toollocale "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const localeRoot = "/usr/lib/locale"

var categories = []toollocale.Category{
	toollocale.CType,
	toollocale.Collate,
	toollocale.Messages,
	toollocale.Monetary,
	toollocale.Numeric,
	toollocale.Time,
}

var builtinNames = []string{"C", "C.UTF-8", "POSIX"}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "locale")
	all := flags.BoolP("all", "a", false, "列出可用的语言环境")
	keywords := flags.BoolP("keyword-name", "k", false, "按关键字查询（charmap 或类别名）")
	sections := flags.BoolP("category", "c", false, "输出类别分节头")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if *all && *keywords {
		_, _ = fmt.Fprintln(h.Stderr, "locale: -a 与 -k 不能同给")
		return interp.ExitStatus(2)
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	env := environ(p)
	switch {
	case *all:
		if len(flags.Args()) > 0 {
			_, _ = fmt.Fprintln(h.Stderr, "locale: -a 不接受名字")
			return interp.ExitStatus(2)
		}
		return listAll(ctx, h)
	case *keywords:
		if len(flags.Args()) == 0 {
			_, _ = fmt.Fprintln(h.Stderr, "locale: -k 需要关键字名字")
			return interp.ExitStatus(2)
		}
		return printKeywords(ctx, h, env, flags.Args(), *sections)
	default:
		return printCategories(ctx, h, env, flags.Args(), *sections)
	}
}

func environ(p proc.Proc) []string {
	keys := make([]string, 0, len(p.Env))
	for key := range p.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+p.Env[key])
	}
	return pairs
}

func resolve(env []string, category toollocale.Category) string {
	for _, key := range []string{"LC_ALL", string(category), "LANG"} {
		if value := getenv(env, key); value != "" {
			return value
		}
	}
	return "C"
}

func getenv(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return entry[len(prefix):]
		}
	}
	return ""
}

func categoryByName(name string) toollocale.Category {
	for _, category := range categories {
		if string(category) == name {
			return category
		}
	}
	return ""
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

func known(ctx context.Context, name string) (bool, error) {
	if !safeName(name) {
		return false, nil
	}
	if _, err := toollocale.Open(name); err == nil {
		return true, nil
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return false, err
	}
	if _, err := system.Stat(ctx, path.Join(localeRoot, name)); err != nil {
		if missing(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, errno.NotFound)
}

func illegalLocale(h interp.HandlerContext, name string) error {
	_, _ = fmt.Fprintf(h.Stderr, "locale: 非法语言环境 %s\n", name)
	return interp.ExitStatus(1)
}

func printCategories(ctx context.Context, h interp.HandlerContext, env []string, names []string, sections bool) error {
	selected := categories
	if len(names) > 0 {
		selected = make([]toollocale.Category, 0, len(names))
		for _, name := range names {
			category := categoryByName(name)
			if category == "" {
				_, _ = fmt.Fprintf(h.Stderr, "locale: %s: 不是语言环境类别名\n", name)
				return interp.ExitStatus(1)
			}
			selected = append(selected, category)
		}
	}
	values := make([]string, len(selected))
	for index, category := range selected {
		value := resolve(env, category)
		ok, err := known(ctx, value)
		if err != nil {
			return err
		}
		if !ok {
			return illegalLocale(h, value)
		}
		values[index] = value
	}
	for index, category := range selected {
		if sections {
			if _, err := fmt.Fprintln(h.Stdout, string(category)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(h.Stdout, "%s=\"%s\"\n", category, values[index]); err != nil {
			return err
		}
	}
	return nil
}

func printKeywords(ctx context.Context, h interp.HandlerContext, env []string, names []string, sections bool) error {
	for _, name := range names {
		if name == "charmap" {
			value, err := charmap(ctx, h, env)
			if err != nil {
				return err
			}

			if _, err := fmt.Fprintf(h.Stdout, "charmap=\"%s\"\n", value); err != nil {
				return err
			}
			continue
		}
		category := categoryByName(name)
		if category == "" {
			_, _ = fmt.Fprintf(h.Stderr, "locale: %s: 客体没有该关键字\n", name)
			return interp.ExitStatus(1)
		}
		value := resolve(env, category)
		ok, err := known(ctx, value)
		if err != nil {
			return err
		}
		if !ok {
			return illegalLocale(h, value)
		}
		if sections {
			if _, err := fmt.Fprintln(h.Stdout, name); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(h.Stdout, "%s=\"%s\"\n", category, value); err != nil {
			return err
		}
	}
	return nil
}

func charmap(ctx context.Context, h interp.HandlerContext, env []string) (closeResult0 string, closeResult1 error) {
	name := resolve(env, toollocale.CType)
	if !safeName(name) {
		return "", illegalLocale(h, name)
	}
	if _, err := toollocale.Open(name); err == nil {

		if toollocale.IsCUTF8(name) {
			return "UTF-8", nil
		}
		return "ANSI_X3.4-1968", nil
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return "", err
	}
	file, err := system.Open(ctx, path.Join(localeRoot, name, "locale.conf"), vmfs.ReadOnly, 0)
	if err != nil {
		if missing(err) {

			return "", illegalLocale(h, name)
		}
		return "", err
	}
	defer func() {
		closeResult1 = errors.Join(closeResult1, file.Close())
	}()

	text, err := commandio.ReadText(ctx, file)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(line, "charmap="); ok {
			return strings.TrimSpace(value), nil
		}
	}
	_, _ = fmt.Fprintf(h.Stderr, "locale: %s: 归档没有声明 charmap\n", name)
	return "", interp.ExitStatus(1)
}

func listAll(ctx context.Context, h interp.HandlerContext) error {
	names := append([]string(nil), builtinNames...)
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	entries, err := system.ReadDir(ctx, localeRoot)
	if err != nil {
		if !missing(err) {
			return err
		}

		entries = nil
	}
	for _, entry := range entries {

		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	previous := ""
	for _, name := range names {
		if name == previous {
			continue
		}
		previous = name
		if _, err := fmt.Fprintln(h.Stdout, name); err != nil {
			return err
		}
	}
	return nil
}
