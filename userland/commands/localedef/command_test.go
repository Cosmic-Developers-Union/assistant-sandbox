package localedef

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
	localecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/locale"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
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
func (r rootFS) Link(ctx context.Context, name, to string) error {
	return r.inner.Link(ctx, guestPath(r.dir, name), guestPath(r.dir, to))
}
func (r rootFS) Symlink(ctx context.Context, target, link string) error {
	return r.inner.Symlink(ctx, guestPath(r.dir, target), guestPath(r.dir, link))
}
func (r rootFS) Readlink(ctx context.Context, name string) (string, error) {
	return r.inner.Readlink(ctx, guestPath(r.dir, name))
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

func runWith(t *testing.T, input string, args ...string) commandtest.Result {
	t.Helper()
	dir := t.TempDir()
	handler := withSystem(t, newRootFS(dir), Run)
	return commandtest.RunWith(t, handler, dir, args, input, commandtest.Options{})
}

func mustConf(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(dir + "/usr/lib/locale/" + name + "/locale.conf")
	if err != nil {
		t.Fatalf("读取 locale.conf: %v", err)
	}
	return string(data)
}

func TestLocaledefCreatesArchive(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, newRootFS(dir), Run)
	source := "LC_MESSAGES\nyesexpr \"^[yY]\"\nnoexpr \"^[nN]\"\nEND LC_MESSAGES\n"
	r := commandtest.RunWith(t, handler, dir, []string{"testlocale"}, source, commandtest.Options{})
	if r.Code != 0 || r.Stdout != "" {
		t.Fatalf("落地 = %+v", r)
	}
	content := mustConf(t, dir, "testlocale")
	if !strings.Contains(content, "LC_MESSAGES.yesexpr=^[yY]\n") || !strings.Contains(content, "LC_MESSAGES.noexpr=^[nN]\n") {
		t.Fatalf("归档内容 = %q", content)
	}

	if info, err := os.Stat(dir + "/usr/lib/locale/testlocale"); err != nil || !info.IsDir() {
		t.Fatalf("归档目录: %v", err)
	}
}

func TestLocaledefLocaleIntegration(t *testing.T) {
	dir := t.TempDir()
	system := newRootFS(dir)
	if r := commandtest.RunWith(t, withSystem(t, system, Run), dir, []string{"zh_X"},
		"LC_MESSAGES\nyesexpr \"^是\"\nEND LC_MESSAGES\n", commandtest.Options{}); r.Code != 0 {
		t.Fatalf("localedef = %+v", r)
	}
	localeRun := withSystem(t, system, localecmd.Run)
	r := commandtest.RunWith(t, localeRun, dir, []string{"-a"}, "", commandtest.Options{})
	if r.Code != 0 || !strings.Contains(r.Stdout, "zh_X\n") {
		t.Fatalf("locale -a = %+v", r)
	}

	options := commandtest.Options{Env: map[string]string{"LC_CTYPE": "zh_X"}}
	r = commandtest.RunWith(t, localeRun, dir, []string{"LC_CTYPE"}, "", options)
	if r.Code != 0 || r.Stdout != "LC_CTYPE=\"zh_X\"\n" {
		t.Fatalf("locale 解析归档 = %+v", r)
	}
}

func TestLocaledefDirectives(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, newRootFS(dir), Run)
	source := "# 这行是缺省注释\ncomment_char %\nescape_char \\\n% 这行是注释\nLC_MESSAGES\nyesexpr \"^[yY]\"\nEND LC_MESSAGES\n"
	r := commandtest.RunWith(t, handler, dir, []string{"directives"}, source, commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("指令 = %+v", r)
	}
	if content := mustConf(t, dir, "directives"); !strings.Contains(content, "LC_MESSAGES.yesexpr=^[yY]\n") {
		t.Fatalf("归档内容 = %q", content)
	}
}

