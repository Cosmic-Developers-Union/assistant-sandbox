package locale

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

type rootFS struct{ inner vmfs.System }

func newRootFS(dir string) vmfs.System { return rootFS{platformfs.NewFilesystem(dir)} }

func guestPath(name string) string {
	if name == "/" {
		return "."
	}
	return strings.TrimPrefix(name, "/")
}

func (r rootFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	return r.inner.Open(ctx, guestPath(name), flags, mode)
}
func (r rootFS) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return r.inner.Stat(ctx, guestPath(name))
}
func (r rootFS) Lstat(ctx context.Context, name string) (fs.FileInfo, error) {
	return r.inner.Lstat(ctx, guestPath(name))
}
func (r rootFS) ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	return r.inner.ReadDir(ctx, guestPath(name))
}
func (r rootFS) Mkdir(ctx context.Context, name string, mode fs.FileMode) error {
	return r.inner.Mkdir(ctx, guestPath(name), mode)
}
func (r rootFS) Remove(ctx context.Context, name string) error {
	return r.inner.Remove(ctx, guestPath(name))
}
func (r rootFS) Rename(ctx context.Context, from, to string) error {
	return r.inner.Rename(ctx, guestPath(from), guestPath(to))
}
func (r rootFS) Link(ctx context.Context, from, to string) error {
	return r.inner.Link(ctx, guestPath(from), guestPath(to))
}
func (r rootFS) Symlink(ctx context.Context, target, link string) error {
	return r.inner.Symlink(ctx, guestPath(target), guestPath(link))
}
func (r rootFS) Readlink(ctx context.Context, name string) (string, error) {
	return r.inner.Readlink(ctx, guestPath(name))
}

func withSystem(t *testing.T, system vmfs.System, run commandio.Handler) commandio.Handler {
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
		set.FS = system
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}

func runWith(t *testing.T, options commandtest.Options, args ...string) commandtest.Result {
	t.Helper()
	dir := t.TempDir()
	return commandtest.RunWith(t, withSystem(t, newRootFS(dir), Run), dir, args, "", options)
}

func envOnly(values map[string]string) commandtest.Options {
	return commandtest.Options{Env: values}
}

func TestLocaleDefaults(t *testing.T) {
	r := runWith(t, commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("缺省 = %+v", r)
	}
	want := "LC_CTYPE=\"C\"\nLC_COLLATE=\"C\"\nLC_MESSAGES=\"C\"\nLC_MONETARY=\"C\"\nLC_NUMERIC=\"C\"\nLC_TIME=\"C\"\n"
	if r.Stdout != want {
		t.Fatalf("缺省输出 = %q", r.Stdout)
	}
}

func TestLocaleEnvironment(t *testing.T) {

	options := envOnly(map[string]string{"LC_ALL": "C.UTF-8"})
	r := runWith(t, options)
	if r.Code != 0 || strings.Count(r.Stdout, "C.UTF-8") != 6 {
		t.Fatalf("LC_ALL = %+v", r)
	}

	options = envOnly(map[string]string{"LC_TIME": "C.UTF-8", "LANG": "C"})
	r = runWith(t, options)
	if r.Code != 0 || !strings.Contains(r.Stdout, "LC_TIME=\"C.UTF-8\"\n") || strings.Count(r.Stdout, "LC_COLLATE=\"C\"\n") != 1 {
		t.Fatalf("LC_TIME+LANG = %+v", r)
	}

	options = envOnly(map[string]string{"LC_ALL": "C", "LC_NUMERIC": "C.UTF-8"})
	r = runWith(t, options)
	if r.Code != 0 || !strings.Contains(r.Stdout, "LC_NUMERIC=\"C\"\n") {
		t.Fatalf("LC_ALL 优先 = %+v", r)
	}

	options = envOnly(map[string]string{"LC_ALL": "", "LANG": "POSIX"})
	r = runWith(t, options)
	if r.Code != 0 || !strings.Contains(r.Stdout, "LC_CTYPE=\"POSIX\"\n") {
		t.Fatalf("空值穿透 = %+v", r)
	}
}

func TestLocaleRejectsUnresolvableValues(t *testing.T) {
	for _, values := range []map[string]string{
		{"LANG": "en_US.UTF-8"},
		{"LC_ALL": "fr_FR"},
		{"LC_COLLATE": "../../etc"},
		{"LC_ALL": ".."},
	} {
		r := runWith(t, envOnly(values))
		if r.Code != 1 || !strings.Contains(r.Stderr, "非法语言环境") {
			t.Fatalf("%v = %+v", values, r)
		}
		if r.Stdout != "" {
			t.Fatalf("%v 不应有输出: %+v", values, r)
		}
	}
}

