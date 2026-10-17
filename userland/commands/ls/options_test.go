package ls

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	base := time.Now().Add(-time.Hour)
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{"a.txt": 0o600, "run.sh": 0o700, ".hidden": 0o600} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "inner"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}

	for index, name := range []string{"sub", "a.txt", "run.sh"} {
		target := filepath.Join(dir, name)
		stamp := base.Add(time.Duration(index+1) * time.Minute)
		if err := os.Chtimes(target, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("sub", filepath.Join(dir, "link")); err != nil {
		t.Skip("该平台不可创建符号链接")
	}
	return dir
}

func TestSelectionOptions(t *testing.T) {
	dir := fixture(t)
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"默认不显示隐藏项", nil, "a.txt\nlink\nrun.sh\nsub\n"},
		{"-A 显示隐藏项", []string{"-A"}, ".hidden\na.txt\nlink\nrun.sh\nsub\n"},
		{"-a 含当前目录", []string{"-a"}, ".\n.hidden\na.txt\nlink\nrun.sh\nsub\n"},
		{"-d 只列目录本身", []string{"-d", "sub"}, "sub\n"},
		{"-d 与多个操作数", []string{"-d", "sub", "a.txt"}, "a.txt\nsub\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, test.args, "")
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 code=%d stdout=%q stderr=%q", test.args, result.Code, result.Stdout, result.Stderr)
			}
		})
	}

	result := commandtest.Run(t, Run, dir, []string{"-f"}, "")
	if result.Code != 0 {
		t.Fatalf("-f 得到 %+v", result)
	}
	if got := strings.Fields(result.Stdout); len(got) != 5 {
		t.Fatalf("-f 应列出 5 项（含隐藏项）: %q", result.Stdout)
	}

	result = commandtest.Run(t, Run, dir, []string{"-R", "sub"}, "")
	want := "sub:\ndeeper\ninner\n\nsub/deeper:\n"
	if result.Code != 0 || result.Stdout != want {
		t.Fatalf("-R 得到 %q，期望 %q", result.Stdout, want)
	}
}

func TestSortOptions(t *testing.T) {
	dir := fixture(t)
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"-S 大的在前", []string{"-S"}, "sub\nrun.sh\na.txt\nlink\n"},
		{"-t 按修改时间（link 最新）", []string{"-t"}, "link\nrun.sh\na.txt\nsub\n"},
		{"-tr 反向时间", []string{"-tr"}, "sub\na.txt\nrun.sh\nlink\n"},
		{"-Sr 反向大小", []string{"-Sr"}, "link\na.txt\nrun.sh\nsub\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, test.args, "")
			if result.Code != 0 || result.Stdout != test.want {
				t.Fatalf("%v 得到 %q", test.args, result.Stdout)
			}
		})
	}
}

func TestClassifyAndQuote(t *testing.T) {
	dir := fixture(t)
	result := commandtest.Run(t, Run, dir, []string{"-F"}, "")
	if result.Code != 0 || result.Stdout != "a.txt\nlink@\nrun.sh*\nsub/\n" {
		t.Fatalf("-F 得到 %q", result.Stdout)
	}
	result = commandtest.Run(t, Run, dir, []string{"-p"}, "")
	if result.Code != 0 || result.Stdout != "a.txt\nlink\nrun.sh\nsub/\n" {
		t.Fatalf("-p 得到 %q", result.Stdout)
	}

	if err := os.WriteFile(filepath.Join(dir, "odd\tname"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"-q", "-1"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "odd?name\n") {
		t.Fatalf("-q 得到 %q", result.Stdout)
	}
}

func TestLongFormatVariants(t *testing.T) {
	dir := fixture(t)
	long := commandtest.Run(t, Run, dir, []string{"-l", "a.txt"}, "")
	if long.Code != 0 || !strings.HasPrefix(long.Stdout, "-rw-------") || !strings.Contains(long.Stdout, " a.txt\n") {
		t.Fatalf("-l 得到 %+v", long)
	}
	for _, shorthand := range []string{"-g", "-o"} {
		result := commandtest.Run(t, Run, dir, []string{shorthand, "a.txt"}, "")
		if result.Code != 0 || result.Stdout != long.Stdout {
			t.Fatalf("%s 应与 -l 一致: %q vs %q", shorthand, result.Stdout, long.Stdout)
		}
	}
}

func TestSymlinkFollowing(t *testing.T) {
	dir := fixture(t)

	plain := commandtest.Run(t, Run, dir, []string{"-l", "link"}, "")
	if plain.Code != 0 || !strings.HasPrefix(plain.Stdout, "L") {
		t.Fatalf("默认应显示链接本身: %+v", plain)
	}
	followed := commandtest.Run(t, Run, dir, []string{"-L", "-l", "link"}, "")
	if followed.Code != 0 || !strings.HasPrefix(followed.Stdout, "d") {
		t.Fatalf("-L 应跟随到目录: %+v", followed)
	}

	args := commandtest.Run(t, Run, dir, []string{"-H", "-l", "link"}, "")
	if args.Code != 0 || args.Stdout != followed.Stdout {
		t.Fatalf("-H 得到 %+v", args)
	}
}

func TestUnsupportedOptions(t *testing.T) {
	dir := fixture(t)
	for _, test := range []struct {
		flag   string
		reason string
	}{
		{"-C", "终端"},
		{"-x", "终端"},
		{"-m", "终端"},
		{"-i", "inode"},
		{"-s", "st_blocks"},
		{"-c", "ctime"},
		{"-n", "uid/gid"},
	} {
		result := commandtest.Run(t, Run, dir, []string{test.flag}, "")
		if result.Code != 2 || !strings.Contains(result.Stderr, "未支持") || !strings.Contains(result.Stderr, test.reason) {
			t.Fatalf("%s 应如实拒绝并说明原因: %+v", test.flag, result)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"--nope"}, ""); result.Code != 2 {
		t.Fatalf("未知选项应报错: %+v", result)
	}
}
