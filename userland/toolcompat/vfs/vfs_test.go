package vfs

import (
	"bytes"
	"errors"
	"io"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func bind(t *testing.T) func() {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return Bind(system, "/", map[string]string{"LANG": "C"})
}

func TestUnboundCapability(t *testing.T) {
	if _, _, err := Sys(); err == nil {
		t.Fatal("未绑定应报错")
	}
	if _, err := Open("x"); err == nil {
		t.Fatal("Open 未绑定应报错")
	}
	if _, err := Stat("x"); err == nil {
		t.Fatal("Stat 未绑定应报错")
	}
	if _, err := Lstat("x"); err == nil {
		t.Fatal("Lstat 未绑定应报错")
	}
	if _, err := ReadDir("x"); err == nil {
		t.Fatal("ReadDir 未绑定应报错")
	}
	if err := Mkdir("x", 0700); err == nil {
		t.Fatal("Mkdir 未绑定应报错")
	}
	if err := Remove("x"); err == nil {
		t.Fatal("Remove 未绑定应报错")
	}
	if err := Rename("x", "y"); err == nil {
		t.Fatal("Rename 未绑定应报错")
	}
	if err := Link("x", "y"); err == nil {
		t.Fatal("Link 未绑定应报错")
	}
	if err := Symlink("x", "y"); err == nil {
		t.Fatal("Symlink 未绑定应报错")
	}
	if _, err := Readlink("x"); err == nil {
		t.Fatal("Readlink 未绑定应报错")
	}
	if _, err := Getwd(); err == nil {
		t.Fatal("Getwd 未绑定应报错")
	}
	if _, err := ReadFile("x"); err == nil {
		t.Fatal("ReadFile 未绑定应报错")
	}
	if Getenv("LANG") != "" || Environ() != nil {
		t.Fatal("未绑定时环境为空")
	}

	if err := Chmod("x", 0600); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("Chmod 未绑定应报 ENOSYS: %v", err)
	}

	if err := Chtimes("x", 0, 0); !errors.Is(err, errno.NotImplemented) {
		t.Fatalf("Chtimes 未绑定应报 ENOSYS: %v", err)
	}
}

