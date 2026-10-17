package lp_test

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	lpcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/lp"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
	"mvdan.cc/sh/v3/interp"
)

type fixture struct {
	t      *testing.T
	dir    string
	Clock  *clock.Virtual
	kernel proc.Scheduler
	fs     vmfs.System
}

type rootFS struct {
	inner vmfs.System
	dir   string
}

func newRootFS(dir string) vmfs.System { return rootFS{inner: platformfs.NewFilesystem(dir), dir: dir} }

func guestPath(dir, name string) string {
	if name == dir {
		return "."
	}
	if dir != "/" && strings.HasPrefix(name, dir+"/") {
		return name[len(dir)+1:]
	}
	if strings.HasPrefix(name, "/") {
		return strings.TrimPrefix(name, "/")
	}
	return name
}

func (r rootFS) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	return r.inner.Open(ctx, guestPath(r.dir, name), flags, mode)
}
func (r rootFS) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return r.inner.Stat(ctx, guestPath(r.dir, name))
}
func (r rootFS) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return r.inner.Lstat(ctx, guestPath(r.dir, name))
}
func (r rootFS) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	return r.inner.ReadDir(ctx, guestPath(r.dir, name))
}
func (r rootFS) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	return r.inner.Mkdir(ctx, guestPath(r.dir, name), mode)
}
func (r rootFS) Remove(ctx context.Context, name string) error {
	return r.inner.Remove(ctx, guestPath(r.dir, name))
}
func (r rootFS) Rename(ctx context.Context, from, to string) error {
	return r.inner.Rename(ctx, guestPath(r.dir, from), guestPath(r.dir, to))
}
func (r rootFS) Link(ctx context.Context, from, to string) error {
	return r.inner.Link(ctx, guestPath(r.dir, from), guestPath(r.dir, to))
}
func (r rootFS) Symlink(ctx context.Context, oldname, newname string) error {
	return r.inner.Symlink(ctx, oldname, newname)
}
func (r rootFS) Readlink(ctx context.Context, name string) (string, error) {
	return r.inner.Readlink(ctx, guestPath(r.dir, name))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	clk := &clock.Virtual{}
	meters, err := resources.NewSession(resources.Limits{Memory: 32 << 20, Work: 1 << 20},
		resources.Limits{Memory: 8 << 20, Work: 1 << 18})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := executable.New(map[string]executable.Program{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := vm.New(vm.Services{
		Set: capability.Set{
			FS: newRootFS(dir), FD: fd.New(), Identity: identity.SingleUser{},
			Clock: clk, Entropy: &entropy.Counter{}, Resources: meters,
		},
		Programs: registry,
	}, 16)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, dir: dir, Clock: clk, kernel: kernel, fs: newRootFS(dir)}
}

type control struct {
	kernel proc.Scheduler
}

func (control) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (control) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (control) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c control) QueueJob(ctx context.Context, spec proc.JobSpec) (string, error) {
	return c.kernel.QueueJob(ctx, spec)
}
func (c control) ListJobs(ctx context.Context) ([]string, error) {
	return c.kernel.ListJobs(ctx)
}
func (c control) RemoveJob(ctx context.Context, id string) (bool, error) {
	return c.kernel.RemoveJob(ctx, id)
}
func (c control) ReadJob(ctx context.Context, id string) (string, error) {
	return c.kernel.ReadJob(ctx, id)
}
func (c control) QueuePrint(ctx context.Context, spec proc.PrintSpec) (string, error) {
	return c.kernel.QueuePrint(ctx, spec)
}
func (c control) ListPrintRequests(ctx context.Context) ([]string, error) {
	return c.kernel.ListPrintRequests(ctx)
}
func (c control) RunDueJobs(ctx context.Context) error {
	return c.kernel.RunDueJobs(ctx)
}

func (f *fixture) run(args []string, input string) commandtest.Result {
	return f.runHandler(lpcmd.Run, args, input)
}

func (f *fixture) runHandler(handler commandio.Handler, args []string, input string) commandtest.Result {
	return f.runOptions(handler, args, input, commandtest.Options{Control: control{f.kernel}})
}

func (f *fixture) runOptions(handler commandio.Handler, args []string, input string, options commandtest.Options) commandtest.Result {
	var result commandtest.Result
	err := commandtest.InvokeWith(context.Background(), handler, f.dir, args, strings.NewReader(input),
		&capture{&result, "out"}, &capture{&result, "err"}, options)
	result.Code = exitOf(err)
	return result
}

type stubScheduler struct {
	proc.Control
	proc.Scheduler
	queuePrint func(context.Context, proc.PrintSpec) (string, error)
}

func (s stubScheduler) QueuePrint(ctx context.Context, spec proc.PrintSpec) (string, error) {
	if s.queuePrint == nil {
		panic("stubScheduler.QueuePrint 未注入")
	}
	return s.queuePrint(ctx, spec)
}

