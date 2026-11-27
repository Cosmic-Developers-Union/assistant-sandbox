//go:build linux && amd64

package instance

import (
	"context"
	"errors"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	platformnet "github.com/Cosmic-Developers-Union/assistant-sandbox/platform/network"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/resources"
)

func TestNativeVMIsolatesProcessAndResources(t *testing.T) {
	machine, err := newNativeVMForTest(t, Options{})
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
	machine, err := newNativeVMForTest(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	runNativeShell(t, machine, `printf alpha > /work/shared; test "$(cat /work/shared)" = alpha`, nil)
	runNativeShell(t, machine, `test "$(cat /work/shared)" = alpha; printf beta > /work/shared`, nil)

	other, err := newNativeVMForTest(t, Options{})
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

func newNativeVMForTest(t *testing.T, opts Options) (*NativeVM, error) {
	t.Helper()
	if _, err := probeNativeBackend(t.Context()); err != nil {
		if os.Getenv("ASSISTANT_REQUIRE_NATIVE") == "1" {
			t.Fatalf("required native isolation unavailable: %v", err)
		}
		t.Skipf("linux/amd64 native isolation unavailable on this host: %v", err)
	}
	machine, err := newNativeVM(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return machine, nil
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

func TestNativeProcessExitWaitCancelAndSignal(t *testing.T) {
	machine, err := newNativeVMForTest(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = machine.Close(t.Context()) }()
	for _, code := range []int{0, 7} {
		state, err := machine.Run(t.Context(), NativeExecSpec{
			Program: "/bin/sh", Args: []string{"-c", "exit " + strconv.Itoa(code)}, Dir: "/work",
		})
		if err != nil || state.Code != code || state.Signal != 0 {
			t.Fatalf("exit %d: state=%+v err=%v", code, state, err)
		}
	}

	process, err := machine.Exec(t.Context(), NativeExecSpec{Program: "/bin/sleep", Args: []string{"30"}, Dir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := process.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled wait error=%v", err)
	}
	if err := process.Cancel(); err != nil {
		t.Fatal(err)
	}
	state, err := process.Wait(t.Context())
	if err != nil || state.Signal != syscall.SIGKILL || state.Code != 128+int(syscall.SIGKILL) {
		t.Fatalf("cancelled process state=%+v err=%v", state, err)
	}

	signaled, err := machine.Exec(t.Context(), NativeExecSpec{
		Program: "/bin/sh",
		Args:    []string{"-c", `trap "exit 42" TERM; printf ready > /work/signal-ready; while :; do sleep 1; done`},
		Dir:     "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForNativeFile(t, filepath.Join(machine.Root(), "work", "signal-ready"), "ready")
	if err := signaled.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	state, err = signaled.Wait(t.Context())
	if err != nil || state.Code != 42 || state.Signal != 0 {
		t.Fatalf("signaled process state=%+v err=%v", state, err)
	}
}

func TestNativeShutdownKillsProcessTreeAndReleasesRoot(t *testing.T) {
	machine, err := newNativeVMForTest(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := machine.Root()
	hostDir := t.TempDir()
	if err := machine.Mount(hostDir, "/work/host"); err != nil {
		t.Fatal(err)
	}
	_, err = machine.Exec(t.Context(), NativeExecSpec{
		Program: "/bin/sh",
		Args: []string{"-c", `while :; do echo tick > /work/host/heartbeat; sleep 1; done &
printf ready > /work/host/ready
wait`},
		Dir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForNativeFile(t, filepath.Join(hostDir, "ready"), "ready")
	waitForNativePath(t, filepath.Join(hostDir, "heartbeat"))
	before, err := os.ReadFile(filepath.Join(hostDir, "heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private root still exists: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(hostDir, "heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("background process tree still running: heartbeat %q -> %q", before, after)
	}
	if err := machine.Shutdown(t.Context()); err != nil {
		t.Fatalf("repeated shutdown: %v", err)
	}
}

func waitForNativePath(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(name); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("path %s was not created", name)
}

func waitForNativeFile(t *testing.T, name, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(name)
		if err == nil && ((want == "" && len(data) > 0) || strings.TrimSpace(string(data)) == want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s did not contain %q", name, want)
}

func TestNativeLoopbackIsolationAndDeniedEgress(t *testing.T) {
	machine, _ := newNativeVMForTest(t, Options{})
	defer func() { _ = machine.Close(t.Context()) }()
	server := `const net = require('node:net');
const server = net.createServer(socket => {
  socket.end('native-loopback', () => { if (process.env.SERVER_LONG !== '1') server.close(); });
});
server.listen(39081, '127.0.0.1', () => {
  if (process.env.SERVER_LONG !== '1') console.log('listening');
});`
	client := `const net = require('node:net');
const socket = net.connect(Number(process.argv[2]), '127.0.0.1');
let data = '';
socket.on('data', chunk => data += chunk);
socket.on('end', () => { console.log(data); socket.end(); });
socket.on('error', error => { console.error(error.message); process.exit(1); });`
	if err := os.WriteFile(filepath.Join(machine.Root(), "work", "server.js"), []byte(server), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine.Root(), "work", "client.js"), []byte(client), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "loopback-output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	state, err := machine.Run(t.Context(), NativeExecSpec{
		Program: "/bin/sh",
		Args: []string{"-c", `node /work/server.js & server=$!
i=0
until node /work/client.js 39081; do
  i=$((i + 1)); [ "$i" -lt 30 ] || exit 1; sleep 0.1
done
wait "$server"`},
		Dir:    "/work",
		Stdout: output,
		Stderr: output,
	})
	if err != nil || state.Code != 0 {
		t.Fatalf("loopback exchange state=%+v err=%v", state, err)
	}
	if data, err := os.ReadFile(output.Name()); err != nil || !strings.Contains(string(data), "native-loopback") {
		t.Fatalf("loopback output=%q err=%v", data, err)
	}

	other, _ := newNativeVMForTest(t, Options{})
	defer func() { _ = other.Close(t.Context()) }()
	longServer, err := machine.Exec(t.Context(), NativeExecSpec{
		Program: "/usr/bin/node", Args: []string{"/work/server.js"}, Dir: "/work",
		Env: map[string]string{"SERVER_LONG": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = longServer.Cancel() }()
	time.Sleep(300 * time.Millisecond)
	state, err = other.Run(t.Context(), NativeExecSpec{
		Program: "/bin/sh", Args: []string{"-c", `if node /work/client.js 39081; then exit 1; fi`}, Dir: "/work",
	})
	if err != nil || state.Code != 0 {
		t.Fatalf("cross-VM loopback was reachable: state=%+v err=%v", state, err)
	}

	hostListener, err := stdnet.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hostListener.Close() }()
	port := hostListener.Addr().(*stdnet.TCPAddr).Port
	state, err = machine.Run(t.Context(), NativeExecSpec{
		Program: "/bin/sh", Args: []string{"-c", `if node /work/client.js "$1"; then exit 1; fi`, "sh", strconv.Itoa(port)}, Dir: "/work",
	})
	if err != nil || state.Code != 0 {
		t.Fatalf("default egress was not denied: state=%+v err=%v", state, err)
	}
}

func nativeLoopbackTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	listener, err := stdnet.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestNativeAuthorizedEgressProxy(t *testing.T) {
	allowed := nativeLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "allowed")
	}))
	denied := nativeLoopbackTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "must-not-be-reachable")
	}))
	allowedPort := uint16(allowed.Listener.Addr().(*stdnet.TCPAddr).Port) //nolint:gosec // TCP listener ports are 16-bit
	policy := vmnet.NewEgressPolicy(vmnet.EgressRule{
		Protocol: vmnet.ProtocolTCP,
		Prefix:   netip.MustParsePrefix("127.0.0.0/8"),
		PortMin:  allowedPort,
		PortMax:  allowedPort,
	})
	machine, _ := newNativeVMForTest(t, Options{
		HostNetwork: platformnet.Dialer{}, Egress: policy,
	})
	defer func() { _ = machine.Close(t.Context()) }()
	deniedPort := denied.Listener.Addr().(*stdnet.TCPAddr).Port
	output, err := os.CreateTemp(t.TempDir(), "egress-output")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	script := `import sys
import urllib.request
import urllib.error
allowed = urllib.request.urlopen("http://127.0.0.2:%s/" % sys.argv[1]).read().decode()
print(allowed)
try:
    urllib.request.urlopen("http://127.0.0.2:%s/" % sys.argv[2])
except urllib.error.HTTPError as error:
    print("DENIED", error.code)
else:
    raise SystemExit("denied egress succeeded")`
	state, err := machine.Run(t.Context(), NativeExecSpec{
		Program: "/usr/bin/python3.12",
		Args:    []string{"-c", script, strconv.Itoa(int(allowedPort)), strconv.Itoa(deniedPort)},
		Dir:     "/work",
		Stdout:  output,
		Stderr:  output,
	})
	if err != nil || state.Code != 0 {
		data, _ := os.ReadFile(output.Name())
		t.Fatalf("egress state=%+v err=%v output=%q", state, err, data)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil || !strings.Contains(string(data), "allowed") || !strings.Contains(string(data), "DENIED 502") {
		t.Fatalf("egress output=%q err=%v", data, err)
	}
}

func TestNativeResourceLimitsAndStatus(t *testing.T) {
	opts := Options{
		Session:      resources.Limits{Memory: 64 << 20},
		CPUs:         1,
		MaxProcesses: 8,
	}
	machine, _ := newNativeVMForTest(t, opts)
	defer func() { _ = machine.Close(t.Context()) }()
	status, err := machine.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.MemoryMax != 64<<20 || status.CPUQuotaMicros != 100000 ||
		status.CPUPeriodMicros != 100000 || status.PidsMax != 8 {
		t.Fatalf("unexpected native status: %+v", status)
	}
	swapMax, err := readNativeUint(filepath.Join(machine.cgroup, "memory.swap.max"))
	if err != nil {
		t.Fatalf("read memory.swap.max: %v", err)
	}
	if swapMax != 0 {
		t.Fatalf("memory.swap.max=%d, want 0 so memory.max includes anonymous pages", swapMax)
	}
	runNativeShell(t, machine, `printf within-limit`, nil)

	spin, err := machine.Exec(t.Context(), NativeExecSpec{
		Program: "/bin/sh", Args: []string{"-c", `while :; do :; done`}, Dir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	status, err = machine.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.CPUUsageMicros == 0 {
		t.Fatal("CPU usage was not metered")
	}
	t.Logf("cpu quota=%d/%d usec, observed usage=%d usec", status.CPUQuotaMicros, status.CPUPeriodMicros, status.CPUUsageMicros)
	if err := spin.Cancel(); err != nil {
		t.Fatal(err)
	}
	if _, err := spin.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	adjacent, _ := newNativeVMForTest(t, opts)
	defer func() { _ = adjacent.Close(t.Context()) }()
	runNativeShell(t, adjacent, `printf adjacent-ok`, nil)

	memoryState, err := machine.Run(t.Context(), NativeExecSpec{
		Program: "/usr/bin/python3.12",
		Args: []string{"-c", `x = bytearray(128 * 1024 * 1024)
for offset in range(0, len(x), 4096):
    x[offset] = 1
print(len(x))`},
		Dir: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	if memoryState.Code == 0 && memoryState.Signal == 0 {
		t.Fatalf("memory overflow was not stopped: %+v", memoryState)
	}
	runNativeShell(t, adjacent, `printf still-ok`, nil)

	limited, _ := newNativeVMForTest(t, Options{MaxProcesses: 8})
	defer func() { _ = limited.Close(t.Context()) }()
	diagnostic, err := os.CreateTemp(t.TempDir(), "pids-stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = diagnostic.Close() }()
	process, err := limited.Exec(t.Context(), NativeExecSpec{
		Program: "/bin/sh",
		Args:    []string{"-c", `i=0; while [ "$i" -lt 32 ]; do sleep 30 & i=$((i + 1)); done; wait`},
		Dir:     "/work",
		Stderr:  diagnostic,
	})
	if err != nil {
		data, readErr := os.ReadFile(diagnostic.Name())
		t.Fatalf("start process-limit guest: %v; stderr=%q read=%v", err, data, readErr)
	}
	time.Sleep(200 * time.Millisecond)
	status, err = limited.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.PidsCurrent > status.PidsMax {
		t.Fatalf("pids.current=%d exceeds pids.max=%d", status.PidsCurrent, status.PidsMax)
	}
	if err := process.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	if _, err := process.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(diagnostic.Name()); err != nil || !strings.Contains(string(data), "can't fork") {
		t.Fatalf("process-limit diagnostic=%q err=%v", data, err)
	}
}

func TestNativeRejectsInstructionBudget(t *testing.T) {
	_, err := newNativeVM(t.Context(), Options{Process: resources.Limits{Work: 1}})
	if !errors.Is(err, ErrNativeWorkBudgetUnsupported) {
		t.Fatalf("error=%v want ErrNativeWorkBudgetUnsupported", err)
	}
}

func TestNativeProcessTimeout(t *testing.T) {
	machine, _ := newNativeVMForTest(t, Options{ProcessTimeout: uint64(50 * time.Millisecond)})
	defer func() { _ = machine.Close(t.Context()) }()
	started := time.Now()
	state, err := machine.Run(t.Context(), NativeExecSpec{Program: "/bin/sleep", Args: []string{"30"}, Dir: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Signal != syscall.SIGKILL || time.Since(started) > 2*time.Second {
		t.Fatalf("timeout state=%+v elapsed=%s", state, time.Since(started))
	}
}

func TestNativeInitFailureDoesNotFallbackToHost(t *testing.T) {
	machine, err := newNativeVMForTest(t, Options{})
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
