package ar

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformfs "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func commandioFilesystem(dir string) (vmfs.System, error) {
	return platformfs.NewFilesystem(dir), nil
}

type faultyFS struct {
	vmfs.System
	openErr   error
	openName  string
	writeErr  error
	renameErr error
	removeErr error
	statErr   error
	stampErr  error
	chmodErr  error
	removed   []string
}

func (f *faultyFS) Open(ctx context.Context, name string, flags uint32, mode os.FileMode) (vmfs.File, error) {
	if f.openErr != nil || (f.openName != "" && strings.HasSuffix(name, f.openName)) {
		return nil, errPick(f.openErr)
	}
	file, err := f.System.Open(ctx, name, flags, mode)
	if err != nil {
		return nil, err
	}
	if f.writeErr != nil && flags&vmfs.WriteOnly != 0 {
		return faultyFile{File: file, err: f.writeErr}, nil
	}
	return file, nil
}

func (f *faultyFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return f.System.Stat(ctx, name)
}

func (f *faultyFS) Rename(ctx context.Context, from, to string) error {
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.System.Rename(ctx, from, to)
}

func (f *faultyFS) Remove(ctx context.Context, name string) error {
	f.removed = append(f.removed, name)
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.System.Remove(ctx, name)
}

func (f *faultyFS) SetTimes(ctx context.Context, name string, access, modification int64) error {
	return f.stampErr
}

func (f *faultyFS) Chmod(ctx context.Context, name string, mode os.FileMode) error {
	return f.chmodErr
}

func errPick(err error) error {
	if err != nil {
		return err
	}
	return errors.New("注入的打开失败")
}

type faultyFile struct {
	vmfs.File
	err error
}

func (f faultyFile) Write(p []byte) (int, error) { return 0, f.err }

type bareFS struct{ vmfs.System }

func newWork(t *testing.T, dir string, system vmfs.System) (*work, *strings.Builder, *strings.Builder) {
	t.Helper()
	var out, diag strings.Builder
	w := &work{
		ctx:    context.Background(),
		system: system,
		dir:    dir,
		out:    &stickyWriter{writer: &out},
		diag:   &stickyWriter{writer: &diag},
		budget: maxInputBytes,
	}
	return w, &out, &diag
}

