//go:build linux

package shell

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func TestDfReportsFilesystemSpaceThroughRealShell(t *testing.T) {
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	if err := run(t.Context(), "df -P", dir, nil, strings.NewReader(""), &out, &diagnostic); err != nil {
		t.Fatalf("err=%v stderr=%q", err, diagnostic.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || lines[0] != "Filesystem 512-blocks Used Available Capacity Mounted on" {
		t.Fatalf("输出=%q", out.String())
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 6 {
		t.Fatalf("数据行应有六列: %q", lines[1])
	}
	for _, index := range []int{1, 2, 3} {
		if _, err := strconv.ParseUint(fields[index], 10, 64); err != nil {
			t.Fatalf("第 %d 列应是数字: %q", index+1, lines[1])
		}
	}
	if !strings.HasSuffix(fields[4], "%") {
		t.Fatalf("容量列应是百分数: %q", lines[1])
	}
}

func TestMkfifoCreatesVisibleNamedPipeThroughRealShell(t *testing.T) {
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	script := "mkfifo pipe && ls -F pipe && test -p pipe && echo classified"
	if err := run(t.Context(), script, dir, nil, strings.NewReader(""), &out, &diagnostic); err != nil {
		t.Fatalf("err=%v stderr=%q", err, diagnostic.String())
	}
	if out.String() != "pipe|\nclassified\n" {
		t.Fatalf("stdout=%q stderr=%q", out.String(), diagnostic.String())
	}
}
