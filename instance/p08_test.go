package instance

import (
	"bytes"
	"os"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// TestGuestPTYMasterSlave 验证 P08:真实 ELF 打开 /dev/ptmx 与
// /dev/pts/0,完成 isatty、窗口大小、termios 与管道差异检查。
func TestGuestPTYMasterSlave(t *testing.T) {
	image, err := os.ReadFile("../internal/guest/testdata/p08-pty")
	if err != nil {
		t.Fatal(err)
	}
	machine, err := New(t.Context(), Options{Deterministic: true, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	probe, err := machine.Filesystem().Open(t.Context(), "/probe", vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Write(image); err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := machine.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	process, err := connection.Exec(t.Context(), ExecSpec{Program: "/probe", Streams: fd.Streams{Output: output, Error: output}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := connection.Wait(t.Context(), process.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode != 0 || output.String() != "P08-OK\n" {
		t.Fatalf("PTY 探针 exit=%d output=%q(断言码见 p08-pty.c)", state.ExitCode, output.String())
	}
}
