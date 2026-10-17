package splitcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestSizeSuffixesAndLineBytes(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"lines":  "abc\nde\n",
		"big":    strings.Repeat("x", 1500),
		"medium": strings.Repeat("y", 600),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		args  []string
		files map[string]int
	}{
		{args: []string{"-C", "4", "lines", "lb"}, files: map[string]int{"lbaa": 4, "lbab": 3}},
		{args: []string{"-b", "1K", "big", "kb"}, files: map[string]int{"kbaa": 1024, "kbab": 476}},
		{args: []string{"-b", "1KB", "big", "dec"}, files: map[string]int{"decaa": 1000, "decab": 500}},
		{args: []string{"-b", "1KiB", "big", "bin"}, files: map[string]int{"binaa": 1024, "binab": 476}},
		{args: []string{"-b", "1b", "medium", "blk"}, files: map[string]int{"blkaa": 512, "blkab": 88}},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 {
			t.Fatalf("args=%v 得到 %+v", test.args, result)
		}
		for name, size := range test.files {
			if got := len(read(t, dir, name)); got != size {
				t.Fatalf("args=%v 文件 %s 大小 %d，want %d", test.args, name, got, size)
			}
		}
	}
}

func TestObsoleteAndSuffixRollover(t *testing.T) {
	dir := t.TempDir()
	var input strings.Builder
	for index := 1; index <= 27; index++ {
		fmt.Fprintf(&input, "%d\n", index)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte(input.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-2", "data", "old"}, ""); result.Code != 0 {
		t.Fatalf("历史写法得到 %+v", result)
	}
	if got := read(t, dir, "oldaa"); got != "1\n2\n" {
		t.Fatalf("历史写法产物 %q", got)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-l", "1", "data", "roll"}, ""); result.Code != 0 {
		t.Fatalf("后缀进位得到 %+v", result)
	}
	if got := read(t, dir, "rollba"); got != "27\n" {
		t.Fatalf("进位后最后一个文件 %q", got)
	}
	result := commandtest.Run(t, Run, dir, []string{"-l", "1", "-a", "1", "data", "tiny"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "suffix") {
		t.Fatalf("后缀耗尽应失败: %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-l", "1", "--hex-suffixes=3", "data", "hex"}, ""); result.Code != 0 {
		t.Fatalf("十六进制起始值得到 %+v", result)
	}
	if got := read(t, dir, "hex3"); got != "1\n" {
		t.Fatalf("十六进制产物 %q", got)
	}
}

func TestChunkAndSizeErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n2\n3\n4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-n", "x", "data", "o"},
		{"-n", "l/0", "data", "o"},
		{"-n", "r/2", "data", "o"},
		{"-b", "0", "data", "o"},
		{"-b", "1X", "data", "o"},
		{"-C", "0", "data", "o"},
		{"-C", "1Z", "data", "o"},
		{"--hex-suffixes=x", "data", "o"},
	} {
		result := commandtest.Run(t, Run, dir, args, "")
		if result.Code != 2 || result.Stderr == "" {
			t.Fatalf("args=%v 得到 %+v", args, result)
		}
	}
	if result := commandtest.Run(t, Run, dir, []string{"-n", "3", "-e", "data", "chunk"}, ""); result.Code != 0 {
		t.Fatalf("-n 3 -e 得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-n", "l/3", "-e", "data", "line"}, ""); result.Code != 0 {
		t.Fatalf("-n l/3 -e 得到 %+v", result)
	}
}

func TestFaultedWrites(t *testing.T) {
	for _, test := range []struct {
		operation string
		args      []string
	}{
		{"Write", []string{"-l", "1", "data", "o"}},
		{"Write", []string{"-b", "2", "data", "o"}},
		{"Write", []string{"-C", "2", "data", "o"}},
		{"Write", []string{"-n", "2", "data", "o"}},
		{"Close", []string{"-l", "1", "data", "o"}},
		{"OpenWrite", []string{"-l", "1", "data", "o"}},
		{"Read", []string{"-l", "1", "data", "o"}},
		{"Read", []string{"-t", ",", "-l", "1", "data", "o"}},
		{"Write", []string{"-t", ",", "-l", "1", "data", "o"}},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n2\n3\n4\n"), 0600); err != nil {
			t.Fatal(err)
		}
		result := commandtest.RunFault(t, Run, dir, test.args, "", test.operation)
		_ = result
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("%s args=%v 得到 %+v", test.operation, test.args, result)
		}
	}
}

func TestMoreUsageErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("1\n2\n3\n4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-l", "x", "data", "o"}, ""); result.Code != 2 {
		t.Fatalf("非法行数得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-n", "2", "absent", "o"}, ""); result.Code != 1 {
		t.Fatalf("缺失输入得到 %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"data", "-l", "2", "post"}, ""); result.Code != 0 {
		t.Fatalf("操作数在前的参数得到 %+v", result)
	}
	if got := read(t, dir, "postaa"); got != "1\n2\n" {
		t.Fatalf("操作数在前的产物 %q", got)
	}
	lines := strings.Repeat("x\n", 11)
	if result := commandtest.Run(t, Run, dir, []string{"-l", "1", "-d", "-a", "1", "-", "num"}, lines); result.Code != 1 {
		t.Fatalf("数字后缀耗尽应失败: %+v", result)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-b", "1P", "data", "huge"}, ""); result.Code != 0 {
		t.Fatalf("大尺寸后缀得到 %+v", result)
	}
}

func TestSeparatorsAndHexLimits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cols"), []byte("ab,cd"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-t", ",", "-l", "1", "plain", "noseq"}, ""); result.Code != 0 {
		t.Fatalf("无分隔符输入得到 %+v", result)
	}
	if got := read(t, dir, "noseqaa"); got != "abc" {
		t.Fatalf("无分隔符产物 %q", got)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-t", ",", "-C", "2", "cols", "col"}, ""); result.Code != 0 {
		t.Fatalf("-C 自定义分隔符得到 %+v", result)
	}
	if got := read(t, dir, "colaa"); got != "ab" {
		t.Fatalf("-C 自定义分隔符产物 %q", got)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-t", ",", "-n", "2", "cols", "chunk"}, ""); result.Code != 0 {
		t.Fatalf("-n 自定义分隔符得到 %+v", result)
	}
	var lines strings.Builder
	for index := 0; index < 17; index++ {
		fmt.Fprintf(&lines, "%d\n", index)
	}
	if result := commandtest.Run(t, Run, dir, []string{"-l", "1", "-x", "-a", "1", "-", "hex"}, lines.String()); result.Code != 1 {
		t.Fatalf("十六进制后缀耗尽应失败: %+v", result)
	}
}
