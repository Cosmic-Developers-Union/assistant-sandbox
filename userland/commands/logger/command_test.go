package logger

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

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

func (r rootFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	return r.inner.Open(ctx, guestPath(r.dir, name), flags, mode)
}
func (r rootFS) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return r.inner.Stat(ctx, guestPath(r.dir, name))
}
func (r rootFS) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	return r.inner.Lstat(ctx, guestPath(r.dir, name))
}
func (r rootFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	return r.inner.ReadDir(ctx, guestPath(r.dir, name))
}
func (r rootFS) Mkdir(ctx context.Context, name string, mode fs.FileMode) error {
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
func (r rootFS) Symlink(ctx context.Context, target, link string) error {
	return r.inner.Symlink(ctx, guestPath(r.dir, target), guestPath(r.dir, link))
}
func (r rootFS) Readlink(ctx context.Context, name string) (string, error) {
	return r.inner.Readlink(ctx, guestPath(r.dir, name))
}

type fixedClock struct{ nanos int64 }

func (c fixedClock) Wall(context.Context) (int64, error)     { return c.nanos, nil }
func (fixedClock) Monotonic(context.Context) (uint64, error) { return 0, nil }
func (fixedClock) Sleep(context.Context, uint64) error       { return nil }

func withSystem(t *testing.T, dir string, source clock.Source) commandio.Handler {
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
		set.FS = newRootFS(dir)
		set.Clock = source
		p.Resources = set
		return Run(proc.WithContext(ctx, p), args)
	}
}

func runWith(t *testing.T, input string, args ...string) commandtest.Result {
	t.Helper()
	dir := t.TempDir()
	handler := withSystem(t, dir, fixedClock{})
	return commandtest.RunWith(t, handler, dir, args, input, commandtest.Options{})
}

func logContent(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "var/log/messages"))
	if err != nil {
		t.Fatalf("读取系统日志: %v", err)
	}
	return string(data)
}

func TestLoggerWritesMessages(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, fixedClock{})
	options := commandtest.Options{}

	r := commandtest.RunWith(t, handler, dir, []string{"hello", "world"}, "", options)
	if r.Code != 0 || r.Stdout != "" {
		t.Fatalf("消息 = %+v", r)
	}
	if got := logContent(t, dir); got != "Jan 1 00:00:00 logger: hello world\n" {
		t.Fatalf("日志行 = %q", got)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"second"}, "", options)
	if r.Code != 0 {
		t.Fatalf("追加 = %+v", r)
	}
	if got := logContent(t, dir); !strings.HasSuffix(got, "Jan 1 00:00:00 logger: second\n") || strings.Count(got, "\n") != 2 {
		t.Fatalf("追加后 = %q", got)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"-t", "sshd", "line"}, "", options)
	if r.Code != 0 {
		t.Fatalf("-t = %+v", r)
	}
	if got := logContent(t, dir); !strings.HasSuffix(got, "Jan 1 00:00:00 sshd: line\n") {
		t.Fatalf("-t 后 = %q", got)
	}
}

func TestLoggerTimestamp(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, fixedClock{nanos: 3600 * 1e9})
	r := commandtest.RunWith(t, handler, dir, []string{"boot"}, "", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("时钟 = %+v", r)
	}
	if got := logContent(t, dir); got != "Jan 1 01:00:00 logger: boot\n" {
		t.Fatalf("时间戳 = %q", got)
	}
}

func TestLoggerStdinAndFile(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, fixedClock{})
	options := commandtest.Options{}
	r := commandtest.RunWith(t, handler, dir, nil, "first line\nsecond line\n", options)
	if r.Code != 0 {
		t.Fatalf("stdin = %+v", r)
	}
	got := logContent(t, dir)
	if !strings.Contains(got, "logger: first line\n") || !strings.Contains(got, "logger: second line\n") {
		t.Fatalf("逐行 = %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "events"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = commandtest.RunWith(t, handler, dir, []string{"-f", "events"}, "", options)
	if r.Code != 0 {
		t.Fatalf("-f = %+v", r)
	}
	got = logContent(t, dir)
	if !strings.Contains(got, "logger: a\n") || !strings.Contains(got, "logger: b\n") {
		t.Fatalf("-f 逐行 = %q", got)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"-f", "missing"}, "", options)
	if r.Code == 0 {
		t.Fatalf("-f 缺失 = %+v", r)
	}
}

