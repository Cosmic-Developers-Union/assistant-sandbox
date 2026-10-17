package commandtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	fs "io/fs"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type Result struct {
	Stdout, Stderr string
	Code           int
}

func Invoke(ctx context.Context, run commandio.Handler, dir string, args []string, input io.Reader, stdout, stderr io.Writer) error {
	return InvokeWithControl(ctx, run, dir, args, input, stdout, stderr, nil)
}

type fixedUmask struct{ mask fs.FileMode }

func (f fixedUmask) Umask(uint64) (fs.FileMode, error) { return f.mask, nil }

type Options struct {
	Control proc.Control

	Env map[string]string

	Umask *fs.FileMode

	SID  proc.PID
	PGID proc.PID

	OutputLimit int

	Clock clock.Source

	Identity identity.Source
}

func InvokeWithControl(ctx context.Context, run commandio.Handler, dir string, args []string, input io.Reader, stdout, stderr io.Writer, control proc.Control) error {
	return InvokeWith(ctx, run, dir, args, input, stdout, stderr, Options{Control: control})
}

func InvokeWith(ctx context.Context, run commandio.Handler, dir string, args []string, input io.Reader, stdout, stderr io.Writer, options Options) error {
	descriptors := fd.New()
	descriptors.Attach(1, fd.Streams{Input: input, Output: stdout, Error: stderr})

	timeSource := options.Clock
	if timeSource == nil {
		timeSource = &clock.Virtual{}
	}
	set := capability.Set{FS: filesystem.NewFilesystem(dir), FD: descriptors, Clock: timeSource, Entropy: &entropy.Counter{}, Identity: options.Identity}
	if options.Umask != nil {
		set.UmaskSource = fixedUmask{*options.Umask}
	}

	process := proc.Proc{ID: 1, Dir: dir, Control: options.Control, Env: options.Env, Resources: set, SID: 1, PGID: 1}
	if options.SID != 0 {
		process.SID = options.SID
	}
	if options.PGID != 0 {
		process.PGID = options.PGID
	}
	ctx = proc.WithContext(ctx, process)
	runner, err := interp.New(interp.Dir(dir), interp.Env(expand.ListEnviron("TMPDIR="+dir)), interp.StdIO(input, stdout, stderr), interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, _ []string) error {
			h := interp.HandlerCtx(ctx)
			descriptors.Attach(1, fd.Streams{Input: h.Stdin, Output: h.Stdout, Error: h.Stderr})
			return commandio.Execute(ctx, "test", run, args)
		}
	}))
	if err != nil {
		return err
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader("under_test"), "test")
	if err != nil {
		return err
	}
	return runner.Run(ctx, tree)
}

func Run(t *testing.T, run commandio.Handler, dir string, args []string, input string) Result {
	t.Helper()
	return RunWithControl(t, run, dir, args, input, nil)
}

func RunWithControl(t *testing.T, run commandio.Handler, dir string, args []string, input string, control proc.Control) Result {
	t.Helper()
	return RunWith(t, run, dir, args, input, Options{Control: control})
}

func RunWithIdentity(t *testing.T, run commandio.Handler, dir string, args []string, input string, control proc.Control, identity identity.Source) Result {
	t.Helper()
	return RunWith(t, run, dir, args, input, Options{Control: control, Identity: identity})
}

func RunWith(t *testing.T, run commandio.Handler, dir string, args []string, input string, options Options) Result {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	limit := options.OutputLimit
	if limit <= 0 {
		limit = outputCap
	}
	out := newLimitedBuffer(limit, cancel)
	diagnostic := newLimitedBuffer(limit, cancel)
	err := InvokeWith(ctx, run, dir, args, strings.NewReader(input), out, diagnostic, options)

	if out.overflowed() || diagnostic.overflowed() {
		t.Fatalf("命令输出超过测试预算 %d 字节：该命令按设计是无界输出（下游关闭才是终止条件），"+
			"测试必须给它有限的下游或加取消；当前命令 %T", outputCap, run)
	}
	code := 0
	if err != nil {
		status, ok := errors.AsType[interp.ExitStatus](err)
		if !ok {
			t.Fatal(err)
		}
		code = int(status)
	}
	return Result{out.String(), diagnostic.String(), code}
}

var outputCap = 4 << 20

type limitedBuffer struct {
	buffer     bytes.Buffer
	limit      int
	overflow   bool
	onOverflow func()
}

func newLimitedBuffer(limit int, onOverflow func()) *limitedBuffer {
	return &limitedBuffer{limit: limit, onOverflow: onOverflow}
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if room := b.limit - b.buffer.Len(); room > 0 {
		if len(data) <= room {
			b.buffer.Write(data)
		} else {
			b.buffer.Write(data[:room])
			b.markOverflow()
		}
	} else if len(data) > 0 {
		b.markOverflow()
	}
	return len(data), nil
}

func (b *limitedBuffer) markOverflow() {
	if b.overflow {
		return
	}
	b.overflow = true
	if b.onOverflow != nil {
		b.onOverflow()
	}
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) overflowed() bool { return b.overflow }

type FailWriter struct{}

func (FailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
