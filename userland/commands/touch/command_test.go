package touch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"touched"}, "")
	if result.Code != 0 {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(dir, "touched")); err != nil {
		t.Fatal(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"--assistant-invalid-flag"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatal(result)
	}
}

func TestTimestampForms(t *testing.T) {
	for _, test := range []struct {
		name  string
		stamp string
		want  time.Time
	}{
		{"12 位带秒", "202401020304.05", time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)},
		{"12 位不带秒", "202401020304", time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)},
		{"10 位 69 之前按 20xx", "2401020304", time.Date(2024, 1, 2, 3, 4, 0, 0, time.UTC)},
		{"10 位 69 起按 19xx", "6901020304", time.Date(1969, 1, 2, 3, 4, 0, 0, time.UTC)},
		{"闰年 2 月 29", "202402290000", time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			result := commandtest.Run(t, Run, dir, []string{"-t", test.stamp, "stamped"}, "")
			if result.Code != 0 || result.Stderr != "" {
				t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
			}
			info, err := os.Stat(filepath.Join(dir, "stamped"))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.ModTime().Unix(); got != test.want.Unix() {
				t.Fatalf("mtime = %d (%v)，期望 %d (%v)", got, info.ModTime().UTC(), test.want.Unix(), test.want)
			}
		})
	}

	for _, stamp := range []string{"202413020304", "202401023004", "202401020304.99", "202401", "abcdefgh", "2024010203", "202302290000", "226301010000", "160001010000"} {
		result := commandtest.Run(t, Run, t.TempDir(), []string{"-t", stamp, "x"}, "")
		if result.Code == 0 || result.Stderr == "" {
			t.Fatalf("非法时间 %q 应失败: %+v", stamp, result)
		}
	}
}

func TestNoCreateSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	result := commandtest.Run(t, Run, dir, []string{"-c", "absent"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("-c 不应创建文件")
	}
}

func TestNoCreateReportsNonMissingFailure(t *testing.T) {
	result := commandtest.RunFault(t, Run, t.TempDir(), []string{"-c", "blocked"}, "", "Stat")
	if result.Code == 0 || result.Stderr == "" {
		t.Fatalf("-c 遇到 Stat 故障应失败: %+v", result)
	}
}

func TestAccessTimeKeepsModificationTime(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	if err := os.Chtimes(target, pinned, pinned); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-a", "-t", "202001010000", "keep"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != pinned.Unix() {
		t.Fatalf("mtime 被改动: %v，期望 %v", info.ModTime().UTC(), pinned)
	}

	result = commandtest.Run(t, Run, dir, []string{"-t", "202001010000", "keep"}, "")
	if result.Code != 0 {
		t.Fatal(result)
	}
	if info, err = os.Stat(target); err != nil || info.ModTime().Unix() != time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("mtime = %v, %v", info.ModTime().UTC(), err)
	}
}

func TestReferenceTimes(t *testing.T) {
	dir := t.TempDir()
	reference := filepath.Join(dir, "ref")
	target := filepath.Join(dir, "target")
	for _, name := range []string{"ref", "target"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Date(1999, 12, 31, 23, 58, 59, 0, time.UTC)
	if err := os.Chtimes(reference, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	other := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(target, other, other); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-r", "ref", "target"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != stamp.Unix() {
		t.Fatalf("mtime = %v，期望 %v", info.ModTime().UTC(), stamp)
	}

	result = commandtest.Run(t, Run, dir, []string{"-r", "absent", "target"}, "")
	if result.Code == 0 || result.Stderr == "" {
		t.Fatalf("缺失的参考文件应失败: %+v", result)
	}
}

func TestModificationTimeKeepsAccessTime(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-m", "-t", "202001010000", "keep"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("code=%d stderr=%q", result.Code, result.Stderr)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Unix() != time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("mtime = %v", info.ModTime().UTC())
	}

	result = commandtest.Run(t, Run, dir, []string{"-a", "-m", "-t", "202101010000", "keep"}, "")
	if result.Code != 0 {
		t.Fatal(result)
	}
	if info, err = os.Stat(target); err != nil || info.ModTime().Unix() != time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("mtime = %v, %v", info.ModTime().UTC(), err)
	}
}
