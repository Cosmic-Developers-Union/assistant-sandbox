package ctags

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const maxInputBytes = 4 << 20

const defaultTagsFile = "tags"

var errOverBudget = errors.New("累计输入超过 4 MiB")

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	out := &stickyWriter{writer: h.Stdout}
	diag := &stickyWriter{writer: h.Stderr}
	flags := commandio.Flags(ctx, "ctags")
	appendMode := flags.BoolP("append", "a", false, "把新标签追加到输出文件")
	output := flags.StringP("output", "f", "", "输出文件（缺省 ./tags，- 表示标准输出）")
	cxref := flags.BoolP("cxref", "x", false, "生成 cxref 清单到标准输出")

	tFlag := flags.BoolP("traditional", "t", false, "不支持")
	uFlag := flags.BoolP("update", "u", false, "不支持")
	vFlag := flags.BoolP("vgrind", "v", false, "不支持")
	wFlag := flags.BoolP("warnings", "w", false, "不支持")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if *tFlag || *uFlag || *vFlag || *wFlag {
		diag.Println("ctags: -t/-u/-v/-w 不支持：客体 ctags 只做 C 子集（函数定义与 typedef）")
		return interp.ExitStatus(2)
	}
	if *cxref && *appendMode {
		diag.Println("ctags: -x 写标准输出，与 -a 不能同时使用")
		return interp.ExitStatus(2)
	}
	files := flags.Args()
	if len(files) == 0 {
		diag.Println("ctags: 需要至少一个源文件")
		return interp.ExitStatus(2)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		diag.Printf("ctags: %v\n", err)
		return interp.ExitStatus(1)
	}
	work := &work{
		ctx:    ctx,
		system: system,
		dir:    h.Dir,
		out:    out,
		diag:   diag,
	}
	code := work.run(files, options{appendMode: *appendMode, output: *output, cxref: *cxref})
	if out.err != nil {
		diag.Printf("ctags: 输出失败: %v\n", out.err)
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
	appendMode bool
	output     string
	cxref      bool
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

func (w *stickyWriter) Printf(format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		w.err = err
	}
}

func (w *stickyWriter) Println(args ...any) {
	if _, err := fmt.Fprintln(w, args...); err != nil {
		w.err = err
	}
}

type work struct {
	ctx    context.Context
	system vmfs.System
	dir    string
	out    *stickyWriter
	diag   *stickyWriter
	budget int
}

func (w *work) diagf(format string, args ...any) {
	w.diag.Printf(format, args...)
}

func (w *work) run(files []string, options options) int {
	var tags []tag
	code := 0
	w.budget = maxInputBytes
	for _, name := range files {
		switch ext := path.Ext(name); ext {
		case ".c", ".h":
		default:
			w.diagf("ctags: %s: 不是 C 源文件（只处理 .c/.h），已跳过\n", name)
			code = 1
			continue
		}
		source, err := w.read(commandio.Path(w.dir, name))
		if err != nil {
			w.diagf("ctags: %s: %v\n", name, err)
			code = 1
			if errors.Is(err, errOverBudget) {

				return 1
			}
			continue
		}
		tags = append(tags, scan(name, source)...)
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].name < tags[j].name })
	if options.cxref {
		for _, t := range tags {
			_, _ = fmt.Fprintf(w.out, "%s %d %s %s\n", t.name, t.line, t.file, t.text)
		}
		return code
	}
	target := options.output
	if target == "" {
		target = defaultTagsFile
	}
	var content strings.Builder
	for _, t := range tags {
		fmt.Fprintf(&content, "%s\t%s\t/^%s$/\n", t.name, t.file, escapePattern(t.text))
	}
	if err := w.writeTags(target, content.String(), options.appendMode); err != nil {
		w.diagf("ctags: %v\n", err)
		return 1
	}
	return code
}

func scan(file, source string) []tag {
	var tags []tag
	clean := cleanLines(source)
	scanFunctions(clean, func(name string, lineNo int) {
		tags = append(tags, tag{name: name, file: file, line: lineNo, text: rawLine(source, lineNo)})
	})
	scanTypedefs(clean, func(name string, lineNo int) {
		tags = append(tags, tag{name: name, file: file, line: lineNo, text: rawLine(source, lineNo)})
	})
	return tags
}

func rawLine(source string, lineNo int) string {
	lines := strings.Split(source, "\n")
	if lineNo < 1 || lineNo > len(lines) {
		return ""
	}
	return strings.TrimSuffix(lines[lineNo-1], "\r")
}

func (w *work) read(target string) (string, error) {
	if w.budget < 0 {
		return "", errOverBudget
	}
	file, err := w.system.Open(w.ctx, target, vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}

	data, err := io.ReadAll(io.LimitReader(file, int64(w.budget+1)))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if len(data) > w.budget {
		return "", errOverBudget
	}
	w.budget -= len(data)
	return string(data), nil
}

func (w *work) writeTags(target, content string, appendMode bool) error {
	flags := vmfs.WriteOnly | vmfs.Create
	if !appendMode {
		flags |= vmfs.Truncate
	} else {
		flags |= vmfs.Append
	}
	if target == "-" {

		_, err := w.out.Write([]byte(content))
		return err
	}
	file, err := w.system.Open(w.ctx, commandio.Path(w.dir, target), flags, 0o666)
	if err != nil {
		return err
	}
	_, writeErr := file.Write([]byte(content))
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}
