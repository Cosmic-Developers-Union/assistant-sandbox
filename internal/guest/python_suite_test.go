package guest_test

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestPython312UpstreamSuite 运行 CPython 3.12.16 上游测试。默认不执行，
// 避免普通单元测试承担数小时的客体测试；通过 PYTHON_SUITE_TESTS 指定
// 模块名或 "all"。输出保留完整 stdout/stderr 供失败归因。
func TestPython312UpstreamSuite(t *testing.T) {
	selection := strings.Fields(os.Getenv("PYTHON_SUITE_TESTS"))
	if len(selection) == 0 {
		t.Skip("设置 PYTHON_SUITE_TESTS 后运行 CPython 上游测试")
	}
	machine, connection := python312Guest(t)
	extractPython312Test(t, machine, python312Asset(t,
		"testdata/python312/python312-test.tar.gz", python312TestDigest))
	args := []string{"-S", "-m", "test", "-j1"}
	if os.Getenv("PYTHON_SUITE_VERBOSE") != "" {
		args = append(args, "-v")
	}
	if timeout := os.Getenv("PYTHON_SUITE_TIMEOUT"); timeout != "" {
		// 单个模块超过时限时 regrtest 终止其 worker 并继续下一个
		// 模块,避免整套门禁静默挂到外层超时。
		args = append(args, "--timeout", timeout)
	}
	if len(selection) != 1 || selection[0] != "all" {
		args = append(args, selection...)
	}
	output, diagnostic, code := runGuestProgram(t, connection,
		"/usr/bin/python3.12", args,
		map[string]string{
			"HOME": "/work", "PATH": "/usr/bin:/bin", "LANG": "C.UTF-8",
			"PYTHONHOME": "/usr", "PYTHONIOENCODING": "utf-8",
		},
		3*time.Hour)
	if code != 0 {
		t.Fatalf("CPython 测试 exit=%d\nstdout:\n%s\nstderr:\n%s", code, output, diagnostic)
	}
}