func TestLocaledefCharmap(t *testing.T) {
	dir := t.TempDir()
	system := newRootFS(dir)
	handler := withSystem(t, system, Run)
	if err := os.WriteFile(dir+"/charmap_UTF-8", []byte("<code_set_name> \"UTF-8\"\n<U0041> /x41 A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := commandtest.RunWith(t, handler, dir, []string{"-f", "charmap_UTF-8", "withcharmap"},
		"", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("charmap = %+v", r)
	}
	if content := mustConf(t, dir, "withcharmap"); !strings.Contains(content, "charmap=UTF-8\n") {
		t.Fatalf("charmap 键 = %q", content)
	}

	if err := os.WriteFile(dir+"/ISO-8859-2", []byte("# 只有码位表\n<U00E9> /xe9 e\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = commandtest.RunWith(t, handler, dir, []string{"-f", "ISO-8859-2", "fallback"},
		"", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("charmap 兜底 = %+v", r)
	}
	if content := mustConf(t, dir, "fallback"); !strings.Contains(content, "charmap=ISO-8859-2\n") {
		t.Fatalf("charmap 兜底键 = %q", content)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"nocharmap"}, "", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("无 -f = %+v", r)
	}
	if content := mustConf(t, dir, "nocharmap"); strings.Contains(content, "charmap=") {
		t.Fatalf("无 -f 不应有 charmap 键 = %q", content)
	}
}

func TestLocaledefUnrecognizedInstructions(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, newRootFS(dir), Run)
	source := "LC_MESSAGES\nyesexpr \"^[yY]\"\nEND LC_MESSAGES\ncopy \"C\"\nLC_ADDRESS\ngyepong\n"

	r := commandtest.RunWith(t, handler, dir, []string{"rejected"}, source, commandtest.Options{})
	if r.Code != 1 || r.Stdout != "" {
		t.Fatalf("不认识指令 = %+v", r)
	}
	if strings.Count(r.Stderr, "不认识的指令") != 3 {
		t.Fatalf("逐条报告 = %+v", r)
	}
	if _, err := os.Stat(dir + "/usr/lib/locale/rejected"); !os.IsNotExist(err) {
		t.Fatalf("失败不应产出归档: %v", err)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"-c", "lenient"}, source, commandtest.Options{})
	if r.Code != 0 || !strings.Contains(r.Stderr, "-c 降级为警告") {
		t.Fatalf("-c 降级 = %+v", r)
	}
	if content := mustConf(t, dir, "lenient"); !strings.Contains(content, "LC_MESSAGES.yesexpr=^[yY]\n") {
		t.Fatalf("-c 归档内容 = %q", content)
	}
}

