package packagefs

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

type memorySource struct {
	blobs map[string][]byte
	err   error
	opens int
}

func newSource() *memorySource { return &memorySource{blobs: map[string][]byte{}} }

func (s *memorySource) put(content string) (string, int64) {
	identity := digest.Content([]byte(content))
	s.blobs[identity] = []byte(content)
	return identity, int64(len(content))
}

func (s *memorySource) Open(ctx context.Context, identity string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.opens++
	if s.err != nil {
		return nil, s.err
	}
	data, ok := s.blobs[identity]
	if !ok {
		return nil, errno.Wrap(errno.NotFound, "内容不存在")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func buildFS(t *testing.T) (*FS, *memorySource) {
	t.Helper()
	source := newSource()
	hello, helloSize := source.put("#!/bin/sh\necho hi\n")
	motd, motdSize := source.put("welcome\n")
	secret, secretSize := source.put("top secret\n")
	entries := []Entry{
		{Path: "/bin", Kind: KindDirectory, Mode: 0o755},
		{Path: "/bin/hello", Kind: KindFile, Mode: 0o755, Size: helloSize, Digest: hello},
		{Path: "/bin/link", Kind: KindSymlink, Mode: 0o777, Target: "hello"},
		{Path: "/bin/up", Kind: KindSymlink, Mode: 0o777, Target: "../etc/motd"},
		{Path: "/bin/loop", Kind: KindSymlink, Mode: 0o777, Target: "loop"},
		{Path: "/etc/motd", Kind: KindFile, Mode: 0o644, Size: motdSize, Digest: motd},
		{Path: "/etc/secret", Kind: KindFile, Mode: 0o000, Size: secretSize, Digest: secret},
		{Path: "/dangling", Kind: KindSymlink, Mode: 0o777, Target: "/missing"},
		{Path: "/dirlink", Kind: KindSymlink, Mode: 0o777, Target: "/etc"},
	}
	fsys, err := New(Manifest{Version: Version, Entries: entries}, source)
	if err != nil {
		t.Fatal(err)
	}
	return fsys, source
}

func readFile(t *testing.T, fsys *FS, path string) string {
	t.Helper()
	handle, err := fsys.Open(context.Background(), path, vmfsReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const vmfsReadOnly = 0

func TestOpenFollowsSymlinksAndReadsContent(t *testing.T) {
	fsys, _ := buildFS(t)
	if got := readFile(t, fsys, "/bin/hello"); got != "#!/bin/sh\necho hi\n" {
		t.Fatalf("内容=%q", got)
	}

	if got := readFile(t, fsys, "/bin/link"); got != "#!/bin/sh\necho hi\n" {
		t.Fatalf("相对链接=%q", got)
	}
	if got := readFile(t, fsys, "/bin/up"); got != "welcome\n" {
		t.Fatalf("带 .. 的链接=%q", got)
	}

	if got := readFile(t, fsys, "/dirlink/motd"); got != "welcome\n" {
		t.Fatalf("链接后接子路径=%q", got)
	}
}

func TestSeekAndStat(t *testing.T) {
	fsys, _ := buildFS(t)
	handle, err := fsys.Open(context.Background(), "/etc/motd", vmfsReadOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "come\n" {
		t.Fatalf("定位后读到 %q", rest)
	}
	info, err := handle.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name() != "motd" || info.Size() != 8 || info.Mode().Perm() != 0o644 || info.IsDir() {
		t.Fatalf("元数据=%+v", info)
	}

	directory, err := fsys.Stat(context.Background(), "/bin")
	if err != nil {
		t.Fatal(err)
	}
	if !directory.IsDir() || directory.Size() != 0 || !directory.Mode().IsDir() {
		t.Fatalf("目录元数据=%+v", directory)
	}
}

func TestLstatDoesNotFollowSymlink(t *testing.T) {
	fsys, _ := buildFS(t)
	ctx := context.Background()
	link, err := fsys.Lstat(ctx, "/bin/link")
	if err != nil {
		t.Fatal(err)
	}
	if link.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat 未报告符号链接: %v", link.Mode())
	}
	followed, err := fsys.Stat(ctx, "/bin/link")
	if err != nil {
		t.Fatal(err)
	}
	if followed.Mode()&stdfs.ModeSymlink != 0 || followed.Size() == 0 {
		t.Fatalf("Stat 未跟随链接: %+v", followed)
	}
	target, err := fsys.Readlink(ctx, "/bin/link")
	if err != nil || target != "hello" {
		t.Fatalf("Readlink=%q err=%v", target, err)
	}
	if _, err := fsys.Readlink(ctx, "/etc/motd"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非链接 Readlink=%v want EINVAL", err)
	}
}

func TestReadDirIsSortedAndTyped(t *testing.T) {
	fsys, _ := buildFS(t)
	entries, err := fsys.ReadDir(context.Background(), "/bin")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 4 || names[0] != "hello" || names[1] != "link" || names[2] != "loop" || names[3] != "up" {
		t.Fatalf("目录项=%v", names)
	}
	if entries[0].IsDir() || entries[1].Type()&stdfs.ModeSymlink == 0 {
		t.Fatalf("目录项类型不对: %+v", entries)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name() != "hello" || info.Size() == 0 {
		t.Fatalf("目录项信息=%+v", info)
	}
	if _, err := fsys.ReadDir(context.Background(), "/etc/motd"); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("对文件列举=%v want ENOTDIR", err)
	}
}

func TestOpenRejections(t *testing.T) {
	fsys, _ := buildFS(t)
	ctx := context.Background()
	for _, tc := range []struct {
		label string
		path  string
		flags uint32
		want  error
	}{
		{"写标志", "/etc/motd", 1, errno.ReadOnlyFS},
		{"创建标志", "/etc/new", 64, errno.ReadOnlyFS},
		{"目录", "/bin", 0, errno.IsDirectory},
		{"不可读", "/etc/secret", 0, errno.Permission},
		{"缺失", "/nope", 0, errno.NotFound},
		{"悬空链接", "/dangling", 0, errno.NotFound},
		{"链接环", "/bin/loop", 0, errno.LinkLoop},
		{"相对路径", "etc/motd", 0, errno.Invalid},
		{"上溯组件", "/etc/../bin/hello", 0, errno.Invalid},
	} {
		if _, err := fsys.Open(ctx, tc.path, tc.flags, 0); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v want %v", tc.label, err, tc.want)
		}
	}
}

func TestWriteOperationsAreRejected(t *testing.T) {
	fsys, _ := buildFS(t)
	ctx := context.Background()
	for label, err := range map[string]error{
		"Mkdir":   fsys.Mkdir(ctx, "/new", 0o755),
		"Remove":  fsys.Remove(ctx, "/etc/motd"),
		"Rename":  fsys.Rename(ctx, "/etc/motd", "/etc/motd2"),
		"Link":    fsys.Link(ctx, "/etc/motd", "/etc/motd2"),
		"Symlink": fsys.Symlink(ctx, "/etc/motd", "/etc/motd2"),
	} {
		if !errors.Is(err, errno.ReadOnlyFS) {
			t.Fatalf("%s: err=%v want EROFS", label, err)
		}
	}
	handle, err := fsys.Open(ctx, "/etc/motd", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := handle.Write([]byte("x")); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatalf("写入句柄=%v want EROFS", err)
	}
}

func TestIntegrityChecks(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label   string
		size    int64
		digest  string
		content string
	}{
		{"长度不符", 4, digest.Content([]byte("payload")), "payload"},
		{"身份不符", 7, digest.Content([]byte("other")), "payload"},
	} {
		source := newSource()
		source.blobs[tc.digest] = []byte(tc.content)
		fsys, err := New(Manifest{Version: Version, Entries: []Entry{
			{Path: "/f", Kind: KindFile, Mode: 0o644, Size: tc.size, Digest: tc.digest},
		}}, source)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.Open(ctx, "/f", 0, 0); !errors.Is(err, errno.Version) {
			t.Fatalf("%s: err=%v want EPROTO", tc.label, err)
		}
	}

	source := newSource()
	missing := digest.Content([]byte("nowhere"))
	fsys, err := New(Manifest{Version: Version, Entries: []Entry{
		{Path: "/f", Kind: KindFile, Mode: 0o644, Size: 7, Digest: missing},
	}}, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Open(ctx, "/f", 0, 0); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失内容=%v want ENOENT", err)
	}
}

func TestManifestValidation(t *testing.T) {
	source := newSource()
	payload, size := source.put("payload")
	for _, tc := range []struct {
		label   string
		entry   Entry
		want    error
		version int
		source  Source
	}{
		{"空来源", Entry{Path: "/f", Kind: KindFile, Size: size, Digest: payload}, errno.Invalid, Version, nil},
		{"版本不符", Entry{Path: "/f", Kind: KindFile, Size: size, Digest: payload}, errno.Version, Version + 1, source},
		{"相对路径", Entry{Path: "f", Kind: KindFile, Size: size, Digest: payload}, errno.Invalid, Version, source},
		{"上溯组件", Entry{Path: "/a/../f", Kind: KindFile, Size: size, Digest: payload}, errno.Invalid, Version, source},
		{"根目录条目", Entry{Path: "/", Kind: KindDirectory}, errno.Invalid, Version, source},
		{"未知种类", Entry{Path: "/f"}, errno.Invalid, Version, source},
		{"文件缺身份", Entry{Path: "/f", Kind: KindFile, Size: size}, errno.Invalid, Version, source},
		{"文件大小为负", Entry{Path: "/f", Kind: KindFile, Size: -1, Digest: payload}, errno.Invalid, Version, source},
		{"文件带链接目标", Entry{Path: "/f", Kind: KindFile, Size: size, Digest: payload, Target: "x"}, errno.Invalid, Version, source},
		{"链接缺目标", Entry{Path: "/f", Kind: KindSymlink}, errno.Invalid, Version, source},
		{"链接带身份", Entry{Path: "/f", Kind: KindSymlink, Target: "x", Digest: payload}, errno.Invalid, Version, source},
		{"目录带身份", Entry{Path: "/d", Kind: KindDirectory, Digest: payload}, errno.Invalid, Version, source},
	} {
		fsys, err := New(Manifest{Version: tc.version, Entries: []Entry{tc.entry}}, tc.source)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err=%v want %v", tc.label, err, tc.want)
		}
		if fsys != nil {
			t.Fatalf("%s: 校验失败时不应返回文件系统", tc.label)
		}
	}
}

func TestManifestStructuralErrors(t *testing.T) {
	source := newSource()
	payload, size := source.put("payload")
	file := Entry{Path: "/a/b", Kind: KindFile, Size: size, Digest: payload}

	if _, err := New(Manifest{Version: Version, Entries: []Entry{
		{Path: "/a", Kind: KindFile, Size: size, Digest: payload},
		{Path: "/a/b", Kind: KindFile, Size: size, Digest: payload},
	}}, source); !errors.Is(err, errno.NotDirectory) {
		t.Fatalf("前缀是文件: err=%v want ENOTDIR", err)
	}

	if _, err := New(Manifest{Version: Version, Entries: []Entry{file, file}}, source); !errors.Is(err, errno.Exist) {
		t.Fatalf("重复路径: err=%v want EEXIST", err)
	}

	fsys, err := New(Manifest{Version: Version, Entries: []Entry{
		file,
		{Path: "/a", Kind: KindDirectory, Mode: 0o700},
	}}, source)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := fsys.Stat(context.Background(), "/a")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Mode().Perm() != 0o700 {
		t.Fatalf("显式目录权限=%v", directory.Mode().Perm())
	}
}

func TestClosedHandleAndCancellation(t *testing.T) {
	fsys, _ := buildFS(t)
	ctx := context.Background()
	handle, err := fsys.Open(ctx, "/etc/motd", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Read(make([]byte, 1)); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后读=%v want EBADF", err)
	}
	if _, err := handle.Seek(0, io.SeekStart); !errors.Is(err, errno.BadHandle) {
		t.Fatalf("关闭后定位=%v want EBADF", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fsys.Open(cancelled, "/etc/motd", 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消打开=%v", err)
	}
	if _, err := fsys.Stat(cancelled, "/etc/motd"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消 stat=%v", err)
	}
	if _, err := fsys.ReadDir(cancelled, "/"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消列举=%v", err)
	}
	if _, err := fsys.Readlink(cancelled, "/bin/link"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消读链接=%v", err)
	}
}

func TestSourceErrorsArePropagated(t *testing.T) {
	fsys, source := buildFS(t)
	source.err = errno.Wrap(errno.Range, "来源不可用")
	if _, err := fsys.Open(context.Background(), "/etc/motd", 0, 0); !errors.Is(err, errno.Range) {
		t.Fatalf("err=%v want ERANGE", err)
	}
}

func TestKindNames(t *testing.T) {
	for kind, want := range map[Kind]string{
		KindDirectory: "directory", KindFile: "file", KindSymlink: "symlink",
	} {
		if got := kind.String(); got != want {
			t.Fatalf("%v=%q want %q", uint8(kind), got, want)
		}
	}
	if got := Kind(99).String(); got != "kind(99)" {
		t.Fatalf("未知种类=%q", got)
	}
}

func TestMetadataErrorPaths(t *testing.T) {
	fsys, _ := buildFS(t)
	ctx := context.Background()
	if _, err := fsys.Stat(ctx, "/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("Stat 缺失=%v want ENOENT", err)
	}
	if _, err := fsys.Lstat(ctx, "/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("Lstat 缺失=%v want ENOENT", err)
	}
	if _, err := fsys.ReadDir(ctx, "/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("ReadDir 缺失=%v want ENOENT", err)
	}
	if _, err := fsys.Readlink(ctx, "/nope"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("Readlink 缺失=%v want ENOENT", err)
	}

	link, err := fsys.Lstat(ctx, "/dirlink")
	if err != nil {
		t.Fatal(err)
	}
	if !link.Mode().IsRegular() && link.Mode()&stdfs.ModeSymlink == 0 {
		t.Fatalf("Lstat 模式=%v", link.Mode())
	}

	if !link.ModTime().IsZero() || link.Sys() != nil {
		t.Fatalf("时间戳=%v Sys=%v", link.ModTime(), link.Sys())
	}

	entries, err := fsys.ReadDir(ctx, "/etc")
	if err != nil {
		t.Fatal(err)
	}
	seenDirectory := false
	for _, entry := range entries {
		if entry.Name() == "motd" && entry.Type() != 0 {
			t.Fatalf("文件目录项类型=%v", entry.Type())
		}
		if entry.Name() == "motd" {
			seenDirectory = true
		}
	}
	if !seenDirectory {
		t.Fatalf("目录项=%v", entries)
	}
	root, err := fsys.ReadDir(ctx, "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range root {
		if entry.Name() == "bin" && entry.Type() != stdfs.ModeDir {
			t.Fatalf("目录目录项类型=%v", entry.Type())
		}
	}
}