func withFS(t *testing.T, wrap func(vmfs.System) vmfs.System) commandio.Handler {
	t.Helper()
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		set.FS = wrap(set.FS)
		p.Resources = set
		return lpcmd.Run(proc.WithContext(ctx, p), args)
	}
}

func withClosedStdin(t *testing.T) commandio.Handler {
	t.Helper()
	return func(ctx context.Context, args []string) error {
		p, err := proc.FromContext(ctx)
		if err != nil {
			return err
		}
		set, ok := p.Resources.(capability.Set)
		if !ok {
			t.Fatalf("能力视图类型: %T", p.Resources)
		}
		table, ok := set.FD.(*fd.Table)
		if !ok {
			t.Fatalf("描述符表类型: %T", set.FD)
		}
		if err := table.Close(ctx, uint64(p.ID), 0); err != nil {
			t.Fatal(err)
		}
		return lpcmd.Run(proc.WithContext(ctx, p), args)
	}
}

type readFaultFS struct{ vmfs.System }

func (r readFaultFS) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	file, err := r.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	return readFaultFile{file}, nil
}

type readFaultFile struct{ vmfs.File }

func (readFaultFile) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type closeFaultFS struct{ vmfs.System }

func (c closeFaultFS) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	file, err := c.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	return closeFaultFile{file}, nil
}

type closeFaultFile struct{ vmfs.File }

func (closeFaultFile) Close() error { return io.ErrClosedPipe }

type capture struct {
	result *commandtest.Result
	stream string
}

func (c *capture) Write(p []byte) (int, error) {
	if c.stream == "out" {
		c.result.Stdout += string(p)
	} else {
		c.result.Stderr += string(p)
	}
	return len(p), nil
}

func exitOf(err error) int {
	if err == nil {
		return 0
	}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		return int(status)
	}
	return 1
}

func (f *fixture) readObject(name string) (text string, ok bool) {
	file, err := f.fs.Open(context.Background(), name, 0, 0)
	if err != nil {
		return "", false
	}
	defer func() {
		if err := file.Close(); err != nil {
			ok = false
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func TestQueueAndDrain(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"-t", "greeting", "doc.txt"}, "")
	if r.Code != 0 || !strings.Contains(r.Stdout, "request id is lp0-0001") {
		t.Fatalf("提交应打印请求号: %+v", r)
	}

	requests, _ := f.kernel.ListPrintRequests(context.Background())
	if len(requests) != 1 {
		t.Fatalf("排队中应有 1 个请求: %v", requests)
	}

	if err := f.kernel.RunDueJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests, _ = f.kernel.ListPrintRequests(context.Background()); len(requests) != 0 {
		t.Fatalf("排空后应清空: %v", requests)
	}
	if mail, ok := f.readObject("/var/mail/sandbox"); !ok || !strings.Contains(mail, "读不到") {
		t.Fatalf("引用缺失应投递失败通知: %q", mail)
	}
}

func TestCopyAndSink(t *testing.T) {
	f := newFixture(t)

	r := f.run([]string{"-c", "-n", "2"}, "hello\n")
	if r.Code != 0 {
		t.Fatalf("提交 = %+v", r)
	}
	if err := f.kernel.RunDueJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink, ok := f.readObject("/var/spool/lp/lp0")
	if !ok || sink != "hello\n\fhello\n" {
		t.Fatalf("目的地落点 = %q ok=%v", sink, ok)
	}
}

func TestUnknownDestination(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"-d", "other", "-c"}, "x")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有打印目的地") {
		t.Fatalf("未知目的地 = %+v", r)
	}
}

func TestStdinWithoutFile(t *testing.T) {
	f := newFixture(t)
	r := f.run(nil, "from stdin\n")
	if r.Code != 0 {
		t.Fatalf("stdin 提交 = %+v", r)
	}
	if err := f.kernel.RunDueJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink, _ := f.readObject("/var/spool/lp/lp0")
	if !strings.Contains(sink, "from stdin") {
		t.Fatalf("落点应有 stdin 内容: %q", sink)
	}
}

func TestReferenceToFile(t *testing.T) {
	f := newFixture(t)

	content := "printed body\n"
	if err := stdosWriteFile(f.dir, "doc.txt", content); err != nil {
		t.Fatal(err)
	}
	r := f.run([]string{"doc.txt"}, "")
	if r.Code != 0 {
		t.Fatalf("引用提交 = %+v", r)
	}
	if err := f.kernel.RunDueJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink, ok := f.readObject("/var/spool/lp/lp0")
	if !ok || !strings.Contains(sink, "printed body") {
		t.Fatalf("落点应有引用文件内容: %q ok=%v", sink, ok)
	}
}

