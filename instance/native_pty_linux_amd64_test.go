//go:build linux && amd64

package instance

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/model"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func TestNativeModelSuite(t *testing.T) {
	if _, err := probeNativeBackend(t.Context()); err != nil {
		if os.Getenv("ASSISTANT_REQUIRE_NATIVE") == "1" {
			t.Fatalf("required native isolation unavailable: %v", err)
		}
		t.Skipf("linux/amd64 native isolation unavailable on this host: %v", err)
	}
	model.Suite(t, func() (model.VM, error) {
		return newNativeModelVM(t.Context(), Options{})
	})
}

func TestNativePTYResizeInterruptAndEOF(t *testing.T) {
	machine, err := newNativeVMForTest(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	session, err := machine.StartSession(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close(t.Context()) }()

	output, code, err := session.runScript(t.Context(), `printf 'size:%s\n' "$(stty size)"`)
	if err != nil || code != 0 || !strings.Contains(output, "size:24 100") {
		t.Fatalf("initial PTY size output=%q code=%d err=%v", output, code, err)
	}
	if err := session.Resize(40, 132); err != nil {
		t.Fatal(err)
	}
	output, code, err = session.runScript(t.Context(), `printf 'size:%s\n' "$(stty size)"`)
	if err != nil || code != 0 || !strings.Contains(output, "size:40 132") {
		t.Fatalf("resized PTY size output=%q code=%d err=%v", output, code, err)
	}
	lines, rows, columns := session.ScreenView()
	if len(lines) != 40 || rows >= len(lines) || columns < 0 {
		t.Fatalf("screen lines=%d cursor=%d,%d", len(lines), rows, columns)
	}

	if err := session.Write([]byte("printf 'foreground-ready\\n'; sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := session.WaitOutput(ctx, "foreground-ready"); err != nil {
		t.Fatal(err)
	}
	// Ensure execve(sleep) has completed before delivering SIGINT; otherwise
	// the signal can race Bash between the preceding printf and the command.
	time.Sleep(100 * time.Millisecond)
	if err := session.Interrupt(); err != nil {
		t.Fatal(err)
	}
	output, code, err = session.runScript(ctx, `printf 'shell-alive\n'`)
	if err != nil || code != 0 || !strings.Contains(output, "shell-alive") {
		t.Fatalf("shell after Ctrl-C output=%q code=%d err=%v", output, code, err)
	}

	if err := session.Write([]byte{0x04}); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancelWait := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelWait()
	state, err := session.Wait(waitCtx)
	if err != nil || state.Code != 0 || state.Signal != 0 {
		t.Fatalf("Ctrl-D exit=%+v err=%v", state, err)
	}
}

func TestNativePTYShutdownClosesSession(t *testing.T) {
	machine, err := newNativeVMForTest(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := machine.StartSession(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	root := machine.Root()
	if err := machine.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native root still exists: %v", err)
	}
	if err := session.Write([]byte("printf no\n")); !errors.Is(err, errno.Cancelled) {
		t.Fatalf("closed session write error=%v", err)
	}
}
