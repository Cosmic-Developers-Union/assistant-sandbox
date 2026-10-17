package nm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const maxInputBytes = 4 << 20

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	out := &stickyWriter{writer: h.Stdout}
	diag := &stickyWriter{writer: h.Stderr}
	flags := commandio.Flags(ctx, "nm")
	all := flags.BoolP("all", "a", false, "输出全部符号（缺省只输出外部符号）")
	external := flags.BoolP("external-only", "g", false, "只输出外部符号")
	undefined := flags.BoolP("undefined-only", "u", false, "只输出未定义符号")
	numeric := flags.BoolP("numeric-sort", "n", false, "按值排序")
	reverse := flags.BoolP("reverse-sort", "r", false, "反转排序")
	noSort := flags.BoolP("no-sort", "p", false, "不排序（符号表原序）")
	valueSort := flags.BoolP("value-sort", "v", false, "按值排序（-n 的同义旧写法）")
	filePrefix := flags.BoolP("file-prefix", "o", false, "每行前缀「文件名:」")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	if len(files) == 0 {
		_, _ = fmt.Fprintln(diag, "nm: 需要至少一个文件操作数")
		return interp.ExitStatus(2)
	}
	if *external && *undefined {
		_, _ = fmt.Fprintln(diag, "nm: -g 与 -u 不能同时使用")
		return interp.ExitStatus(2)
	}
	if *noSort && (*numeric || *valueSort) {
		_, _ = fmt.Fprintln(diag, "nm: -p 与 -n/-v 不能同时使用")
		return interp.ExitStatus(2)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "nm: %v\n", err)
		return interp.ExitStatus(1)
	}
	options := options{
		all:      *all,
		external: *external,
		undef:    *undefined,
		byValue:  *numeric || *valueSort,
		noSort:   *noSort,
		reverse:  *reverse,
		prefix:   *filePrefix,
	}
	code := run(h.Dir, system, files, options, out, diag)
	if out.err != nil {
		_, _ = fmt.Fprintf(diag, "nm: 输出失败: %v\n", out.err)
		return interp.ExitStatus(1)
	}
	if diag.err != nil {
		return interp.ExitStatus(1)
	}
	if code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

type options struct {
	all      bool
	external bool
	undef    bool
	byValue  bool
	noSort   bool
	reverse  bool
	prefix   bool
}

type stickyWriter struct {
	writer io.Writer
	err    error
}

func (w *stickyWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func run(dir string, system vmfs.System, files []string, options options, out, diag *stickyWriter) int {
	code := 0
	budget := maxInputBytes
	for _, name := range files {

		if len(files) > 1 && !options.prefix {
			_, _ = fmt.Fprintf(out, "\n%s:\n", name)
		}
		read, err := processFile(dir, system, name, options, budget, out)
		budget -= read
		if err != nil {
			_, _ = fmt.Fprintf(diag, "nm: %s: %v\n", name, err)
			code = 1
			if errors.Is(err, errOverBudget) {
				return 1
			}
		}
	}
	return code
}

var errOverBudget = errors.New("累计输入超过 4 MiB")

func processFile(dir string, system vmfs.System, name string, options options, budget int, out *stickyWriter) (int, error) {
	raw, read, err := readFile(dir, system, name, budget)
	if err != nil {
		return read, err
	}
	img, err := parseImage(raw)
	if err != nil {
		return read, err
	}
	entries := selectSymbols(img, options)
	sortEntries(entries, options)
	for _, e := range entries {
		_, _ = fmt.Fprint(out, formatEntry(e, options, name))
	}
	return read, nil
}

func selectSymbols(img *image, options options) []entry {
	var entries []entry
	for _, s := range img.symbols {
		switch {
		case options.undef:
			if s.shndx != shnUndef {
				continue
			}
		case options.all:

		default:

			if !isExternal(s) {
				continue
			}
		}
		entries = append(entries, entry{symbol: s, letter: typeLetter(img, s)})
	}
	return entries
}

type entry struct {
	symbol
	letter byte
}

func sortEntries(entries []entry, options options) {
	less := func(a, b entry) bool {
		if options.byValue {
			if a.value != b.value {
				return a.value < b.value
			}
			return a.name < b.name
		}
		return a.name < b.name
	}
	switch {
	case options.noSort && options.reverse:
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	case options.noSort:

	case options.reverse:
		sort.SliceStable(entries, func(i, j int) bool { return less(entries[j], entries[i]) })
	default:
		sort.SliceStable(entries, func(i, j int) bool { return less(entries[i], entries[j]) })
	}
}

func formatEntry(e entry, options options, name string) string {
	value := fmt.Sprintf("%16s", "")
	if e.shndx != shnUndef {
		value = fmt.Sprintf("%16x", e.value)
	}
	line := fmt.Sprintf("%s %c %s\n", value, e.letter, e.name)
	if options.prefix {
		return name + ":" + line
	}
	return line
}

func readFile(dir string, system vmfs.System, name string, budget int) ([]byte, int, error) {
	if budget < 0 {
		return nil, 0, errOverBudget
	}
	file, err := system.Open(context.Background(), commandio.Path(dir, name), vmfs.ReadOnly, 0)
	if err != nil {
		return nil, 0, err
	}

	raw, err := io.ReadAll(io.LimitReader(file, int64(budget+1)))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, 0, err
	}
	if len(raw) > budget {
		return nil, len(raw), errOverBudget
	}
	return raw, len(raw), nil
}