func TestCopyFromFile(t *testing.T) {
	f := newFixture(t)
	if err := stdosWriteFile(f.dir, "doc.txt", "copied body\n"); err != nil {
		t.Fatal(err)
	}
	r := f.run([]string{"-c", "doc.txt"}, "")
	if r.Code != 0 {
		t.Fatalf("-c 提交 = %+v", r)
	}
	if err := f.kernel.RunDueJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink, _ := f.readObject("/var/spool/lp/lp0")
	if !strings.Contains(sink, "copied body") {
		t.Fatalf("落点应有副本内容: %q", sink)
	}
}

func TestCopyMissingFileFails(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"-c", "missing.txt"}, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "读不到") {
		t.Fatalf("-c 缺文件 = %+v", r)
	}
}

func TestMultipleFilesRejected(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"a.txt", "b.txt"}, "")
	if r.Code != 2 || !strings.Contains(r.Stderr, "一次一个文件") {
		t.Fatalf("多文件 = %+v", r)
	}
}

func TestQuietSuppressesRequestID(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"-s", "-c"}, "quiet\n")
	if r.Code != 0 || strings.Contains(r.Stdout, "request id") {
		t.Fatalf("-s 应静默: %+v", r)
	}
}

func stdosWriteFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func TestMissingSchedulerPort(t *testing.T) {

	r := commandtest.Run(t, lpcmd.Run, t.TempDir(), []string{"-c"}, "x")
	if r.Code != 1 || !strings.Contains(r.Stderr, "没有打印队列") {
		t.Fatalf("缺端口 = %+v", r)
	}
}

func TestParseErrorRejectsUnknownOption(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"--bogus"}, "x")
	if r.Code != 2 || !strings.Contains(r.Stderr, "参数错误") {
		t.Fatalf("未知选项应拒绝: %+v", r)
	}
	if requests, _ := f.kernel.ListPrintRequests(context.Background()); len(requests) != 0 {
		t.Fatalf("用法错误不得排队: %v", requests)
	}
}

func TestZeroCopiesRejectedByQueue(t *testing.T) {
	f := newFixture(t)
	r := f.run([]string{"-n", "0", "-c"}, "x")
	if r.Code != 1 || !strings.Contains(r.Stderr, "份数必须为正数") {
		t.Fatalf("零份数应由队列拒绝: %+v", r)
	}
	if requests, _ := f.kernel.ListPrintRequests(context.Background()); len(requests) != 0 {
		t.Fatalf("被拒的请求不得落盘: %v", requests)
	}
}

func TestStdinReadFailure(t *testing.T) {
	f := newFixture(t)
	r := f.runHandler(withClosedStdin(t), nil, "")
	if r.Code != 1 || !strings.Contains(r.Stderr, "lp: ") {
		t.Fatalf("stdin 读故障 = %+v", r)
	}
	if requests, _ := f.kernel.ListPrintRequests(context.Background()); len(requests) != 0 {
		t.Fatalf("读故障不得排队: %v", requests)
	}
}

func TestCopySourceFailures(t *testing.T) {
	f := newFixture(t)
	if err := stdosWriteFile(f.dir, "doc.txt", "body\n"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		wrap func(vmfs.System) vmfs.System
		code int
		want string
	}{

		{"没有文件能力", func(vmfs.System) vmfs.System { return nil }, 2, "ENOSYS"},
		{"读正文失败", func(system vmfs.System) vmfs.System { return readFaultFS{system} }, 1, "lp: "},
		{"关闭失败", func(system vmfs.System) vmfs.System { return closeFaultFS{system} }, 1, "lp: "},
	}
	for _, c := range cases {
		r := f.runHandler(withFS(t, c.wrap), []string{"-c", "doc.txt"}, "")
		if r.Code != c.code || !strings.Contains(r.Stderr, c.want) {
			t.Fatalf("%s = %+v（期望退出 %d，含 %q）", c.name, r, c.code, c.want)
		}
	}
	if requests, _ := f.kernel.ListPrintRequests(context.Background()); len(requests) != 0 {
		t.Fatalf("来源故障不得排队: %v", requests)
	}
}

func TestQueueWriteFailureIsReported(t *testing.T) {
	f := newFixture(t)
	scheduler := stubScheduler{queuePrint: func(context.Context, proc.PrintSpec) (string, error) {
		return "", errno.ReadOnlyFS
	}}
	r := f.runOptions(lpcmd.Run, []string{"-c"}, "x", commandtest.Options{Control: scheduler})
	if r.Code != 1 || !strings.Contains(r.Stderr, "EROFS") || strings.Contains(r.Stdout, "request id") {
		t.Fatalf("队列落盘失败 = %+v", r)
	}
}
