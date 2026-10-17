package ls

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

type options struct {
	all        bool
	almostAll  bool
	directory  bool
	classify   bool
	slash      bool
	quote      bool
	bySize     bool
	byTime     bool
	byAccess   bool
	noSort     bool
	recursive  bool
	long       bool
	reverse    bool
	followAll  bool
	followArgs bool
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "ls")
	var opts options
	flags.BoolVarP(&opts.all, "all", "a", false, "显示隐藏项（含 . 与 ..）")
	flags.BoolVarP(&opts.almostAll, "almost-all", "A", false, "显示隐藏项（不含 . 与 ..）")
	flags.BoolVarP(&opts.directory, "directory", "d", false, "只列目录本身")
	flags.BoolVarP(&opts.classify, "classify", "F", false, "附加类型标记")
	flags.BoolVarP(&opts.slash, "slash-directories", "p", false, "目录附加 /")
	flags.BoolVarP(&opts.quote, "quote-nongraphic", "q", false, "不可打印字符显示为 ?")
	flags.BoolVarP(&opts.bySize, "size", "S", false, "按大小降序")
	flags.BoolVarP(&opts.byTime, "time", "t", false, "按修改时间")
	flags.BoolVarP(&opts.byAccess, "access-time", "u", false, "按访问时间")
	flags.BoolVarP(&opts.noSort, "no-sort", "f", false, "不排序（并显示隐藏项）")
	flags.BoolVarP(&opts.recursive, "recursive", "R", false, "递归子目录")
	flags.BoolVarP(&opts.long, "long", "l", false, "长格式")
	flags.BoolVarP(&opts.long, "no-owner", "g", false, "长格式（客体没有属主列，与 -l 同义）")
	flags.BoolVarP(&opts.long, "no-group", "o", false, "长格式（客体没有属组列，与 -l 同义）")
	flags.BoolVarP(&opts.reverse, "reverse", "r", false, "逆序")
	flags.BoolVarP(&opts.followAll, "dereference", "L", false, "跟随符号链接")
	flags.BoolVarP(&opts.followArgs, "dereference-args", "H", false, "跟随命令行上的符号链接")
	flags.BoolP("one", "1", false, "每行一个")

	unsupported := map[string]string{
		"C": "需要终端宽度排成多列，客体 stdout 不是终端",
		"x": "需要终端宽度排成多列，客体 stdout 不是终端",
		"m": "需要终端宽度排成逗号分隔的多列，客体 stdout 不是终端",
		"i": "客体没有 inode 身份",
		"s": "客体没有 st_blocks（块数）事实",
		"c": "客体没有 ctime（状态改变时间）能力",
		"n": "客体没有 uid/gid 事实",
	}
	for shorthand, reason := range unsupported {
		flags.BoolP("unsupported-"+shorthand, shorthand, false, reason)
	}
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	for shorthand, reason := range unsupported {
		if flags.Changed("unsupported-" + shorthand) {
			return fmt.Errorf("ls -%s 未支持：%s", shorthand, reason)
		}
	}
	if opts.noSort {

		opts.almostAll = true
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	if opts.byAccess {

		if _, ok := system.(vmfs.AccessTimes); !ok {
			return fmt.Errorf("ls -u 未支持：%w", errno.NotImplemented)
		}
	}
	operands := flags.Args()
	if len(operands) == 0 {
		operands = []string{"."}
	}

	followOperand := opts.followAll || opts.followArgs || (!opts.long && !opts.directory)
	directories := make([]string, 0, len(operands))
	files := make([]string, 0, len(operands))
	for _, name := range operands {
		info, err := operandInfo(ctx, system, commandio.Path(h.Dir, name), followOperand)
		if err != nil {
			return err
		}
		if info.IsDir() && !opts.directory {
			directories = append(directories, name)
			continue
		}
		files = append(files, name)
	}
	slices.Sort(files)
	if opts.reverse {
		slices.Reverse(files)
	}
	for _, name := range files {
		info, err := operandInfo(ctx, system, commandio.Path(h.Dir, name), followOperand)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(h.Stdout, opts.label(ctx, system, h, commandio.Path(h.Dir, name), name, info)); err != nil {
			return err
		}
	}
	if len(directories) > 0 && len(files) > 0 {
		if _, err := fmt.Fprintln(h.Stdout); err != nil {
			return err
		}
	}
	for index, name := range directories {

		if index > 0 {
			if _, err := fmt.Fprintln(h.Stdout); err != nil {
				return err
			}
		}
		if len(directories) > 1 || len(files) > 0 || opts.recursive {
			if _, err := fmt.Fprintf(h.Stdout, "%s:\n", name); err != nil {
				return err
			}
		}
		if err := opts.listDirectory(ctx, system, h, commandio.Path(h.Dir, name), name); err != nil {
			return err
		}
	}
	return nil
}

func (o options) showHidden() bool { return o.all || o.almostAll || o.noSort }

