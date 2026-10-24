package talk_test

import (
	"context"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	commandio "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	talkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/talk"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type fixture struct {
	t        *testing.T
	terminal *tty.Terminal
	dir      string
	Clock    *clock.Virtual
	cancel   context.CancelFunc

	fds []uint32

	stdin fd.Stream

	system vmfs.System

	dropSystem bool

	wrapFD func(fd.Descriptors) fd.Descriptors

	done <-chan error

	drained string
}

func rootFSFor(dir string) vmfs.System {
	inner := platformfs.NewFilesystem(dir)
	return rootFSWrap{inner: inner, dir: dir}
}

type rootFSWrap struct {
	inner vmfs.System
	dir   string
}

func (r rootFSWrap) guestPath(name string) string {
	if name == r.dir {
		return "."
	}
	if strings.HasPrefix(name, "/") {
		return strings.TrimPrefix(name, "/")
	}
	return name
}

func (r rootFSWrap) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	return r.inner.Open(ctx, r.guestPath(name), flags, mode)
}
func (r rootFSWrap) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return r.inner.Stat(ctx, r.guestPath(name))
}
func (r rootFSWrap) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return r.inner.Lstat(ctx, r.guestPath(name))
}
func (r rootFSWrap) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	return r.inner.ReadDir(ctx, r.guestPath(name))
}
func (r rootFSWrap) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	return r.inner.Mkdir(ctx, r.guestPath(name), mode)
}
func (r rootFSWrap) Remove(ctx context.Context, name string) error {
	return r.inner.Remove(ctx, r.guestPath(name))
}
func (r rootFSWrap) Rename(ctx context.Context, from, to string) error {
	return r.inner.Rename(ctx, r.guestPath(from), r.guestPath(to))
}
func (r rootFSWrap) Link(ctx context.Context, from, to string) error {
	return r.inner.Link(ctx, r.guestPath(from), r.guestPath(to))
}
func (r rootFSWrap) Symlink(ctx context.Context, oldname, newname string) error {
	return r.inner.Symlink(ctx, oldname, newname)
}
func (r rootFSWrap) Readlink(ctx context.Context, name string) (string, error) {
	return r.inner.Readlink(ctx, r.guestPath(name))
}

func newTerminal(t *testing.T) *tty.Terminal {
	t.Helper()
	terminals, err := tty.New(tty.Flags{Canonical: true, Echo: false, ISIG: false}, tty.Size{Rows: 24, Cols: 80}, 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	return terminals
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{t: t, terminal: newTerminal(t), dir: t.TempDir(), Clock: &clock.Virtual{}}
}

func (f *fixture) start(args ...string) <-chan error {
	t := f.t
	descriptors := fd.New()
	descriptors.Attach(1, fd.Streams{Input: f.terminal, Output: f.terminal, Error: f.terminal})
	ids := f.fds
	if ids == nil {
		ids = []uint32{0, 1, 2}
	}
	if f.stdin != nil && !slices.Contains(ids, 0) {
		ids = append([]uint32{0}, ids...)
	}
	for id := uint32(0); id < 3; id++ {

		if err := descriptors.Close(context.Background(), 1, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		stream := fd.Stream(f.terminal)
		if id == 0 && f.stdin != nil {
			stream = f.stdin
		}
		if err := descriptors.Install(1, id, stream); err != nil {
			t.Fatal(err)
		}
	}
	var table fd.Descriptors = descriptors
	if f.wrapFD != nil {
		table = f.wrapFD(table)
	}

	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel

	t.Cleanup(cancel)
	set := capability.Set{
		FD:       table,
		Clock:    f.Clock,
		Entropy:  &entropy.Counter{},
		Identity: identity.SingleUser{},
	}
	if !f.dropSystem {
		set.FS = f.system
		if set.FS == nil {
			set.FS = rootFSFor(f.dir)
		}
	}
	process := proc.Proc{ID: 1, Dir: f.dir, Resources: set, SID: 1, PGID: 1}
	done := make(chan error, 1)
	f.done = done
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("talk panic: %v", r)
			}
		}()
		done <- invokeReal(ctx, process, args, f.terminal)
	}()
	return done
}

// awaitDeadline 覆盖满负载 CI 的调度延迟;这些是功能等待而非性能门禁。
const awaitDeadline = 30 * time.Second