func TestLoggerPriorityLines(t *testing.T) {
	for _, want := range []struct{ priority, prefix string }{
		{"3", "<3>"},
		{"192", "<192>"},
		{"user.err", "<11>"},
		{"USER.ERR", "<11>"},
		{"kern.warning", "<4>"},
		{"local7.debug", "<191>"},
		{"mail.info", "<22>"},
	} {
		dir := t.TempDir()
		handler := withSystem(t, dir, fixedClock{})
		r := commandtest.RunWith(t, handler, dir, []string{"-p", want.priority, "msg"}, "", commandtest.Options{})
		if r.Code != 0 {
			t.Fatalf("-p %s = %+v", want.priority, r)
		}
		if got := logContent(t, dir); got != want.prefix+"Jan 1 00:00:00 logger: msg\n" {
			t.Fatalf("-p %s 行 = %q", want.priority, got)
		}
	}

	dir := t.TempDir()
	r := commandtest.RunWith(t, withSystem(t, dir, fixedClock{}), dir, []string{"msg"}, "", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("无 -p = %+v", r)
	}
	if got := logContent(t, dir); !strings.HasPrefix(got, "Jan 1 00:00:00") {
		t.Fatalf("无 -p 行 = %q", got)
	}
}

func TestLoggerInvalidPriorities(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, fixedClock{})
	for _, priority := range []string{"0", "193", "-1", "bogus", "kern", "user.bogus", "nonsuch.err", "user.err.extra"} {
		r := commandtest.RunWith(t, handler, dir, []string{"-p", priority, "msg"}, "", commandtest.Options{})
		if r.Code != 1 || !strings.Contains(r.Stderr, "非法优先级") {
			t.Fatalf("-p %s = %+v", priority, r)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "var/log/messages")); !os.IsNotExist(err) {
		t.Fatalf("非法 priority 不应产出日志: %v", err)
	}
}

func TestLoggerRejectedOptions(t *testing.T) {
	for _, name := range []string{"i", "d", "m"} {
		r := runWith(t, "", "-"+name)
		if r.Code != 1 || !strings.Contains(r.Stderr, "logger: 客体没有 syslog 设施，-"+name+" 不支持") {
			t.Fatalf("-%s = %+v", name, r)
		}
	}
}

func TestLoggerUsage(t *testing.T) {
	if r := runWith(t, "", "-f", "x", "msg"); r.Code != 2 || !strings.Contains(r.Stderr, "-f 与消息操作数不能同给") {
		t.Fatalf("-f + 操作数 = %+v", r)
	}
	if r := runWith(t, "", "--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestLoggerDirectoryCreated(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, fixedClock{})
	r := commandtest.RunWith(t, handler, dir, []string{"first"}, "", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("首次 = %+v", r)
	}
	if info, err := os.Stat(filepath.Join(dir, "var/log")); err != nil || !info.IsDir() {
		t.Fatalf("/var/log: %v", err)
	}

	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "var/log"), []byte("占位"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commandtest.Invoke(t.Context(), withSystem(t, dir2, fixedClock{}), dir2, []string{"x"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("占位文件应导致错误")
	}
}

type failFS struct {
	vmfs.System
	openErr  error
	writeErr error
}

func (f failFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	if f.writeErr != nil && flags&vmfs.WriteOnly != 0 {
		return brokenFile{file}, nil
	}
	return file, nil
}

type brokenFile struct{ vmfs.File }

func (brokenFile) Write([]byte) (int, error) { return 0, errors.New("注入的写失败") }

func TestLoggerWriteFailures(t *testing.T) {
	dir := t.TempDir()
	base := platformfs.NewFilesystem(dir)
	for name, fault := range map[string]failFS{
		"open":  {System: base, openErr: errors.New("注入的打开失败")},
		"write": {System: base, writeErr: errors.New("注入的写失败")},
	} {
		handler := func(ctx context.Context, args []string) error {
			p, err := proc.FromContext(ctx)
			if err != nil {
				return err
			}
			set, ok := p.Resources.(capability.Set)
			if !ok {
				t.Fatalf("能力视图类型: %T", p.Resources)
			}
			set.FS = rootFS{inner: fault, dir: dir}
			set.Clock = fixedClock{}
			p.Resources = set
			return Run(proc.WithContext(ctx, p), args)
		}
		r := commandtest.RunWith(t, handler, dir, []string{"msg"}, "", commandtest.Options{})
		if r.Code != 1 || !strings.Contains(r.Stderr, "logger: ") {
			t.Fatalf("%s 故障 = %+v", name, r)
		}
	}
}