func TestLocaledefStructuralErrors(t *testing.T) {
	for _, source := range []string{
		"END LC_MESSAGES\n",
		"LC_MESSAGES\nyesexpr x\nEND LC_CTYPE\n",
		"LC_MESSAGES\nEND LC_MESSAGES\nLC_CTYPE\n",
		"LC_MESSAGES\nyesexpr x\n",
	} {
		for _, lenient := range []bool{false, true} {
			args := []string{"broken"}
			if lenient {
				args = []string{"-c", "broken"}
			}
			dir := t.TempDir()
			r := commandtest.RunWith(t, withSystem(t, newRootFS(dir), Run), dir, args, source, commandtest.Options{})
			if r.Code != 1 {
				t.Fatalf("源 %q (lenient=%v) = %+v", source, lenient, r)
			}
		}
	}

	dir := t.TempDir()
	r := commandtest.RunWith(t, withSystem(t, newRootFS(dir), Run), dir, []string{"broken"},
		"comment_char %%\n", commandtest.Options{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "单个字符操作数") {
		t.Fatalf("comment_char 形状 = %+v", r)
	}
}

func TestLocaledefExistingArchive(t *testing.T) {
	dir := t.TempDir()
	handler := withSystem(t, newRootFS(dir), Run)
	r := commandtest.RunWith(t, handler, dir, []string{"twice"}, "", commandtest.Options{})
	if r.Code != 0 {
		t.Fatalf("第一次 = %+v", r)
	}
	r = commandtest.RunWith(t, handler, dir, []string{"twice"}, "", commandtest.Options{})
	if r.Code != 1 || !strings.Contains(r.Stderr, "语言环境归档已存在") {
		t.Fatalf("第二次 = %+v", r)
	}
}

func TestLocaledefPathnameRules(t *testing.T) {
	for _, name := range []string{"a/b", "..", "."} {
		r := runWith(t, "", name)
		if r.Code != 1 || !strings.Contains(r.Stderr, "归档名必须是单个名字") {
			t.Fatalf("归档名 %q = %+v", name, r)
		}
	}
	if r := runWith(t, ""); r.Code != 2 {
		t.Fatalf("无操作数 = %+v", r)
	}
	if r := runWith(t, "", "a", "b"); r.Code != 2 {
		t.Fatalf("多操作数 = %+v", r)
	}
	if r := runWith(t, "", "--bogus"); r.Code != 2 {
		t.Fatalf("未知选项 = %+v", r)
	}
}

func TestLocaledefInputSources(t *testing.T) {
	dir := t.TempDir()
	system := newRootFS(dir)
	handler := withSystem(t, system, Run)
	if err := os.WriteFile(dir+"/source", []byte("LC_MESSAGES\nyesexpr \"^[yY]\"\nEND LC_MESSAGES\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := commandtest.RunWith(t, handler, dir, []string{"-i", "source", "fromfile"}, "", commandtest.Options{})
	if r.Code != 0 || !strings.Contains(mustConf(t, dir, "fromfile"), "LC_MESSAGES.yesexpr=^[yY]\n") {
		t.Fatalf("-i 文件 = %+v", r)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"-i", "-", "fromstdin"}, "LC_MESSAGES\nyesexpr \"^[nN]\"\nEND LC_MESSAGES\n", commandtest.Options{})
	if r.Code != 0 || !strings.Contains(mustConf(t, dir, "fromstdin"), "LC_MESSAGES.yesexpr=^[nN]\n") {
		t.Fatalf("-i - = %+v", r)
	}

	r = commandtest.RunWith(t, handler, dir, []string{"-i", "missing", "never"}, "", commandtest.Options{})
	if r.Code != 2 {
		t.Fatalf("源缺失 = %+v", r)
	}
}

type failFS struct {
	vmfs.System
	mkdirErr error
	writeErr error
}

func (f failFS) Mkdir(ctx context.Context, name string, mode fs.FileMode) error {
	if f.mkdirErr != nil {
		return f.mkdirErr
	}
	return f.System.Mkdir(ctx, name, mode)
}

func (f failFS) Open(ctx context.Context, name string, flags uint32, mode fs.FileMode) (vmfs.File, error) {
	if f.writeErr != nil && flags&vmfs.WriteOnly != 0 {
		return nil, f.writeErr
	}
	return f.System.Open(ctx, name, flags, mode)
}

func TestLocaledefWriteFailures(t *testing.T) {
	dir := t.TempDir()
	base := platformfs.NewFilesystem(dir)

	mkdirHandler := withSystem(t, rootFS{inner: failFS{System: base, mkdirErr: errors.New("注入的建目录失败")}, dir: dir}, Run)
	if err := commandtest.Invoke(t.Context(), mkdirHandler, dir, []string{"never"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("建目录失败应上抛")
	}

	writeHandler := withSystem(t, rootFS{inner: failFS{System: base, writeErr: errors.New("注入的写失败")}, dir: dir}, Run)
	if err := commandtest.Invoke(t.Context(), writeHandler, dir, []string{"never"}, strings.NewReader(""), io.Discard, io.Discard); err == nil {
		t.Fatal("写归档失败应上抛")
	}
}
