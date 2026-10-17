package chmod

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func reset(t *testing.T, path string, perm fs.FileMode) {
	t.Helper()
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

func ptr[T any](value T) *T { return &value }

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func write(t *testing.T, path string, perm fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), perm); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

func TestOctalModes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file")
	write(t, target, 0o644)
	if result := commandtest.Run(t, Run, dir, []string{"600", "file"}, ""); result.Code != 0 {
		t.Fatalf("600: %+v", result)
	}
	if got := mode(t, target); got != 0o600 {
		t.Fatalf("得到 %o", got)
	}

	if result := commandtest.Run(t, Run, dir, []string{"4755", "file"}, ""); result.Code != 0 {
		t.Fatalf("4755: %+v", result)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 || info.Mode()&fs.ModeSetuid == 0 {
		t.Fatalf("4755 得到 %v", info.Mode())
	}

	for _, text := range []string{"8", "77777", "999999", "0o644"} {
		if result := commandtest.Run(t, Run, dir, []string{text, "file"}, ""); result.Code != 1 {
			t.Fatalf("非法模式 %q 应失败: %+v", text, result)
		}
	}

	if result := commandtest.Run(t, Run, dir, []string{"644", "absent"}, ""); result.Code != 1 || result.Stderr == "" {
		t.Fatalf("缺失文件: %+v", result)
	}
}

func TestSymbolicModes(t *testing.T) {
	for _, test := range []struct {
		name  string
		start fs.FileMode
		mode  string
		want  fs.FileMode
	}{
		{"加写位", 0o644, "u+w", 0o644},
		{"去掉组读", 0o644, "g-r", 0o604},
		{"等于直接赋值", 0o777, "a=", 0o000},
		{"等于赋指定位", 0o600, "go=rx", 0o655},
		{"多子句逗号分隔", 0o000, "u+rw,g+r", 0o640},
		{"按位或", 0o640, "a+r", 0o644},
		{"权限拷贝 g=u", 0o750, "g=u", 0o770},
		{"权限拷贝 o=g", 0o760, "o=g", 0o766},
		{"X 对非可执行文件不给执行位", 0o644, "a+X", 0o644},
		{"X 对可执行文件给执行位", 0o744, "go+X", 0o755},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "file")
			write(t, target, test.start)
			if result := commandtest.Run(t, Run, dir, []string{test.mode, "file"}, ""); result.Code != 0 {
				t.Fatalf("%s: %+v", test.mode, result)
			}
			if got := mode(t, target); got != test.want {
				t.Fatalf("%s 得到 %o，期望 %o", test.mode, got, test.want)
			}
		})
	}

	dir := t.TempDir()
	write(t, filepath.Join(dir, "file"), 0o644)
	if result := commandtest.Run(t, Run, dir, []string{"u+q", "file"}, ""); result.Code != 1 || result.Stderr == "" {
		t.Fatalf("非法权限字符: %+v", result)
	}
}

func TestDirectoryXAndRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "outside"), 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(dir, "outside", "file")
	write(t, outsideFile, 0o600)
	write(t, filepath.Join(dir, "sub", "deep", "file"), 0o600)

	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(dir, "sub", "escape")); err != nil {
		t.Skip("该平台不可创建符号链接")
	}
	if result := commandtest.Run(t, Run, dir, []string{"-R", "a+X", "sub"}, ""); result.Code != 0 {
		t.Fatalf("-R: %+v", result)
	}

	if got := mode(t, filepath.Join(dir, "sub")); got != 0o711 {
		t.Fatalf("目录 = %o（X 应给目录执行位）", got)
	}
	if got := mode(t, filepath.Join(dir, "sub", "deep")); got != 0o711 {
		t.Fatalf("子目录 = %o", got)
	}

	if got := mode(t, filepath.Join(dir, "sub", "deep", "file")); got != 0o600 {
		t.Fatalf("普通文件 = %o", got)
	}

	if got := mode(t, filepath.Join(dir, "outside")); got != 0o700 {
		t.Fatalf("-R 顺着符号链接改到了外面: %o", got)
	}
	if got := mode(t, outsideFile); got != 0o600 {
		t.Fatalf("外部文件被改: %o", got)
	}
}

