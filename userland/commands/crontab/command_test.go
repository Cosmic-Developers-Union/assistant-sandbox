package crontab

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
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
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

func withSystem(t *testing.T, dir string, run commandio.Handler) commandio.Handler {
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
		set.Identity = identity.SingleUser{}
		p.Resources = set
		return run(proc.WithContext(ctx, p), args)
	}
}

func runWith(t *testing.T, input string, args ...string) commandtest.Result {
	t.Helper()
	dir := t.TempDir()
	handler := withSystem(t, dir, Run)
	return commandtest.RunWith(t, handler, dir, args, input,
		commandtest.Options{Identity: identity.SingleUser{}})
}

func spoolPath(dir string) string { return filepath.Join(dir, "var/cron/tabs/sandbox") }

func TestCrontabInstall(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, Run)
	options := commandtest.Options{Identity: identity.SingleUser{}}

	source := "* * * * * echo hi\n0 0 * * 0 weekly\n"
	r := commandtest.RunWith(t, handler, dir, nil, source, options)
	if r.Code != 0 || r.Stdout != "" {
		t.Fatalf("stdin 安装 = %+v", r)
	}
	if data, err := os.ReadFile(spoolPath(dir)); err != nil || string(data) != source {
		t.Fatalf("spool 内容 = %q, %v", data, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "jobs"), []byte("1 2 3 4 5 cmd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = commandtest.RunWith(t, handler, dir, []string{"jobs"}, "", options)
	if r.Code != 0 {
		t.Fatalf("文件安装 = %+v", r)
	}
	if data, err := os.ReadFile(spoolPath(dir)); err != nil || string(data) != "1 2 3 4 5 cmd\n" {
		t.Fatalf("替换后 = %q, %v", data, err)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"-"}, "dash\n", options)
	if r.Code != 0 {
		t.Fatalf("- 操作数 = %+v", r)
	}
	if data, err := os.ReadFile(spoolPath(dir)); err != nil || string(data) != "dash\n" {
		t.Fatalf("替换后 = %q, %v", data, err)
	}

	r = commandtest.RunWith(t, handler, dir, nil, "", options)
	if r.Code != 0 {
		t.Fatalf("空安装 = %+v", r)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "var/cron/tabs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "sandbox" {
		t.Fatalf("spool 目录项 = %v", entries)
	}
}

func TestCrontabListRemove(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, Run)
	options := commandtest.Options{Identity: identity.SingleUser{}}

	if r := commandtest.RunWith(t, handler, dir, []string{"-l"}, "", options); r.Code != 1 || !strings.Contains(r.Stderr, "没有 crontab") {
		t.Fatalf("缺 spool -l = %+v", r)
	}
	if r := commandtest.RunWith(t, handler, dir, []string{"-r"}, "", options); r.Code != 1 || !strings.Contains(r.Stderr, "没有 crontab") {
		t.Fatalf("缺 spool -r = %+v", r)
	}

	source := "* * * * * job\n"
	if r := commandtest.RunWith(t, handler, dir, nil, source, options); r.Code != 0 {
		t.Fatalf("安装 = %+v", r)
	}
	if r := commandtest.RunWith(t, handler, dir, []string{"-l"}, "", options); r.Code != 0 || r.Stdout != source {
		t.Fatalf("-l = %+v", r)
	}

	if r := commandtest.RunWith(t, handler, dir, []string{"-r"}, "", options); r.Code != 0 {
		t.Fatalf("-r = %+v", r)
	}
	if _, err := os.Stat(spoolPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("spool 应已删除: %v", err)
	}
}

func TestCrontabUsernames(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, dir, Run)
	options := commandtest.Options{Identity: identity.SingleUser{}}
	if r := commandtest.RunWith(t, handler, dir, []string{"-l", "sandbox"}, "", options); r.Code != 1 || !strings.Contains(r.Stderr, "没有 crontab") {
		t.Fatalf("-l sandbox = %+v", r)
	}
	if r := commandtest.RunWith(t, handler, dir, []string{"-l", "root"}, "", options); r.Code != 1 || !strings.Contains(r.Stderr, "crontab: root: 没有这个用户") {
		t.Fatalf("-l root = %+v", r)
	}
	if r := commandtest.RunWith(t, handler, dir, []string{"-r", "nobody"}, "", options); r.Code != 1 || !strings.Contains(r.Stderr, "crontab: nobody: 没有这个用户") {
		t.Fatalf("-r nobody = %+v", r)
	}

	if err := os.WriteFile(filepath.Join(dir, "root"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := commandtest.RunWith(t, handler, dir, []string{"root"}, "", options); r.Code != 0 {
		t.Fatalf("文件安装 = %+v", r)
	}
}

func TestCrontabEditorRejected(t *testing.T) {
	for _, args := range [][]string{{"-e"}, {"-e", "sandbox"}} {
		if r := runWith(t, "", args...); r.Code != 1 || !strings.Contains(r.Stderr, "crontab: 客体没有编辑器，-e 不支持") {
			t.Fatalf("%v = %+v", args, r)
		}
	}
}

func TestCrontabUsage(t *testing.T) {
	for _, args := range [][]string{
		{"-l", "-r"},
		{"-e", "-l"},
		{"-l", "a", "b"},
		{"a", "b"},
		{"--bogus"},
	} {
		if r := runWith(t, "", args...); r.Code != 2 {
			t.Fatalf("%v = %+v", args, r)
		}
	}
}

func TestCrontabRequiresIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := commandtest.Invoke(t.Context(), Run, dir, []string{"-l"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("缺身份能力应失败")
	}
}

func TestCrontabInstallFailures(t *testing.T) {
	options := commandtest.Options{Identity: identity.SingleUser{}}
	var dir string
	for name, fault := range map[string]func(base *platformfs.Filesystem) failFS{
		"mkdir": func(base *platformfs.Filesystem) failFS {
			return failFS{System: base, mkdirErr: errors.New("注入的建目录失败")}
		},
		"write": func(base *platformfs.Filesystem) failFS {
			return failFS{System: base, writeErr: errors.New("注入的写失败")}
		},
		"rename": func(base *platformfs.Filesystem) failFS {
			return failFS{System: base, renameErr: errors.New("注入的改名失败")}
		},
	} {

		dir := t.TempDir()
		base := platformfs.NewFilesystem(dir)
		handler := harness(t, dir, rootFS{inner: fault(base), dir: dir}, identity.SingleUser{})
		r := commandtest.RunWith(t, handler, dir, nil, "x\n", options)
		if r.Code == 0 {
			t.Fatalf("%s 故障应上抛: %+v", name, r)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "var/cron/tabs"))
	if err != nil {

		return
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Fatalf("临时文件残留: %v", entries)
		}
	}
}
