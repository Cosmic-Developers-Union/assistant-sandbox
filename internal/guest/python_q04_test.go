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

const python312PycDigest = "9f163b7ce9d11c3d663c42b317e9316391bf048bddb0baf2030084b6f95c39cb"

// TestPython312StandardLibrary 验证 Q04:路径/编码与 json、re、
// datetime、math 固定用例在客体 CPython 中通过。预编译 unchecked
// 字节码避免在客体重复编译大型标准库模块。
func TestPython312StandardLibrary(t *testing.T) {
	apk := python312Asset(t, "testdata/python312/python3-3.12.16-r0.apk", python312APKDigest)
	pyc := python312Asset(t, "testdata/python312/python312-pyc.tar.gz", python312PycDigest)
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
	defer func() { _ = machine.Close(context.Background()) }()
	extractPython312(t, machine, apk)
	extractPython312Pyc(t, machine, pyc)
	writeGuestFile(t, machine, "/lib/ld-musl-riscv64.so.1", loader, 0o755)
	writeGuestFile(t, machine, "/lib/libc.musl-riscv64.so.1", loader, 0o755)
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	script := `import sys, json, re, datetime, math
assert sys.version_info[:2] == (3, 12), sys.version
assert sys.getfilesystemencoding().lower() == "utf-8", sys.getfilesystemencoding()
assert any(p.endswith("/python3.12") for p in sys.path), sys.path
assert json.loads('{"a": 1}')["a"] == 1
assert re.findall("[0-9]+", "a1b22") == ["1", "22"]
assert datetime.date(2024, 1, 2).isoformat() == "2024-01-02"
assert math.factorial(5) == 120
print("Q04-OK")
`
	output := &bytes.Buffer{}
	diagnostic := &bytes.Buffer{}
	runCtx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
		t.Fatalf("标准库用例: %v (stdout=%q stderr=%q)", err, output.String(), diagnostic.String())
	}
	if state.ExitCode != 0 || output.String() != "Q04-OK\n" {
		t.Fatalf("标准库用例 exit=%d stdout=%q stderr=%q", state.ExitCode, output.String(), diagnostic.String())
	}
}

func extractPython312Pyc(t *testing.T, machine *instance.VM, archive []byte) {
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
		if header.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(files, 8<<20))
		if err != nil {
			t.Fatal(err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		writeGuestFile(t, machine, "/usr/lib/python3.12/"+name, data, 0o644)
	}
}
