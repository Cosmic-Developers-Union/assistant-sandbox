//go:build linux && amd64

package instance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNativeVMIsolatesProcessAndResources(t *testing.T) {
	machine, err := newNativeVM(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()

	sentinel := filepath.Join(t.TempDir(), "host-secret")
	if err := os.WriteFile(sentinel, []byte("host-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASSISTANT_SENTINEL", "host-only")
	extra, err := os.Open(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = extra.Close() }()
	output, err := os.CreateTemp(t.TempDir(), "native-output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()

	script := `set -eu
test "$(uname -m)" = x86_64
test "$$" = 1
test "$(hostname)" = sandbox
test -z "${ASSISTANT_SENTINEL:-}"
test ! -e "$1"
test ! -e "/proc/self/fd/$2"
test ! -e /.old-root
grep -q '^CapEff:[[:space:]]*0000000000000000' /proc/self/status
grep -q '^NoNewPrivs:[[:space:]]*1' /proc/self/status
if mount -t proc proc /proc; then exit 20; fi
for name in user mnt pid net ipc uts cgroup; do
  printf "%s=" "$name"
  readlink "/proc/self/ns/$name"
done
printf "arch=%s pid=%s\n" "$(uname -m)" "$$"
`
	state, err := machine.Run(t.Context(), NativeExecSpec{
		Program: "/bin/sh",
		Args:    []string{"-c", script, "sh", sentinel, strconv.Itoa(int(extra.Fd()))},
		Dir:     "/work",
		Stdout:  output,
		Stderr:  output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Code != 0 || state.Signal != 0 {
		t.Fatalf("guest exit=%+v", state)
	}
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "arch=x86_64 pid=1") {
		t.Fatalf("guest output=%q", text)
	}
	guestNamespaces := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		name, target, ok := strings.Cut(line, "=")
		if ok && strings.HasSuffix(target, "]") {
			guestNamespaces[name] = target
		}
	}
	for _, name := range []string{"user", "mnt", "pid", "net", "ipc", "uts", "cgroup"} {
		host, err := os.Readlink("/proc/self/ns/" + name)
		if err != nil {
			t.Fatal(err)
		}
		guest := guestNamespaces[name]
		if guest == "" || guest == host {
			t.Fatalf("namespace %s host=%q guest=%q", name, host, guest)
		}
	}
}

func TestNativeVMFileStateAndAuthorizedMounts(t *testing.T) {
	machine, err := newNativeVM(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	runNativeShell(t, machine, `printf alpha > /work/shared; test "$(cat /work/shared)" = alpha`, nil)
	runNativeShell(t, machine, `test "$(cat /work/shared)" = alpha; printf beta > /work/shared`, nil)

	other, err := newNativeVM(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close(t.Context()) }()
	runNativeShell(t, other, `test ! -e /work/shared`, nil)

	parent := t.TempDir()
	authorized := filepath.Join(parent, "authorized")
	if err := os.Mkdir(authorized, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authorized, "from-host"), []byte("host-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside-secret")
	if err := os.WriteFile(outside, []byte("must-not-leak"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostLink := filepath.Join(parent, "authorized-link")
	if err := os.Symlink(authorized, hostLink); err != nil {
		t.Fatal(err)
	}
	if err := machine.Mount(hostLink, "/work/host"); err != nil {
		t.Fatal(err)
	}
	if got := machine.Mounts()["/work/host"]; got != authorized {
		t.Fatalf("resolved mount=%q want %q", got, authorized)
	}
	runNativeShell(t, machine, `set -eu
test "$(cat /work/host/from-host)" = host-value
printf guest-value > /work/host/from-guest
ln -s "$1" /work/host/absolute-escape
ln -s ../outside-secret /work/host/relative-escape
test ! -e /work/host/absolute-escape
test ! -e /work/host/relative-escape
if cat /work/host/absolute-escape; then exit 21; fi
if cat /work/host/relative-escape; then exit 22; fi
`, []string{outside})
	if data, err := os.ReadFile(filepath.Join(authorized, "from-guest")); err != nil || string(data) != "guest-value" {
		t.Fatalf("host mount write=%q err=%v", data, err)
	}
	runNativeShell(t, other, `test ! -e /work/host/from-host`, nil)
}

func runNativeShell(t *testing.T, machine *NativeVM, script string, args []string) {
	t.Helper()
	command := []string{"-c", script, "sh"}
	command = append(command, args...)
	state, err := machine.Run(t.Context(), NativeExecSpec{Program: "/bin/sh", Args: command, Dir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Code != 0 || state.Signal != 0 {
		t.Fatalf("guest exit=%+v script=%s", state, script)
	}
}

func TestNativeInitFailureDoesNotFallbackToHost(t *testing.T) {
	machine, err := newNativeVM(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	_, err = machine.Exec(t.Context(), NativeExecSpec{
		Program: "/bin/sh",
		Args:    []string{"-c", "echo should-not-run"},
		Dir:     "/missing-native-workdir",
	})
	if err == nil || !strings.Contains(err.Error(), "native init failed") {
		t.Fatalf("error=%v want named native init failure", err)
	}
}
