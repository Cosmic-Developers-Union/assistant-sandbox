package sandbox

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWASIVMOwnsIdentityRandomnessAndNestedProcesses(t *testing.T) {
	runtime, err := newTestRuntime(t.Context(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	var samples []string
	for range 2 {
		dir := t.TempDir()
		session, err := runtime.Bind(dir)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		script := `printf '%s %s %s %s\n' "$$" "$PPID" "$UID" "$GID"; printf '%s %s\n' "$RANDOM" "$SRANDOM"; bash -c 'printf "%s %s\n" "$$" "$PPID"'; touch touched; for ((i=0;i<300;i++)); do bash -c 'basename /work/value' >/dev/null; done`
		result, err := session.Execute(t.Context(), dir, script, &out, &out)
		if err := session.Close(); err != nil {
			t.Error(err)
		}
		if err != nil || result.ExitCode != 0 {
			t.Fatal(result, err, out.String())
		}
		if !strings.HasPrefix(out.String(), "2 1 0 0\n") || !strings.Contains(out.String(), "3 2\n") {
			t.Fatal(out.String())
		}
		info, err := os.Stat(filepath.Join(dir, "touched"))
		if err != nil || info.ModTime().UnixNano() != 0 {
			t.Fatal(info, err)
		}
		samples = append(samples, out.String())
	}
	if samples[0] != samples[1] {
		t.Fatal("独立 VM 的确定性资源初始状态不同", samples)
	}
	dir := t.TempDir()
	for _, script := range []string{`command eval 'echo denied &'`, `command eval 'cat <(printf value)'`, `command eval 'time true'`, `command pwd -P`} {
		var out bytes.Buffer
		result, err := runtime.Execute(t.Context(), dir, script, &out, &out)
		if err != nil || result.ExitCode == 0 {
			t.Fatal(script, result, err, out.String())
		}
	}
}
