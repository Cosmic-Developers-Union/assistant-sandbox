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

const python312LibsDigest = "160b7396952f6579de2a5c2ba30f0aa0b398e704f6d24a8727db39f82def84a9"

// python312Guest 启动一台装配了 Alpine CPython 3.12.16、预编译
// 标准库字节码与运行时共享库的 VM,返回连接。调用方负责关闭 VM。
func python312Guest(t *testing.T) (*instance.VM, *instance.Connection) {
	t.Helper()
	apk := python312Asset(t, "testdata/python312/python3-3.12.16-r0.apk", python312APKDigest)
	pyc := python312Asset(t, "testdata/python312/python312-pyc.tar.gz", python312PycDigest)
	libs := python312Asset(t, "testdata/python312/python312-libs.tar.gz", python312LibsDigest)
	loader, err := os.ReadFile("testdata/lib/ld-musl-riscv64.so.1")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := instance.New(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		Session:       resources.Limits{Memory: 1 << 30, Work: 1 << 36},
		Process:       resources.Limits{Memory: 512 << 20, Work: 1 << 36},
	})
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
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	process, err := connection.Exec(runCtx, instance.ExecSpec{
		Program: "/usr/bin/python3.12",
		Args:    []string{"-S", "-c", script},
		Dir:     "/work",
		Env: map[string]string{
			"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8", "PYTHONHOME": "/usr",
		},
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
