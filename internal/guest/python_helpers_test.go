package guest_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

const python312LibsDigest = "f8831c8c9716c74cac6e2d950ccac7585d64e5f3dfbebad76677bd68eba32486"

// python312Guest 启动一台装配了 Alpine CPython 3.12.16、预编译
// 标准库字节码与运行时共享库的 VM,返回连接。调用方负责关闭 VM。
func python312Guest(t *testing.T) (*instance.VM, *instance.Connection) {
	t.Helper()
	return python312GuestWith(t, nil)
}

// python312GuestWith 与 python312Guest 相同,但允许测试装配宿主网络、
// 出口策略等 VM 选项(M10/M11)。
func python312GuestWith(t *testing.T, configure func(*instance.Options)) (*instance.VM, *instance.Connection) {
	t.Helper()
	apk := python312Asset(t, "testdata/python312/python3-3.12.16-r0.apk", python312APKDigest)
	pyc := python312Asset(t, "testdata/python312/python312-pyc.tar.gz", python312PycDigest)
	libs := python312Asset(t, "testdata/python312/python312-libs.tar.gz", python312LibsDigest)
	loader, err := os.ReadFile("testdata/lib/ld-musl-riscv64.so.1")
	if err != nil {
		t.Fatal(err)
	}
	options := instance.Options{
		Deterministic: false,
		Seed:          1,
		Session:       resources.Limits{Memory: 2 << 30, Work: 1 << 38},
		Process:       resources.Limits{Memory: 1 << 30, Work: 1 << 38},
	}
	if configure != nil {
		configure(&options)
	}
	machine, err := instance.New(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	extractPython312(t, machine, apk)
	extractPython312Pyc(t, machine, pyc)
	extractPython312Libs(t, machine, libs)
	writeGuestFile(t, machine, "/lib/ld-musl-riscv64.so.1", loader, 0o755)
	writeGuestFile(t, machine, "/lib/libc.musl-riscv64.so.1", loader, 0o755)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return machine, connection
}

// runPython312 在客体中运行 python3.12 -S -c script,返回合并输出与退出码。
func runPython312(t *testing.T, connection *instance.Connection, script string, timeout time.Duration) (string, uint8) {
	t.Helper()
	return runPython312Env(t, connection, script, timeout, nil)
}

// runPython312Env 在 runPython312 的基础上追加客体环境变量(M11 CA 配置)。
func runPython312Env(t *testing.T, connection *instance.Connection, script string, timeout time.Duration, extra map[string]string) (string, uint8) {
	t.Helper()
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	env := map[string]string{
		"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "PYTHONHOME": "/usr",
	}
	for name, value := range extra {
		env[name] = value
	}
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: "/usr/bin/python3.12",
		Args:    []string{"-S", "-c", script},
		Dir:     "/work",
		Env:     env,
		Streams: fd.Streams{Output: output, Error: diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatalf("python 脚本: %v (stdout=%q stderr=%q)", err, output.String(), diagnostic.String())
	}
	return output.String() + diagnostic.String(), state.ExitCode
}

// extractPython312Libs 把客体 CPython 依赖的共享库与符号链接装入 /usr/lib。
func extractPython312Libs(t *testing.T, machine *instance.VM, archive []byte) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	for {
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		target := "/" + name
		switch header.Typeflag {
		case tar.TypeDir:
			_ = machine.Filesystem().Mkdir(t.Context(), target, 0o755)
		case tar.TypeReg:
			data, err := io.ReadAll(io.LimitReader(files, 32<<20))
			if err != nil {
				t.Fatal(err)
			}
			mode := header.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			writeGuestFile(t, machine, target, data, mode)
		case tar.TypeSymlink:
			_ = machine.Filesystem().Symlink(t.Context(), header.Linkname, target)
		}
	}
}

// runPython312Module 在客体中运行 python3.12 -S -m <module>,用于
// multiprocessing spawn 的模块主程序。
func runPython312Module(t *testing.T, connection *instance.Connection, module string, timeout time.Duration) (string, uint8) {
	t.Helper()
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: "/usr/bin/python3.12",
		Args:    []string{"-S", "-m", module},
		Dir:     "/work",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
			"PYTHONHOME": "/usr", "PYTHONPATH": "/work",
		},
		Streams: fd.Streams{Output: output, Error: diagnostic},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(runCtx, process.ID)
	if err != nil {
		t.Fatalf("python -m %s: %v (stdout=%q stderr=%q)", module, err, output.String(), diagnostic.String())
	}
	return output.String() + diagnostic.String(), state.ExitCode
}
