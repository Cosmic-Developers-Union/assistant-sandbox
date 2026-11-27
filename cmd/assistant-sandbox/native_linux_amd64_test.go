//go:build linux && amd64

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
)

func TestCLINativeBackendIdentity(t *testing.T) {
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{
		"--backend=linux/amd64", "--tools=minimal", "--debug",
		"-c", `printf "%s:%s\n" "$ASSISTANT_SANDBOX_BACKEND" "$(uname -m)"`,
	}, strings.NewReader(""), output, diagnostic)
	if errors.Is(err, instance.ErrNativeCapability) {
		t.Skipf("native backend unavailable: %v", err)
	}
	if err != nil || code != 0 || output.String() != "linux/amd64:x86_64\n" ||
		!strings.Contains(diagnostic.String(), "backend=linux/amd64") {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
}

func TestCLIAutoBackendIdentity(t *testing.T) {
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{
		"--tools=minimal", "--debug",
		"-c", `printf "%s:%s\n" "$ASSISTANT_SANDBOX_BACKEND" "$(uname -m)"`,
	}, strings.NewReader(""), output, diagnostic)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
	backend := "riscv64"
	architecture := "riscv64"
	if strings.Contains(diagnostic.String(), "backend=linux/amd64") {
		backend, architecture = "linux/amd64", "x86_64"
	}
	if want := backend + ":" + architecture + "\n"; output.String() != want {
		t.Fatalf("stdout=%q want %q stderr=%q", output.String(), want, diagnostic.String())
	}
	selection, openErr := instance.Open(t.Context(), instance.Options{Backend: instance.BackendNativeAMD64})
	if openErr == nil {
		_ = selection.Close(t.Context())
		if backend != "linux/amd64" {
			t.Fatalf("capable host auto-selected %q", backend)
		}
	} else if !errors.Is(openErr, instance.ErrNativeCapability) {
		t.Fatal(openErr)
	} else if backend != "riscv64" {
		t.Fatalf("incapable host auto-selected %q", backend)
	}
}

func TestCLIRejectsUnknownBackend(t *testing.T) {
	_, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{"--backend=unknown", "-c", "true"}, strings.NewReader(""), &bytes.Buffer{}, diagnostic)
	if err == nil || code == 0 || !strings.Contains(err.Error(), "不支持的后端") {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, diagnostic.String())
	}
}

func TestCLIExplicitRISCVBackend(t *testing.T) {
	output, diagnostic := &bytes.Buffer{}, &bytes.Buffer{}
	code, err := run(t.Context(), []string{
		"--backend=riscv64", "--tools=minimal",
		"-c", `printf "%s:%s\n" "$ASSISTANT_SANDBOX_BACKEND" "$(uname -m)"`,
	}, strings.NewReader(""), output, diagnostic)
	if err != nil || code != 0 || output.String() != "riscv64:riscv64\n" {
		t.Fatalf("code=%d err=%v stdout=%q stderr=%q", code, err, output.String(), diagnostic.String())
	}
}