func TestLocaleNamedCategories(t *testing.T) {
	options := envOnly(map[string]string{"LC_NUMERIC": "C.UTF-8"})
	r := runWith(t, options, "LC_NUMERIC")
	if r.Code != 0 || r.Stdout != "LC_NUMERIC=\"C.UTF-8\"\n" {
		t.Fatalf("单类别 = %+v", r)
	}
	r = runWith(t, options, "LC_CTYPE", "LC_NUMERIC")
	if r.Code != 0 || r.Stdout != "LC_CTYPE=\"C\"\nLC_NUMERIC=\"C.UTF-8\"\n" {
		t.Fatalf("多类别 = %+v", r)
	}

	options = envOnly(map[string]string{"LC_CTYPE": "de_DE", "LC_NUMERIC": "C"})
	r = runWith(t, options, "LC_NUMERIC", "LC_CTYPE")
	if r.Code != 1 || r.Stdout != "" {
		t.Fatalf("非法名字 = %+v", r)
	}

	if r := runWith(t, commandtest.Options{}, "LANG"); r.Code != 1 || !strings.Contains(r.Stderr, "不是语言环境类别名") {
		t.Fatalf("LANG 操作数 = %+v", r)
	}
	if r := runWith(t, commandtest.Options{}, "charmap"); r.Code != 1 {
		t.Fatalf("charmap 无 -k = %+v", r)
	}
}

func TestLocaleSections(t *testing.T) {
	r := runWith(t, commandtest.Options{}, "-c", "LC_CTYPE")
	if r.Code != 0 || r.Stdout != "LC_CTYPE\nLC_CTYPE=\"C\"\n" {
		t.Fatalf("-c 单类别 = %+v", r)
	}
	r = runWith(t, commandtest.Options{}, "-c")
	want := "LC_CTYPE\nLC_CTYPE=\"C\"\nLC_COLLATE\nLC_COLLATE=\"C\"\nLC_MESSAGES\nLC_MESSAGES=\"C\"\nLC_MONETARY\nLC_MONETARY=\"C\"\nLC_NUMERIC\nLC_NUMERIC=\"C\"\nLC_TIME\nLC_TIME=\"C\"\n"
	if r.Code != 0 || r.Stdout != want {
		t.Fatalf("-c 缺省 = %+v", r)
	}
}