func (o options) listDirectory(ctx context.Context, system vmfs.System, h interp.HandlerContext, dir, display string) error {
	entries, err := system.ReadDir(ctx, dir)
	if err != nil {
		return err
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		if !o.showHidden() && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := o.info(ctx, system, dir, entry)
		if err != nil {
			return err
		}
		items = append(items, item{name: entry.Name(), mode: info.Mode(), size: info.Size(), modified: info.ModTime().UnixNano(), accessed: o.accessTime(ctx, system, dir, entry.Name(), info), target: o.linkTarget(ctx, system, dir, entry.Name(), info)})
	}
	if o.all {

		if info, err := system.Stat(ctx, dir); err == nil {
			items = append(items, item{name: ".", mode: info.Mode(), size: info.Size(), modified: info.ModTime().UnixNano(), accessed: o.accessTime(ctx, system, dir, ".", info)})
		} else {
			return err
		}
		if info, err := system.Stat(ctx, path.Join(dir, "..")); err == nil {
			items = append(items, item{name: "..", mode: info.Mode(), size: info.Size(), modified: info.ModTime().UnixNano(), accessed: o.accessTime(ctx, system, path.Dir(dir), "..", info)})
		}
	}
	o.sortItems(items)
	for _, selected := range items {
		if _, err := fmt.Fprintln(h.Stdout, o.render(selected)); err != nil {
			return err
		}
	}
	if !o.recursive {
		return nil
	}

	for _, selected := range items {
		if !selected.mode.IsDir() || selected.name == "." || selected.name == ".." {
			continue
		}
		childPath := path.Join(dir, selected.name)
		childDisplay := display + "/" + selected.name
		if _, err := fmt.Fprintf(h.Stdout, "\n%s:\n", childDisplay); err != nil {
			return err
		}
		if err := o.listDirectory(ctx, system, h, childPath, childDisplay); err != nil {
			return err
		}
	}
	return nil
}

func (o options) info(ctx context.Context, system vmfs.System, dir string, entry fs.DirEntry) (fs.FileInfo, error) {
	if !o.followAll && entry.Type()&fs.ModeSymlink != 0 {
		return system.Lstat(ctx, path.Join(dir, entry.Name()))
	}
	info, err := entry.Info()
	if err != nil {
		return nil, err
	}
	if o.followAll {
		if resolved, err := system.Stat(ctx, path.Join(dir, entry.Name())); err == nil {
			return resolved, nil
		}
	}
	return info, nil
}

func (o options) accessTime(ctx context.Context, system vmfs.System, dir, name string, info fs.FileInfo) int64 {
	access, ok := system.(vmfs.AccessTimes)
	if !ok {
		return 0
	}
	value, err := access.AccessTime(ctx, path.Join(dir, name))
	if err != nil {
		return 0
	}
	return value
}

func (o options) linkTarget(ctx context.Context, system vmfs.System, dir, name string, info fs.FileInfo) string {
	if !o.long || info.Mode()&fs.ModeSymlink == 0 {
		return ""
	}
	target, err := system.Readlink(ctx, path.Join(dir, name))
	if err != nil {
		return ""
	}
	return target
}

func (o options) sortItems(items []item) {
	if o.noSort {
		return
	}
	switch {
	case o.bySize:

		slices.SortStableFunc(items, func(left, right item) int {
			switch {
			case left.size > right.size:
				return -1
			case left.size < right.size:
				return 1
			default:
				return strings.Compare(left.name, right.name)
			}
		})
	case o.byTime || o.byAccess:

		slices.SortStableFunc(items, func(left, right item) int {
			leftKey, rightKey := left.modified, right.modified
			if o.byAccess {
				leftKey, rightKey = left.accessed, right.accessed
			}
			switch {
			case leftKey > rightKey:
				return -1
			case leftKey < rightKey:
				return 1
			default:
				return strings.Compare(left.name, right.name)
			}
		})
	default:
		slices.SortStableFunc(items, func(left, right item) int { return strings.Compare(left.name, right.name) })
	}
	if o.reverse {
		slices.Reverse(items)
	}
}

func (o options) render(selected item) string {
	name := selected.name
	if o.quote {
		name = quoteNonGraphic(name)
	}
	if o.classify || o.slash {
		switch {
		case selected.mode.IsDir():
			name += "/"
		case o.classify && selected.mode&fs.ModeSymlink != 0:
			name += "@"
		case o.classify && selected.mode&0o111 != 0:
			name += "*"
		case o.classify && selected.mode&fs.ModeNamedPipe != 0:
			name += "|"
		case o.classify && selected.mode&fs.ModeSocket != 0:
			name += "="
		}
	}
	if !o.long {
		return name
	}
	if selected.target != "" {
		name += " -> " + selected.target
	}
	return fmt.Sprintf("%s %8d %s", selected.mode.String(), selected.size, name)
}

func (o options) label(ctx context.Context, system vmfs.System, h interp.HandlerContext, full, name string, info fs.FileInfo) string {
	selected := item{name: name, mode: info.Mode(), size: info.Size(), modified: info.ModTime().UnixNano(), accessed: o.accessTime(ctx, system, path.Dir(full), path.Base(full), info), target: o.linkTarget(ctx, system, path.Dir(full), path.Base(full), info)}
	return o.render(selected)
}

func quoteNonGraphic(name string) string {
	data := []byte(name)
	changed := false
	for index, value := range data {
		if value < 0x20 || value == 0x7f {
			data[index] = '?'
			changed = true
		}
	}
	if !changed {
		return name
	}
	return string(data)
}

type item struct {
	name     string
	mode     fs.FileMode
	size     int64
	modified int64
	accessed int64

	target string
}

func operandInfo(ctx context.Context, system vmfs.System, full string, follow bool) (fs.FileInfo, error) {
	if follow {
		return system.Stat(ctx, full)
	}
	return system.Lstat(ctx, full)
}
