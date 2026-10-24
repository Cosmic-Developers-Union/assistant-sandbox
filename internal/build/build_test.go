package build

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
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

// B06:相同源码与工具链的两次构建必须产出逐字节一致的客体;
// 摘要不一致时报告两侧摘要与定位线索,而不是笼统失败。
func TestGuestBuildIsReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("快速模式跳过两次完整客体构建")
	}
	first, err := digestGuestBuild(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := digestGuestBuild(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"assistant-bash.wasm", "assistant-bash.wasm.gz"} {
		if first[name] != second[name] {
			t.Fatalf("%s 摘要不一致: 第一次 %s 第二次 %s(相同源码+工具链应逐字节一致;请检查 -trimpath/-buildvcs/-ldflags 与 gzip 头是否引入时间或路径)", name, first[name], second[name])
		}
	}
}

func digestGuestBuild(output string) (map[string]string, error) {
	if err := Generate("../..", output); err != nil {
		return nil, err
	}
	digests := map[string]string{}
	for _, name := range []string{"assistant-bash.wasm", "assistant-bash.wasm.gz"} {
		data, err := os.ReadFile(filepath.Join(output, name))
		if err != nil {
			return nil, err
		}
		digests[name] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return digests, nil
}
