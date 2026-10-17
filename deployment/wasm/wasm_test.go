package wasm

import (
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/deployment"
)

func TestWASMMatrixIsHonest(t *testing.T) {
	matrix := Matrix()
	if matrix.Name != "wasm" || matrix.Deploy == "" {
		t.Fatalf("matrix=%+v", matrix)
	}
	if matrix.Executors["builtin-go"] != deployment.Supported {
		t.Fatal("客体内置程序是已验收能力")
	}
	if matrix.Executors["wasi"] != deployment.Partial {
		t.Fatal("扩展模块经显式桥调用，属有限支持")
	}
	for _, name := range []string{"wasm-in-guest", "shebang"} {
		if matrix.Executors[name] != deployment.Unsupported {
			t.Fatalf("%s 应声明为不支持", name)
		}
	}

	if matrix.Executors["elf"] != deployment.Partial {
		t.Fatalf("elf 应声明为 partial: %v", matrix.Executors["elf"])
	}
	notes := strings.Join(matrix.Notes, "；")
	for _, fragment := range []string{"ELF", "动态链接", "mmap"} {
		if !strings.Contains(notes, fragment) {
			t.Fatalf("ELF 的边界说明缺少 %q: %s", fragment, notes)
		}
	}
	for name, support := range map[string]deployment.Support{
		"Signals":  matrix.Signals,
		"TTY":      matrix.TTY,
		"Snapshot": matrix.Snapshot,
		"Replay":   matrix.Replay,
		"Network":  matrix.Network,

		"AddressSpace": matrix.AddressSpace,
	} {
		if support != deployment.Unsupported {
			t.Fatalf("%s 应声明为不支持，实际 %v", name, support)
		}
	}

	if len(matrix.Notes) == 0 {
		t.Fatal("能力矩阵缺少边界说明")
	}
}

func TestSupportStringIsStable(t *testing.T) {
	cases := map[deployment.Support]string{
		deployment.Unsupported: "unsupported",
		deployment.Partial:     "partial",
		deployment.Supported:   "supported",
		deployment.Support(9):  "unsupported",
	}
	for support, want := range cases {
		if support.String() != want {
			t.Fatalf("%d -> %q, want %q", support, support.String(), want)
		}
	}
}