func TestFileOperations(t *testing.T) {
	release := bind(t)
	defer release()

	if got, err := Getwd(); err != nil || got != "/" {
		t.Fatalf("Getwd=%q err=%v", got, err)
	}
	if Getenv("LANG") != "C" || len(Environ()) != 1 {
		t.Fatal("环境查询错误")
	}
	if Path("rel") != "/rel" || Path("/abs") != "/abs" {
		t.Fatalf("Path 解析错误: %q %q", Path("rel"), Path("/abs"))
	}

	if err := WriteFile("data", []byte("value"), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := ReadFile("data")
	if err != nil || string(content) != "value" {
		t.Fatalf("ReadFile=%q err=%v", content, err)
	}
	file, err := Open("data")
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(file)
	if err != nil || string(read) != "value" {
		t.Fatalf("Open 读取 %q err=%v", read, err)
	}
	if info, err := file.Stat(); err != nil || info.Size() != 5 {
		t.Fatalf("Stat=%v err=%v", info, err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}

	created, err := Create("created")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := created.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Error(err)
	}
	if _, err := OpenFile("created", int(O_RDONLY|O_CREATE|O_EXCL), 0600); !errors.Is(err, ErrExist) {
		t.Fatalf("O_EXCL 应报已存在: %v", err)
	}

	if err := MkdirAll("a/b/c", 0700); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll("a/b/c", 0700); err != nil {
		t.Fatalf("已存在应成功: %v", err)
	}
	if err := Mkdir("a", 0700); !errors.Is(err, ErrExist) {
		t.Fatalf("Mkdir 已存在应报 EEXIST: %v", err)
	}
	entries, err := ReadDir("a/b")
	if err != nil || len(entries) != 1 || entries[0].Name() != "c" {
		t.Fatalf("ReadDir=%v err=%v", entries, err)
	}
	if info, err := Lstat("a"); err != nil || !info.IsDir() {
		t.Fatalf("Lstat=%v err=%v", info, err)
	}

	if err := Rename("created", "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := Link("renamed", "hard"); err != nil {
		t.Fatal(err)
	}
	if err := Symlink("renamed", "soft"); err != nil {
		t.Fatal(err)
	}
	if target, err := Readlink("soft"); err != nil || target != "renamed" {
		t.Fatalf("Readlink=%q err=%v", target, err)
	}
	if _, err := Readlink("renamed"); !errors.Is(err, errno.Invalid) {
		t.Fatalf("非链接应报 EINVAL: %v", err)
	}
	if err := Remove("hard"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll("renamed"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open("missing"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("缺失文件应报 ErrNotExist: %v", err)
	}
	if err := RemoveAll("missing"); err != nil {
		t.Fatalf("缺失路径的 RemoveAll 应成功: %v", err)
	}
}

func TestReadFileRejectsOversize(t *testing.T) {
	release := bind(t)
	defer release()
	if err := WriteFile("big", bytes.Repeat([]byte("x"), 4<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile("big"); err == nil {
		t.Fatal("超过 4 MiB 的读取应失败")
	}

	if err := WriteFile("exact", bytes.Repeat([]byte("y"), 4<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, err := ReadFile("exact"); err != nil || len(data) != 4<<20 {
		t.Fatalf("恰好 4 MiB 应完整读出: len=%d err=%v", len(data), err)
	}
}

func TestRemoveAllRecursive(t *testing.T) {
	release := bind(t)
	defer release()
	if err := MkdirAll("tree/leaf", 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile("tree/leaf/file", []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll("tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := Lstat("tree"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("tree 应被删除: %v", err)
	}
}

func TestNotExistAndExistRecognizeBothIdentities(t *testing.T) {
	hostMissing := &stdfs.PathError{Op: "stat", Path: "new.txt", Err: stdfs.ErrNotExist}
	hostExisting := &stdfs.PathError{Op: "open", Path: "new.txt", Err: stdfs.ErrExist}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"kernel-not-exist", ErrNotExist, true},
		{"kernel-wrapped-not-exist", errno.Wrap(ErrNotExist, "打开"), true},
		{"host-not-exist", hostMissing, true},
		{"host-exist", hostExisting, false},
		{"nil", nil, false},
		{"other", ErrPermission, false},
	} {
		if got := NotExist(tc.err); got != tc.want {
			t.Errorf("NotExist(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"kernel-exist", ErrExist, true},
		{"host-exist", hostExisting, true},
		{"host-not-exist", hostMissing, false},
		{"nil", nil, false},
	} {
		if got := Exist(tc.err); got != tc.want {
			t.Errorf("Exist(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type noModesSystem struct{ vmfs.System }

func TestChmodUsesBackendCapability(t *testing.T) {
	release := bind(t)
	defer release()
	if err := WriteFile("mode", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Chmod("mode", 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := Stat("mode")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("模式位 %v %v", info.Mode(), err)
	}

	system, _, err := Sys()
	if err != nil {
		t.Fatal(err)
	}
	releasePlain := Bind(noModesSystem{System: system}, "/", nil)
	defer releasePlain()
	if err := Chmod("mode", 0o644); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("后端无能力应报 ENOTSUP: %v", err)
	}
}

func TestChtimesUsesBackendCapability(t *testing.T) {
	release := bind(t)
	defer release()
	if err := WriteFile("stamp", []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	const when = 1_700_000_000_000_000_000
	if err := Chtimes("stamp", when, when); err != nil {
		t.Fatal(err)
	}
	info, err := Stat("stamp")
	if err != nil || info.ModTime().UnixNano() != when {
		t.Fatalf("mtime = %d, %v", info.ModTime().UnixNano(), err)
	}

	system, _, err := Sys()
	if err != nil {
		t.Fatal(err)
	}
	releasePlain := Bind(noModesSystem{System: system}, "/", nil)
	defer releasePlain()
	if err := Chtimes("stamp", when, when); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("后端无能力应报 ENOTSUP: %v", err)
	}
}
