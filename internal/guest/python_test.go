package guest_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

const python312APKDigest = "38e902b011e1218292cbc25d3259c98007e437d61159dcffb4504cfdb1b2d487"

// TestPython312MinimalStartup 验证 Q03:真实 CPython 3.12.16 客体
// 执行 --version 与算术脚本。资产为固定 Alpine APK,摘要锁定,
// 测试时解包到客体文件系统(不使用宿主解释器)。
func TestPython312MinimalStartup(t *testing.T) {
	apk := python312Asset(t, "testdata/python312/python3-3.12.16-r0.apk", python312APKDigest)
	loader, err := os.ReadFile("testdata/lib/ld-musl-riscv64.so.1")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := instance.New(t.Context(), instance.Options{
		Deterministic: false,
		Seed:          1,
		Session:       resources.Limits{Memory: 1 << 30, Work: 1 << 34},
		Process:       resources.Limits{Memory: 512 << 20, Work: 1 << 34},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(context.Background()) }()
	extractPython312(t, machine, apk)
	writeGuestFile(t, machine, "/lib/ld-musl-riscv64.so.1", loader, 0o755)
	writeGuestFile(t, machine, "/lib/libc.musl-riscv64.so.1", loader, 0o755)

	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, uint8) {
		t.Helper()
		output := &bytes.Buffer{}
		diagnostic := &bytes.Buffer{}
		runCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		process, err := connection.Exec(runCtx, instance.ExecSpec{
			Program: "/usr/bin/python3.12",
			Args:    args,
			Dir:     "/work",
			Env: map[string]string{
				"PATH": "/usr/bin:/bin", "HOME": "/work", "LANG": "C.UTF-8",
				"PYTHONHOME": "/usr",
			},
			Streams: fd.Streams{Output: output, Error: diagnostic},
		})
		if err != nil {
			t.Fatal(err)
		}
		state, err := connection.Wait(runCtx, process.ID)
		if err != nil {
			t.Fatalf("python %v: %v (stdout=%q stderr=%q)", args, err, output.String(), diagnostic.String())
		}
		return output.String() + diagnostic.String(), state.ExitCode
	}

	if got, code := run("--version"); code != 0 || got != "Python 3.12.16\n" {
		t.Fatalf("--version exit=%d output=%q", code, got)
	}
	if got, code := run("-S", "-c", "print(2+3)"); code != 0 || got != "5\n" {
		t.Fatalf("算术脚本 exit=%d output=%q", code, got)
	}
}

// extractPython312 只解包 APK 中运行 CPython 所需的解释器、共享库
// 与标准库;其余包内容不进入客体。
func extractPython312(t *testing.T, machine *instance.VM, apk []byte) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(apk))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		wanted := name == "usr/bin/python3.12" || name == "usr/lib/libpython3.12.so.1.0" ||
			strings.HasPrefix(name, "usr/lib/python3.12/")
		if !wanted {
			continue
		}
		target := "/" + name
		switch header.Typeflag {
		case tar.TypeDir:
			_ = machine.Filesystem().Mkdir(t.Context(), target, 0o755)
		case tar.TypeReg:
			data, err := io.ReadAll(io.LimitReader(archive, 32<<20))
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

func python312Asset(t *testing.T, name, want string) []byte {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("%s 摘要=%s want %s", name, got, want)
	}
	return content
}

func writeGuestFile(t *testing.T, machine *instance.VM, name string, content []byte, mode os.FileMode) {
	t.Helper()
	parts := strings.Split(strings.Trim(name, "/"), "/")
	current := ""
	for _, part := range parts[:len(parts)-1] {
		current += "/" + part
		_ = machine.Filesystem().Mkdir(t.Context(), current, 0o755)
	}
	file, err := machine.Filesystem().Open(t.Context(), name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
