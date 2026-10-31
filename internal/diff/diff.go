// Package diff 实现参考 Linux 差分执行器(A09):同一客体程序分别在
// 本 VM 与参考 Linux(qemu-riscv64 用户态模拟或原生 riscv64)执行,
// 对比退出码、stdout/stderr 与文件副作用;差异报告携带客体程序摘要
// 与输入,可直接定位到最小用例。
package diff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Result 是一次执行的观察面。
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// Files 是工作目录下产生的文件内容(路径 → 内容;目录以空串表示)。
	Files map[string]string
}

// Finding 是一条差异;String 输出包含客体程序摘要与输入,
// 满足"报告客体程序和输入"的验收要求。
type Finding struct {
	ProgramDigest string
	Input         string
	Field         string
	Expected      string
	Actual        string
}

func (f Finding) String() string {
	return fmt.Sprintf("差异[%s] 输入=%s 字段=%s 期望=%q 实际=%q",
		f.ProgramDigest[:16], f.Input, f.Field, f.Expected, f.Actual)
}

// Compare 对比两份观察面,返回全部差异。
func Compare(programDigest, input string, expected, actual Result) []Finding {
	findings := []Finding{}
	if expected.ExitCode != actual.ExitCode {
		findings = append(findings, Finding{programDigest, input, "exit-code",
			fmt.Sprint(expected.ExitCode), fmt.Sprint(actual.ExitCode)})
	}
	if expected.Stdout != actual.Stdout {
		findings = append(findings, Finding{programDigest, input, "stdout", expected.Stdout, actual.Stdout})
	}
	if expected.Stderr != actual.Stderr {
		findings = append(findings, Finding{programDigest, input, "stderr", expected.Stderr, actual.Stderr})
	}
	for _, path := range sortedKeys(expected.Files) {
		want := expected.Files[path]
		got, ok := actual.Files[path]
		if !ok {
			findings = append(findings, Finding{programDigest, input, "file-missing", path, ""})
			continue
		}
		if got != want {
			findings = append(findings, Finding{programDigest, input, "file-content:" + path, want, got})
		}
	}
	for _, path := range sortedKeys(actual.Files) {
		if _, ok := expected.Files[path]; !ok {
			findings = append(findings, Finding{programDigest, input, "file-unexpected", "", path})
		}
	}
	return findings
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ReferenceLinux 执行参考侧:宿主为 riscv64 时直接执行,否则经
// qemu-riscv64(-static)用户态模拟;两者都不可用时返回错误而不是
// 伪装成功。
func ReferenceLinux(ctx context.Context, image []byte, stdin string) (Result, error) {
	return referenceRun(ctx, image, stdin, "", nil)
}

// ReferenceLinuxWithRoot 以带根文件系统的参考执行运行动态程序(F07):
// qemu -L 把 /lib/ld-musl-riscv64.so.1 等绝对路径重映射到 root 下,
// LD_LIBRARY_PATH 供客体动态加载器定位 root 下的共享库。其余语义与
// ReferenceLinux 一致;root 为空时等价于 ReferenceLinux。
func ReferenceLinuxWithRoot(ctx context.Context, image []byte, stdin, root string) (Result, error) {
	return referenceRun(ctx, image, stdin, root, nil)
}

func referenceRun(ctx context.Context, image []byte, stdin, root string, extraEnv []string) (Result, error) {
	dir, err := os.MkdirTemp("", "diff-ref-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := dir + "/guest"
	if err := os.WriteFile(path, image, 0o755); err != nil {
		return Result{}, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch {
	case root != "" && runtime.GOARCH != "riscv64":
		interpreter := qemuInterpreter()
		if interpreter == "" {
			return Result{}, fmt.Errorf("参考 Linux 不可用:宿主 %s/%s 且 PATH 中无 qemu-riscv64(-static)", runtime.GOOS, runtime.GOARCH)
		}
		command = exec.CommandContext(runCtx, interpreter, "-L", root, path)
	case runtime.GOARCH == "riscv64":
		command = exec.CommandContext(runCtx, path)
	case qemuInterpreter() != "":
		command = exec.CommandContext(runCtx, qemuInterpreter(), path)
	default:
		return Result{}, fmt.Errorf("参考 Linux 不可用:宿主 %s/%s 且 PATH 中无 qemu-riscv64(-static)", runtime.GOOS, runtime.GOARCH)
	}
	command.Dir = dir
	command.Env = append(os.Environ(), extraEnv...)
	if root != "" {
		command.Env = append(command.Env, "LD_LIBRARY_PATH="+root)
	}
	command.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code = exitErr.ExitCode()
	} else if runErr != nil {
		return Result{}, fmt.Errorf("参考执行失败: %w", runErr)
	}
	files := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, entry := range entries {
			if entry.Name() == "guest" {
				continue
			}
			if entry.IsDir() {
				files[entry.Name()] = ""
				continue
			}
			if content, err := os.ReadFile(dir + "/" + entry.Name()); err == nil {
				files[entry.Name()] = string(content)
			}
		}
	}
	return Result{ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String(), Files: files}, nil
}

func qemuInterpreter() string {
	for _, name := range []string{"qemu-riscv64-static", "qemu-riscv64"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// Report 把差异集合格式化为多行文本(空差异返回空串)。
func Report(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		lines = append(lines, finding.String())
	}
	return strings.Join(lines, "\n")
}
