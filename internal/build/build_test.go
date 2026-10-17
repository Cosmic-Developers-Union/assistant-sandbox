package build

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestBuildAndArchive(t *testing.T) {
	output := t.TempDir()
	if err := Generate("../..", output); err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join(output, "assistant-bash.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(code, []byte{0, 97, 115, 109}) {
		t.Fatal("不是 WASM 客体")
	}
	archive, err := os.Open(filepath.Join(output, "assistant-bash.wasm.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := archive.Close(); err != nil {
			t.Error(err)
		}
	}()
	reader, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	decoded, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(code, decoded) {
		t.Fatal("嵌入压缩包与客体不一致", err)
	}
	if err := Generate(t.TempDir(), output); err == nil {
		t.Fatal("缺失项目文件")
	}
	if err := atomicWrite(filepath.Join(output, "missing", "out"), code); err == nil {
		t.Fatal("缺失输出目录")
	}
	if err := atomicWrite(output, code); err == nil {
		t.Fatal("用客体覆盖目录")
	}
}

func TestGuestBuildFailureKeepsExistingArtifact(t *testing.T) {
	project := t.TempDir()
	dir := project
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module fixture\n\ngo 1.27.0\n"), 0o600); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.sum"), nil, 0o600); err != nil {
		t.Error(err)
	}
	output := t.TempDir()
	artifact := filepath.Join(output, "assistant-bash.wasm.gz")
	if err := os.WriteFile(artifact, []byte("existing"), 0o600); err != nil {
		t.Error(err)
	}
	if err := Generate(dir, output); err == nil {
		t.Fatal("缺失依赖被忽略")
	}
	data, _ := os.ReadFile(artifact)
	if string(data) != "existing" {
		t.Fatal("失败构建覆盖了可用客体")
	}
	if err := Generate("../..", filepath.Join(output, "missing")); err == nil {
		t.Fatal("缺失输出目录未报告")
	}
}

func TestDependencySourceDownloadsBeforeLocating(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + calls + "\"\nif [ \"$1\" = list ]; then printf '%s' \"${STUB_DIR:-}\"; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("STUB_DIR", "")

	if _, err := dependencySource(".", "fixture"); err == nil {
		t.Fatal("空源码目录被接受")
	}
	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	download := strings.Index(string(logged), "mod download fixture")
	locate := strings.Index(string(logged), "list -m -f {{.Dir}} fixture")
	if download < 0 || locate < 0 || download > locate {
		t.Fatalf("定位依赖前没有解包: %q", logged)
	}

	want := t.TempDir()
	t.Setenv("STUB_DIR", want)
	got, err := dependencySource(".", "fixture")
	if err != nil || got != want {
		t.Fatalf("定位依赖 = %q, %v", got, err)
	}
}
