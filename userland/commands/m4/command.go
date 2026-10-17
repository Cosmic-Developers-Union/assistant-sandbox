package m4

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/spf13/pflag"
	"mvdan.cc/sh/v3/interp"
)

type macroOp struct {
	beforeFile int
	undefine   bool
	arg        string
}

type macroOps struct {
	ops      *[]macroOp
	flags    *pflag.FlagSet
	undefine bool
}

func (o macroOps) String() string { return "" }
func (o macroOps) Type() string   { return "name" }

func (o macroOps) Set(s string) error {
	name := s
	if !o.undefine {
		name, _, _ = strings.Cut(s, "=")
	}
	if !validMacroName(name) {
		return fmt.Errorf("无效的宏名 %q", name)
	}
	*o.ops = append(*o.ops, macroOp{beforeFile: len(o.flags.Args()), undefine: o.undefine, arg: s})
	return nil
}

func validMacroName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "m4")
	var ops []macroOp
	flags.VarP(macroOps{ops: &ops, flags: flags}, "define", "D",
		"把 name 定义为 value（缺省为空串），可重复且与 -U 保持出现顺序")
	flags.VarP(macroOps{ops: &ops, flags: flags, undefine: true}, "undefine", "U",
		"删除 name 的定义，可重复且与 -D 保持出现顺序")
	sync := flags.BoolP("synclines", "s", false, "生成 #line 行同步指令（客体不支持）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if *sync {

		_, _ = fmt.Fprintln(h.Stderr, "m4: -s 需要行同步输出：客体标准流没有行同步语义，拒绝处理")
		return interp.ExitStatus(2)
	}
	files := flags.Args()
	if len(files) == 0 {
		files = []string{"-"}
	}
	if code := newProcessor(ctx, files, ops).process(); code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

func newProcessor(ctx context.Context, files []string, ops []macroOp) *processor {
	h := commandio.Context(ctx)
	return newProcessorWith(ctx, h.Stdin, h.Stdout, h.Stderr, commandio.Directory(ctx), files, ops)
}

func newProcessorWith(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, dir string, files []string, ops []macroOp) *processor {
	p := &processor{
		ctx:     ctx,
		stdin:   stdin,
		stderr:  stderr,
		stdout:  stdout,
		dir:     dir,
		macros:  map[string][]*macro{},
		files:   files,
		options: ops,
		lquote:  "`",
		rquote:  "'",
		bcomm:   "#",
		ecomm:   "\n",
	}
	if proc0, err := proc.FromContext(ctx); err == nil {
		p.control = proc0.Control
		p.pid = proc0.ID
		p.env = proc0.Env
	}

	p.out = bufio.NewWriter(stdout)
	for name, b := range builtins {
		p.macros[name] = []*macro{b}
	}
	return p
}

func readGuestFile(ctx context.Context, dir, name string) (data string, err error) {
	f, err := commandio.Open(ctx, commandio.Path(dir, name), vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()
	return commandio.ReadText(ctx, f)
}

func (p *processor) takeStdin() (string, error) {
	if p.stdinDone {
		return "", nil
	}
	p.stdinDone = true
	return commandio.ReadText(p.ctx, p.stdin)
}

func (p *processor) writeDiagnostic(format string, a ...any) {
	_, _ = fmt.Fprintf(p.stderr, "m4: %s:%d: %s\n", p.file, p.line, fmt.Sprintf(format, a...))
}