func TestFaultyOperations(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("data"), 0o644); err != nil {
		t.Error(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}

	faulty := &faultyFS{System: base, statErr: errors.New("注入的 stat 失败")}
	w, _, diag := newWork(t, dir, faulty)
	if code := w.run(&invocation{key: 't', archive: "m"}); code != 1 || !strings.Contains(diag.String(), "stat 失败") {
		t.Fatalf("stat 故障: code=%d diag=%q", code, diag.String())
	}

	faulty = &faultyFS{System: base, renameErr: errors.New("注入的改名失败")}
	w, _, diag = newWork(t, dir, faulty)
	code := w.run(&invocation{key: 'q', archive: "q.a", members: []string{"m"}})
	if code != 1 || !strings.Contains(diag.String(), "改名失败") {
		t.Fatalf("rename 故障: code=%d diag=%q", code, diag.String())
	}
	if _, err := os.Stat(filepath.Join(w.dir, "q.a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("改名失败不应留下新档案: %v", err)
	}

	seed, _, diag := newWork(t, dir, base)
	if code := seed.run(&invocation{key: 'r', quiet: true, archive: "q2.a", members: []string{"m"}}); code != 0 || diag.Len() != 0 {
		t.Fatalf("前置档案: code=%d %s", code, diag.String())
	}
	faulty = &faultyFS{System: base, openErr: errors.New("注入的打开失败")}
	w, _, diag = newWork(t, dir, faulty)
	code = w.run(&invocation{key: 'd', archive: "q2.a", members: []string{"m"}})
	if code != 1 || !strings.Contains(diag.String(), "打开失败") {
		t.Fatalf("createTemp 故障: code=%d diag=%q", code, diag.String())
	}
}

func TestFaultyExtractWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("data"), 0o644); err != nil {
		t.Error(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}

	creator, out, diag := newWork(t, dir, base)
	if code := creator.run(&invocation{key: 'r', quiet: true, archive: "e.a", members: []string{"m"}}); code != 0 || out.Len() != 0 && diag.Len() != 0 {
		t.Fatalf("造档案失败: code=%d %s %s", code, out.String(), diag.String())
	}

	faulty := &faultyFS{System: base, writeErr: io.ErrClosedPipe}
	w, _, diag := newWork(t, dir, faulty)
	code := w.run(&invocation{key: 'x', archive: "e.a"})
	if code != 1 || !strings.Contains(diag.String(), "m:") {
		t.Fatalf("x 写失败: code=%d diag=%q", code, diag.String())
	}

	if err := os.Remove(filepath.Join(dir, "m")); err != nil {
		t.Error(err)
	}
	faulty = &faultyFS{System: base, stampErr: errors.New("注入的时间戳失败")}
	w, _, diag = newWork(t, dir, faulty)
	code = w.run(&invocation{key: 'x', archive: "e.a"})
	if code != 0 || !strings.Contains(diag.String(), "回填时间戳失败") {
		t.Fatalf("x 时间戳失败: code=%d diag=%q", code, diag.String())
	}
	if info, err := os.Stat(filepath.Join(dir, "m")); err != nil || info.Size() != 4 {
		t.Fatalf("内容仍应解出: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, "m")); err != nil {
		t.Error(err)
	}
	w, _, diag = newWork(t, dir, &bareFS{System: base})
	code = w.run(&invocation{key: 'x', archive: "e.a"})
	if code != 0 || diag.Len() != 0 {
		t.Fatalf("x 无能力: code=%d diag=%q", code, diag.String())
	}
}

func TestFaultyChmod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("data"), 0o644); err != nil {
		t.Error(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	faulty := &faultyFS{System: base, chmodErr: errors.New("注入的权限失败")}
	w, _, diag := newWork(t, dir, faulty)
	code := w.run(&invocation{key: 'r', quiet: true, archive: "c.a", members: []string{"m"}})
	if code != 0 || !strings.Contains(diag.String(), "恢复权限失败") {
		t.Fatalf("chmod 失败: code=%d diag=%q", code, diag.String())
	}
}

func TestAppendPartialFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("data"), 0o644); err != nil {
		t.Error(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, _, diag := newWork(t, dir, base)
	code := w.run(&invocation{key: 'r', quiet: true, archive: "p.a", members: []string{"m"}})
	if code != 0 {
		t.Fatalf("前置档案: code=%d %s", code, diag.String())
	}

	if err := os.WriteFile(filepath.Join(dir, "ok"), []byte("O"), 0o644); err != nil {
		t.Error(err)
	}
	w, _, diag = newWork(t, dir, base)
	code = w.run(&invocation{key: 'q', archive: "p.a", members: []string{"ok", "ghost"}})
	if code != 1 || !strings.Contains(diag.String(), "ghost") {
		t.Fatalf("q 部分失败: code=%d diag=%q", code, diag.String())
	}
	if members := readMembers(t, w.dir, "p.a"); len(members) != 2 {
		t.Fatalf("成功成员仍应入档: %v", members)
	}

	w, _, diag = newWork(t, dir, base)
	code = w.run(&invocation{key: 'q', archive: "p.a", members: []string{"ghost"}})
	if code != 1 || !strings.Contains(diag.String(), "ghost") {
		t.Fatalf("q 全部缺失: code=%d diag=%q", code, diag.String())
	}
}

func TestReplaceAnchorMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m"), []byte("data"), 0o644); err != nil {
		t.Error(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, _, _ := newWork(t, dir, base)
	w.run(&invocation{key: 'r', quiet: true, archive: "r.a", members: []string{"m"}})
	if err := os.WriteFile(filepath.Join(dir, "n"), []byte("N"), 0o644); err != nil {
		t.Error(err)
	}
	w, _, diag := newWork(t, dir, base)
	code := w.run(&invocation{key: 'r', after: true, posname: "ghost", archive: "r.a", members: []string{"n"}})
	if code != 1 || !strings.Contains(diag.String(), "ghost: 档案里没有那个成员") {
		t.Fatalf("r 锚点缺失: code=%d diag=%q", code, diag.String())
	}
}

func TestExtractDefaultPerm(t *testing.T) {
	dir := t.TempDir()
	raw := "!<arch>\n" + rawHeader("z", "0", 2) + "ok"
	if err := os.WriteFile(filepath.Join(dir, "z.a"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, _, diag := newWork(t, dir, base)
	if code := w.run(&invocation{key: 'x', archive: "z.a"}); code != 0 || diag.Len() != 0 {
		t.Fatalf("x: code=%d diag=%q", code, diag.String())
	}
	info, err := os.Stat(filepath.Join(dir, "z"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("模式 0 应回退到 0644: %v", info.Mode().Perm())
	}
}

func TestMemberIsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Error(err)
	}
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	w, _, diag := newWork(t, dir, base)
	code := w.run(&invocation{key: 'q', quiet: true, archive: "d.a", members: []string{"sub"}})
	if code != 1 || !strings.Contains(diag.String(), "是目录") {
		t.Fatalf("目录成员: code=%d diag=%q", code, diag.String())
	}
}

func TestParseArchiveCorners(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{"空名字段", craftHeader("", "1", "0", "0", "644", "0") + "\n", "没有名字"},
		{"没有终止符", craftHeader("noSlashName12345", "1", "0", "0", "644", "0") + "\n", "'/' 终止符"},
		{"BSD 长名", craftHeader("#1/14", "1", "0", "0", "644", "0") + "\n", "不支持的成员段"},
		{"SYM64", craftHeader("/SYM64/       ", "1", "0", "0", "644", "0") + "\n", "不支持的成员段"},
		{"大小非数字", craftHeader("m/", "1", "0", "0", "644", "abc") + "\n", "大小字段不是数字"},
		{"数据越界", craftHeader("m/", "1", "0", "0", "644", "100") + "xy", "数据超出档案末尾"},
	} {
		_, err := parseArchive([]byte("!<arch>\n" + test.raw))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: err=%v", test.name, err)
		}
		if !errors.Is(err, errBadArchive) && !isUnsupported(err) {
			t.Fatalf("%s: 错误身份不符: %v", test.name, err)
		}
	}

	_, err := parseArchive([]byte("!<arch>\n" + strings.Replace(craftHeader("m/", "1", "0", "0", "644", "0"), "`\n", "..\n", 1)))
	if err == nil || !strings.Contains(err.Error(), "结束符") {
		t.Fatalf("坏结束符: %v", err)
	}
	members, err := parseArchive([]byte("!<arch>\n" + craftHeader("m/", "x", "u", "g", "zz", "0")))
	if err != nil || len(members) != 1 || members[0].mtime != 0 || members[0].mode != 0 {
		t.Fatalf("宽松解析: %v %+v", err, members)
	}

	if _, err := parseArchive([]byte("short")); err == nil {
		t.Fatal("魔数错误应拒绝")
	}
	w, out, _ := newWork(t, t.TempDir(), nil)
	w.print(&invocation{key: 'p', verbose: true}, []member{{name: "m", data: []byte("x")}})
	if !strings.Contains(out.String(), "\n<member m>\n\nx") {
		t.Fatalf("p verbose: %q", out.String())
	}
}