func TestLocaleArchive(t *testing.T) {
	dir := t.TempDir()
	system := newRootFS(dir)
	handler := withSystem(t, system, Run)

	r := commandtest.RunWith(t, handler, dir, []string{"-a"}, "", commandtest.Options{})
	if r.Code != 0 || r.Stdout != "C\nC.UTF-8\nPOSIX\n" {
		t.Fatalf("无归档目录 -a = %+v", r)
	}

	if err := os.MkdirAll(dir+"/usr/lib/locale/zh_X", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/usr/lib/locale/notes", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r = commandtest.RunWith(t, handler, dir, []string{"-a"}, "", commandtest.Options{})
	if r.Code != 0 || r.Stdout != "C\nC.UTF-8\nPOSIX\nzh_X\n" {
		t.Fatalf("有归档 -a = %+v", r)
	}

	options := commandtest.Options{Env: map[string]string{"LC_CTYPE": "zh_X"}}
	r = commandtest.RunWith(t, handler, dir, []string{"LC_CTYPE"}, "", options)
	if r.Code != 0 || r.Stdout != "LC_CTYPE=\"zh_X\"\n" {
		t.Fatalf("归档名解析 = %+v", r)
	}
}

func TestLocaleKeywords(t *testing.T) {
	options := commandtest.Options{}
	r := runWith(t, options, "-k", "charmap")
	if r.Code != 0 || r.Stdout != "charmap=\"ANSI_X3.4-1968\"\n" {
		t.Fatalf("charmap C = %+v", r)
	}
	options = envOnly(map[string]string{"LC_ALL": "C.UTF-8"})
	r = runWith(t, options, "-k", "charmap")
	if r.Code != 0 || r.Stdout != "charmap=\"UTF-8\"\n" {
		t.Fatalf("charmap C.UTF-8 = %+v", r)
	}

	options = envOnly(map[string]string{"LC_CTYPE": "C.utf8"})
	r = runWith(t, options, "-k", "charmap")
	if r.Code != 0 || r.Stdout != "charmap=\"UTF-8\"\n" {
		t.Fatalf("charmap C.utf8 = %+v", r)
	}

	r = runWith(t, commandtest.Options{}, "-k", "LC_MESSAGES", "-c")
	if r.Code != 0 || r.Stdout != "LC_MESSAGES\nLC_MESSAGES=\"C\"\n" {
		t.Fatalf("-k 类别 = %+v", r)
	}

	for _, name := range []string{"yesexpr", "decimal_point", "codeset"} {
		if r := runWith(t, commandtest.Options{}, "-k", name); r.Code != 1 || !strings.Contains(r.Stderr, "客体没有该关键字") {
			t.Fatalf("-k %s = %+v", name, r)
		}
	}
}

func TestLocaleArchiveCharmap(t *testing.T) {
	dir := t.TempDir()
	system := newRootFS(dir)
	handler := withSystem(t, system, Run)
	if err := os.MkdirAll(dir+"/usr/lib/locale/zh_X", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/usr/lib/locale/zh_X/locale.conf", []byte("charmap=GBK\nLC_MESSAGES.yesexpr=是\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options := commandtest.Options{Env: map[string]string{"LC_CTYPE": "zh_X"}}
	r := commandtest.RunWith(t, handler, dir, []string{"-k", "charmap"}, "", options)
	if r.Code != 0 || r.Stdout != "charmap=\"GBK\"\n" {
		t.Fatalf("归档 charmap = %+v", r)
	}

	if err := os.MkdirAll(dir+"/usr/lib/locale/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	options = commandtest.Options{Env: map[string]string{"LC_CTYPE": "empty"}}
	r = commandtest.RunWith(t, handler, dir, []string{"-k", "charmap"}, "", options)
	if r.Code != 1 || !strings.Contains(r.Stderr, "非法语言环境") {
		t.Fatalf("缺 locale.conf = %+v", r)
	}

	if err := os.WriteFile(dir+"/usr/lib/locale/zh_X/locale.conf", []byte("LC_MESSAGES.yesexpr=是\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	options = commandtest.Options{Env: map[string]string{"LC_CTYPE": "zh_X"}}
	r = commandtest.RunWith(t, handler, dir, []string{"-k", "charmap"}, "", options)
	if r.Code != 1 || !strings.Contains(r.Stderr, "归档没有声明 charmap") {
		t.Fatalf("无 charmap 键 = %+v", r)
	}

	options = commandtest.Options{Env: map[string]string{"LC_CTYPE": "../../etc"}}
	r = commandtest.RunWith(t, handler, dir, []string{"-k", "charmap"}, "", options)
	if r.Code != 1 || !strings.Contains(r.Stderr, "非法语言环境") {
		t.Fatalf("路径名字 = %+v", r)
	}
}

type statFailFS struct{ vmfs.System }

func (statFailFS) Stat(context.Context, string) (fs.FileInfo, error) {
	return nil, errors.New("注入的 stat 失败")
}
func (statFailFS) ReadDir(context.Context, string) ([]fs.DirEntry, error) {
	return nil, errors.New("注入的读目录失败")
}

func TestLocaleFilesystemErrors(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, statFailFS{platformfs.NewFilesystem(dir)}, Run)

	options := commandtest.Options{Env: map[string]string{"LC_CTYPE": "not_a_locale"}}
	if err := commandtest.InvokeWith(t.Context(), handler, dir, []string{"LC_CTYPE"}, strings.NewReader(""), io.Discard, io.Discard, options); err == nil {
		t.Fatal("归档探测失败应上抛")
	}

	if err := commandtest.Invoke(t.Context(), handler, dir, []string{"-a"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("读目录失败应上抛")
	}
}

func TestLocaleWriteFailures(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{nil, {"-c"}, {"-a"}, {"-k", "charmap"}, {"-k", "LC_TIME"}, {"-k", "LC_TIME", "-c"}} {
		if err := commandtest.Invoke(t.Context(), Run, dir, args, strings.NewReader(""), commandtest.FailWriter{}, io.Discard); err == nil {
			t.Fatalf("%v 写失败应上抛", args)
		}
	}
}

func TestLocaleUsage(t *testing.T) {
	options := commandtest.Options{}
	if r := runWith(t, options, "-a", "LC_CTYPE"); r.Code != 2 {
		t.Fatalf("-a 带名字 = %+v", r)
	}
	if r := runWith(t, options, "-a", "-k"); r.Code != 2 {
		t.Fatalf("-a 与 -k = %+v", r)
	}
	if r := runWith(t, options, "-k"); r.Code != 2 {
		t.Fatalf("-k 无名字 = %+v", r)
	}
	if r := runWith(t, options, "--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}