func TestSpecialBits(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file")
	write(t, target, 0o755)
	if result := commandtest.Run(t, Run, dir, []string{"u+s", "file"}, ""); result.Code != 0 {
		t.Fatalf("u+s: %+v", result)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&fs.ModeSetuid == 0 || info.Mode()&fs.ModeSetgid != 0 {
		t.Fatalf("u+s 得到 %v", info.Mode())
	}
	if result := commandtest.Run(t, Run, dir, []string{"g+s", "file"}, ""); result.Code != 0 {
		t.Fatalf("g+s: %+v", result)
	}
	if info, err = os.Stat(target); err != nil || info.Mode()&fs.ModeSetgid == 0 {
		t.Fatalf("g+s 得到 %v %v", info.Mode(), err)
	}

	if result := commandtest.Run(t, Run, dir, []string{"a=", "file"}, ""); result.Code != 0 {
		t.Fatalf("a=: %+v", result)
	}
	if info, err = os.Stat(target); err != nil || info.Mode().Perm() != 0 || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid) != 0 {
		t.Fatalf("a= 得到 %v %v", info.Mode(), err)
	}

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o777); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"+t", "sub"}, ""); result.Code != 0 {
		t.Fatalf("+t: %+v", result)
	}
	if info, err = os.Stat(sub); err != nil || info.Mode()&fs.ModeSticky == 0 {
		t.Fatalf("+t 得到 %v %v", info.Mode(), err)
	}

	if result := commandtest.Run(t, Run, dir, []string{",", "file"}, ""); result.Code != 1 || result.Stderr == "" {
		t.Fatalf("空子句: %+v", result)
	}
}

func TestWhoOmittedRespectsUmask(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "file")
	write(t, target, 0o000)

	if result := commandtest.RunWith(t, Run, dir, []string{"+w", "file"}, "", commandtest.Options{
		Umask: ptr(fs.FileMode(0o022)),
	}); result.Code != 0 {
		t.Fatalf("+w: %+v", result)
	}
	if got := mode(t, target); got != 0o200 {
		t.Fatalf("掩码 022 下 +w 得到 %o，期望 0200", got)
	}

	reset(t, target, 0o000)
	if result := commandtest.RunWith(t, Run, dir, []string{"+w", "file"}, "", commandtest.Options{
		Umask: ptr(fs.FileMode(0o000)),
	}); result.Code != 0 {
		t.Fatalf("+w: %+v", result)
	}
	if got := mode(t, target); got != 0o222 {
		t.Fatalf("掩码 000 下 +w 得到 %o，期望 0222", got)
	}

	reset(t, target, 0o000)
	if result := commandtest.RunWith(t, Run, dir, []string{"go+w", "file"}, "", commandtest.Options{
		Umask: ptr(fs.FileMode(0o022)),
	}); result.Code != 0 {
		t.Fatalf("go+w: %+v", result)
	}
	if got := mode(t, target); got != 0o022 {
		t.Fatalf("写了 who 时得到 %o，期望 0022", got)
	}
}

func TestUsageAndOptions(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "file"), 0o644)
	if result := commandtest.Run(t, Run, dir, []string{"644"}, ""); result.Code != 2 {
		t.Fatalf("缺文件操作数: %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"--unknown", "644", "file"}, ""); result.Code != 2 {
		t.Fatalf("未知选项: %+v", result)
	}

	result := commandtest.Run(t, Run, dir, []string{"-f", "644", "absent"}, "")
	if result.Code != 1 || result.Stderr != "" {
		t.Fatalf("-f 应静默但失败: %+v", result)
	}

	result = commandtest.Run(t, Run, dir, []string{"-v", "600", "file"}, "")
	if result.Code != 0 || result.Stdout == "" {
		t.Fatalf("-v: %+v", result)
	}
}