func (f *fixture) run(args ...string) <-chan error {
	done := f.start(args...)
	deadline := time.Now().Add(awaitDeadline)
	for time.Now().Before(deadline) {
		f.drained += string(f.terminal.Drain())
		if strings.Contains(f.drained, "等待对端") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	return done
}

func (f *fixture) await(ready func(seen string) bool) string {
	f.t.Helper()
	seen := f.drained
	f.drained = ""
	deadline := time.Now().Add(awaitDeadline)
	for time.Now().Before(deadline) {
		seen += string(f.terminal.Drain())
		if ready(seen) {
			return seen
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatalf("等待终端输出超时: %q", seen)
	return seen
}

func (f *fixture) awaitText(want string) string {
	f.t.Helper()
	return f.await(func(seen string) bool { return strings.Contains(seen, want) })
}

func (f *fixture) until(ready func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(awaitDeadline)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	f.t.Fatal("等待条件成立超时")
}

func (f *fixture) waitDone(done <-chan error) error {
	f.t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(awaitDeadline):
		f.cancel()
		<-done
		f.t.Fatal("talk 未退出（已取消会话）")
		return nil
	}
}

func (f *fixture) exchange(line string) error {
	f.t.Helper()
	if _, err := f.terminal.Input([]byte(line + "\n")); err != nil {
		return err
	}
	f.awaitText("[me] " + line)
	if _, err := f.terminal.Input([]byte{0x04}); err != nil {
		return err
	}
	return f.waitDone(f.done)
}

func TestUnknownUserRejected(t *testing.T) {

	r := commandtest.Run(t, talkcmd.Run, t.TempDir(), []string{"alice"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有这个用户") {
		t.Fatalf("未知用户 = %+v", r)
	}
}

func TestDeclaredUserAccepted(t *testing.T) {
	f := newFixture(t)
	done := f.run("sandbox")

	if err := os.MkdirAll(filepath.Dir(channelHostPath(f.dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	incoming := channelHostPath(f.dir) + ".incoming"
	if err := os.WriteFile(incoming, []byte("hello from peer\nsecond line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(incoming, channelHostPath(f.dir)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(awaitDeadline)
	seen := ""
	for time.Now().Before(deadline) {
		seen += string(f.terminal.Drain())
		if strings.Count(seen, "[peer]") >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := f.terminal.Input([]byte("hi back\n")); err != nil {
		t.Error(err)
	}
	deadline = time.Now().Add(awaitDeadline)
	for time.Now().Before(deadline) {
		seen += string(f.terminal.Drain())
		if strings.Contains(seen, "[me] hi back") {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	if _, err := f.terminal.Input([]byte{0x04}); err != nil {
		t.Error(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出: %v；末尾输出=%q 终端=%q", err, seen, f.terminal.Drain())
		}
	case <-time.After(5 * time.Second):
		f.cancel()
		<-done
		t.Fatal("talk 未退出（已取消会话）")
	}
	if !strings.Contains(seen, "[peer] hello from peer") || !strings.Contains(seen, "[peer] second line") {
		t.Fatalf("对端行应显示: %q", seen)
	}

	if got := strings.Count(seen, "[peer] hi back"); got != 0 {
		t.Fatalf("本地发言被回读成对端消息: %q", seen)
	}

	system := rootFSFor(f.dir)
	file, err := system.Open(context.Background(), "/var/spool/talk/channel", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, _ := io.ReadAll(file)
	if !strings.Contains(string(data), "hi back") {
		t.Fatalf("通道应有本地发言: %q", string(data))
	}
}

func TestRequiresTerminal(t *testing.T) {

	r := commandtest.Run(t, talkcmd.Run, t.TempDir(), []string{"sandbox"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "不是终端") {
		t.Fatalf("无终端 = %+v", r)
	}
}

func invokeReal(ctx context.Context, process proc.Proc, args []string, stdin *tty.Terminal) error {
	return invokeCommand(proc.WithContext(ctx, process), process.Dir, args, stdin)
}

func invokeCommand(ctx context.Context, dir string, args []string, stdin io.Reader) error {
	diagnostics := &strings.Builder{}
	runner, err := interp.New(
		interp.Dir(dir),
		interp.Env(expand.ListEnviron("TMPDIR="+dir)),
		interp.StdIO(stdin, io.Discard, diagnostics),
		interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, _ []string) error {
				return commandio.Execute(ctx, "talk", talkcmd.Run, args)
			}
		}),
	)
	if err != nil {
		return err
	}
	tree, err := parseTree("under_test")
	if err != nil {
		return err
	}
	if runErr := runner.Run(ctx, tree); runErr != nil {
		if diagnostics.Len() > 0 {
			return fmt.Errorf("%w: %s", runErr, diagnostics.String())
		}
		return runErr
	}
	return nil
}

func parseTree(name string) (*syntax.File, error) {
	return syntax.NewParser().Parse(strings.NewReader(name), "test")
}