func isUnsupported(err error) bool {
	var target *unsupportedMemberError
	return errors.As(err, &target)
}

func craftHeader(name, mtime, uid, gid, mode, size string) string {
	pad := func(s string, width int) string {
		for len(s) < width {
			s += " "
		}
		return s
	}
	return pad(name, 16) + pad(mtime, 12) + pad(uid, 6) + pad(gid, 6) + pad(mode, 8) + pad(size, 10) + "`\n"
}

func TestCreateTempExhausted(t *testing.T) {
	dir := t.TempDir()
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		name := filepath.Join(dir, ".ar-"+itoa(i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w, _, diag := newWork(t, dir, base)
	w.dir = dir
	if _, _, err := w.createTemp(dir); err == nil || !strings.Contains(err.Error(), "无法创建临时文件") {
		t.Fatalf("临时文件名耗尽: %v", err)
	}
	_ = diag
}

func TestRemoveFaultCleanup(t *testing.T) {
	dir := t.TempDir()
	base, err := commandioFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	faulty := &faultyFS{System: base, renameErr: errors.New("注入的改名失败"), removeErr: errors.New("注入的删除失败")}
	w, _, diag := newWork(t, dir, faulty)
	if err := w.writeViaTemp(filepath.Join(dir, "t.a"), []byte("x"), 0o644); err == nil || !strings.Contains(err.Error(), "改名失败") {
		t.Fatalf("主错误应保留: %v", err)
	}
	_ = diag
}
