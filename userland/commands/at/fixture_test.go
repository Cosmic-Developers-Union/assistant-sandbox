package at_test

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
	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
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
	t       *testing.T
	dir     string
	Clock   *clock.Virtual
	kernel  kernelView
	wrapped clock.Source

	nice int
}

type kernelView struct {
	kernel proc.Scheduler
	dir    string
}

func (v kernelView) QueueJob(ctx context.Context, spec proc.JobSpec) (string, error) {
	return v.kernel.QueueJob(ctx, spec)
}

type rootFS struct {
	inner vmfs.System
	dir   string
}

func newRootFS(dir string) vmfs.System { return rootFS{inner: filesystem.NewFilesystem(dir), dir: dir} }

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
	return r.inner.Symlink(ctx, guestPath(r.dir, oldname), guestPath(r.dir, newname))
}
func (r rootFS) Readlink(ctx context.Context, name string) (string, error) {
	return r.inner.Readlink(ctx, guestPath(r.dir, name))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	clk := &clock.Virtual{}

	meters, err := resources.NewSession(resources.Limits{Memory: 64 << 20, Work: 1 << 20},
		resources.Limits{Memory: 16 << 20, Work: 1 << 18})
	if err != nil {
		t.Fatal(err)
	}
	bash := executable.Func(func(ctx context.Context, p proc.Proc) (uint8, error) {

		if len(p.Args) >= 2 && p.Args[0] == "-c" {
			if out, err := p.Resources.Descriptors(); err == nil {
				if stream, err := out.Get(ctx, uint64(p.ID), 1); err == nil {
					_, _ = stream.Write([]byte("through-sleep\n"))
				}
			}
		}
		return 0, nil
	})
	registry, err := executable.New(map[string]executable.Program{"bash": bash})
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

	wrapped, err := kernel.Clock()
	if err != nil {
		t.Fatal(err)
	}
	view := kernelView{kernel: kernel, dir: dir}
	return &fixture{t: t, dir: dir, Clock: clk, kernel: view, wrapped: wrapped}
}

func (f *fixture) run(handler commandio.Handler, args []string, input string) commandtest.Result {
	return f.runOptions(handler, args, input, commandtest.Options{})
}

func (f *fixture) runOptions(handler commandio.Handler, args []string, input string, options commandtest.Options) commandtest.Result {
	if options.Clock == nil {
		options.Clock = f.Clock
	}
	if options.Control == nil {
		options.Control = kernelControl{kernel: f.kernel.kernel, filesystem: f.filesystem, nice: f.nice}
	}
	var result commandtest.Result
	err := commandtest.InvokeWith(context.Background(), handler, f.dir, args, strings.NewReader(input),
		&capture{&result, "out"}, &capture{&result, "err"}, options)
	result.Code = exitOf(err)
	return result
}

func withCapabilities(t *testing.T, run commandio.Handler, mutate func(*capability.Set)) commandio.Handler {
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
		mutate(&set)
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}

func withClosedStdin(t *testing.T, run commandio.Handler) commandio.Handler {
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
		return run(proc.WithContext(ctx, p), args)
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

type noScheduler struct{ proc.Control }

type stubScheduler struct {
	proc.Control
	proc.Scheduler
	queueJob  func(context.Context, proc.JobSpec) (string, error)
	listJobs  func(context.Context) ([]string, error)
	removeJob func(context.Context, string) (bool, error)
	readJob   func(context.Context, string) (string, error)
}

func (s stubScheduler) QueueJob(ctx context.Context, spec proc.JobSpec) (string, error) {
	if s.queueJob == nil {
		panic("stubScheduler.QueueJob 未注入")
	}
	return s.queueJob(ctx, spec)
}

func (s stubScheduler) ListJobs(ctx context.Context) ([]string, error) {
	if s.listJobs == nil {
		panic("stubScheduler.ListJobs 未注入")
	}
	return s.listJobs(ctx)
}

func (s stubScheduler) RemoveJob(ctx context.Context, id string) (bool, error) {
	if s.removeJob == nil {
		panic("stubScheduler.RemoveJob 未注入")
	}
	return s.removeJob(ctx, id)
}

func (s stubScheduler) ReadJob(ctx context.Context, id string) (string, error) {
	if s.readJob == nil {
		panic("stubScheduler.ReadJob 未注入")
	}
	return s.readJob(ctx, id)
}

type kernelControl struct {
	kernel interface {
		QueueJob(context.Context, proc.JobSpec) (string, error)
		ListJobs(context.Context) ([]string, error)
		RemoveJob(context.Context, string) (bool, error)
		ReadJob(context.Context, string) (string, error)
		QueuePrint(context.Context, proc.PrintSpec) (string, error)
		ListPrintRequests(context.Context) ([]string, error)
		RunDueJobs(context.Context) error
	}
	filesystem func() (vmfs.System, error)

	nice int
}

func (c kernelControl) Begin(proc.PID, string, []string, string, map[string]string, fd.Streams) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (kernelControl) Finish(proc.PID, proc.PID, uint8) error { return nil }
func (kernelControl) Run(context.Context, proc.RunSpec) (proc.Proc, error) {
	return proc.Proc{}, errno.NotImplemented
}
func (c kernelControl) QueueJob(ctx context.Context, spec proc.JobSpec) (string, error) {
	return c.kernel.QueueJob(ctx, spec)
}

func (c kernelControl) ListJobs(ctx context.Context) ([]string, error) {
	return c.kernel.ListJobs(ctx)
}
func (c kernelControl) RemoveJob(ctx context.Context, id string) (bool, error) {
	return c.kernel.RemoveJob(ctx, id)
}
func (c kernelControl) ReadJob(ctx context.Context, id string) (string, error) {
	return c.kernel.ReadJob(ctx, id)
}
func (c kernelControl) QueuePrint(ctx context.Context, spec proc.PrintSpec) (string, error) {
	return c.kernel.QueuePrint(ctx, spec)
}
func (c kernelControl) ListPrintRequests(ctx context.Context) ([]string, error) {
	return c.kernel.ListPrintRequests(ctx)
}
func (c kernelControl) RunDueJobs(ctx context.Context) error {
	return c.kernel.RunDueJobs(ctx)
}

func (c kernelControl) Nice(proc.PID) (int, error) { return c.nice, nil }
func (kernelControl) SetNice(proc.PID, int) error  { return errno.NotImplemented }

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

func (f *fixture) wrappedClock() (clock.Source, bool) {

	return f.wrapped, f.wrapped != nil
}

func (f *fixture) filesystem() (vmfs.System, error) {
	return newRootFS(f.dir), nil
}

func (f *fixture) readSpool(name string) (string, error) {
	system, err := f.filesystem()
	if err != nil {
		return "", err
	}
	file, err := system.Open(context.Background(), "/var/spool/at/"+name, vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(file)
	err = errors.Join(err, file.Close())
	return string(data), err
}

func (f *fixture) removeSpool(name string) error {
	system, err := f.filesystem()
	if err != nil {
		return err
	}
	return system.Remove(context.Background(), "/var/spool/at/"+name)
}

func (f *fixture) writeFile(name, content string) error {
	return os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644)
}

func (f *fixture) readMail() (string, bool) {
	system, err := f.filesystem()
	if err != nil {
		return "", false
	}
	file, err := system.Open(context.Background(), "/var/mail/sandbox", vmfs.ReadOnly, 0)
	if err != nil {
		return "", false
	}
	data, err := io.ReadAll(file)
	if errors.Join(err, file.Close()) != nil {
		return "", false
	}
	return string(data), true
}
