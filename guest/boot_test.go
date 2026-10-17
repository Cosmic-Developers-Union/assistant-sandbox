package guest

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/shell"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
)

func TestBootUsesExplicitWorkspaceAndReapsShell(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	kernel, parent, err := New(dir, []string{"VALUE=显式"}, strings.NewReader(""), &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Run(t.Context(), kernel, parent, `bash -c 'printf "%s" "$VALUE"'`, dir, []string{"VALUE=显式"}, strings.NewReader(""), &output, &output); err != nil {
		t.Fatal(err, output.String())
	}
	if output.String() != "显式" || len(kernel.Processes()) != 1 {
		t.Fatal(output.String(), kernel.Processes())
	}
	if _, _, err := New(filepath.Join(dir, "missing"), nil, nil, nil, nil); err == nil {
		t.Fatal("接受不存在的工作区")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(file, nil, nil, nil, nil); err == nil {
		t.Fatal("接受文件作为工作区")
	}
}

func guestProgram(t *testing.T) []byte {
	t.Helper()
	const (
		codeBase  = 0x10000
		entry     = 0x10100
		messageAt = 0x200
		programAt = 0x100
		codeSize  = 0x1000
	)
	raw := make([]byte, codeSize)
	copy(raw[0:4], "\x7fELF")
	raw[4], raw[5], raw[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 243)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], entry)
	binary.LittleEndian.PutUint64(raw[32:], 0x40)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[0x40:], 1)
	binary.LittleEndian.PutUint32(raw[0x44:], 5)
	binary.LittleEndian.PutUint64(raw[0x48:], 0)
	binary.LittleEndian.PutUint64(raw[0x50:], codeBase)
	binary.LittleEndian.PutUint64(raw[0x60:], codeSize)
	binary.LittleEndian.PutUint64(raw[0x68:], codeSize)
	binary.LittleEndian.PutUint64(raw[0x70:], 0x1000)
	copy(raw[messageAt:], "hi")
	for index, instruction := range []uint32{
		0x00100513,
		0x000105B7,
		0x20058593,
		0x00200613,
		0x04000893,
		0x00000073,
		0x00700513,
		0x05D00893,
		0x00000073,
	} {
		binary.LittleEndian.PutUint32(raw[programAt+index*4:], instruction)
	}
	return raw
}

func TestGuestRunsELFProgram(t *testing.T) {
	dir := t.TempDir()
	var output bytes.Buffer
	kernel, _, err := New(dir, nil, strings.NewReader(""), &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "program")
	if err := os.WriteFile(program, guestProgram(t), 0o755); err != nil {
		t.Fatal(err)
	}
	process, err := kernel.Begin(0, program, []string{program}, dir, nil, fd.Streams{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	status, err := kernel.RunRegistered(t.Context(), process)
	if err != nil {
		t.Fatal(err)
	}
	if status.Code != 7 || status.Signal != 0 {
		t.Fatalf("终态=%+v want 退出码 7", status)
	}
	if output.String() != "hi" {
		t.Fatalf("标准输出=%q want hi", output.String())
	}
}
