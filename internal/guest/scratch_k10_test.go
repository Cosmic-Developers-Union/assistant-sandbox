package guest_test

import (
	"context"
	"os"
	"testing"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func TestScratchK10(t *testing.T) {
	program, err := os.ReadFile("testdata/k10-shm")
	if err != nil {
		t.Fatal(err)
	}
	kernel, system := guestKernel(t, program)
	defer func() { _ = kernel.Close(context.Background()) }()
	if err := system.Mkdir(context.Background(), "/lib", 0o755); err != nil {
		t.Fatal(err)
	}
	loader := f10Fixture(t, "lib/ld-musl-riscv64.so.1", f10LoaderDigest)
	for _, p := range []string{"/lib/ld-musl-riscv64.so.1", "/lib/libc.so"} {
		file, err := system.Open(context.Background(), p, vmfs.Create|vmfs.ReadWrite, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(loader); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	stdout, code := runGuestOnKernelWith(t, kernel, system, program)
	for _, e := range kernel.Trace().Entries() {
		if true {
			t.Logf("syscall %d result=%d", e.Number, e.Result)
		}
	}
	for _, e := range kernel.Trace().Entries() {
		if true {
			t.Logf("syscall %d result=%d", e.Number, e.Result)
		}
	}
	t.Logf("exit=%d stdout=%q", code, stdout)
}
