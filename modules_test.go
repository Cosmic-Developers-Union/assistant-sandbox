package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero/sys"
)

func extensionFixture(t *testing.T) []byte {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "extension.wasm")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, "testdata/extension.go")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	code, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
func TestRegisteredWASIModuleReusesFilesystemAndShellContext(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{Modules: map[string][]byte{"extension": extensionFixture(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Error(err)
	}
	var out bytes.Buffer
	result, err := r.Execute(t.Context(), dir, `cd sub; export VALUE=remembered; printf input | extension echo`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "/work/sub remembered\ninput" {
		t.Fatal(result, err, out.String())
	}
	result, err = r.Execute(t.Context(), dir, `extension write`, &out, &out)
	if err != nil || result.ExitCode != 0 {
		t.Fatal(result, err, out.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "sub", "extension.txt"))
	if err != nil || string(data) != "extension" {
		t.Fatal(string(data), err)
	}
	out.Reset()
	result, err = r.Execute(t.Context(), dir, `extension exit`, &out, &out)
	if err != nil || result.ExitCode != 7 || !strings.Contains(out.String(), "expected failure") {
		t.Fatal(result, err, out.String())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.Execute(ctx, dir, `extension spin`, io.Discard, io.Discard); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	out.Reset()
	result, err = r.Execute(t.Context(), dir, `test -z "$VALUE"; pwd`, &out, &out)
	if err != nil || result.ExitCode != 0 || !strings.Contains(out.String(), "Bash loop 已重建") {
		t.Fatal(result, err, out.String())
	}
}
func TestWASIModuleExitContract(t *testing.T) {
	if result, err := moduleResult(t.Context(), sys.NewExitError(7)); err != nil || result.ExitCode != 7 {
		t.Fatal(result, err)
	}
	if result, err := moduleResult(t.Context(), nil); err != nil || result.ExitCode != 0 {
		t.Fatal(result, err)
	}
	if _, err := moduleResult(t.Context(), io.ErrUnexpectedEOF); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if validCommand("") || validCommand("not valid") || !validCommand("EXTENSION_1-test") {
		t.Fatal("注册名校验")
	}
}

func TestVMInternalDispatcherDoesNotShadowLegalModuleNames(t *testing.T) {
	r, err := newTestRuntime(t.Context(), Config{Modules: map[string][]byte{"vm-exec": extensionFixture(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	var out bytes.Buffer
	result, err := r.Execute(t.Context(), dir, `printf input | vm-exec echo`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "/work \ninput" {
		t.Fatal(result, err, out.String())
	}
	out.Reset()
	result, err = r.Execute(t.Context(), dir, `@wasi`, &out, &out)
	if err != nil || result.ExitCode != 127 {
		t.Fatal(result, err, out.String())
	}
}
