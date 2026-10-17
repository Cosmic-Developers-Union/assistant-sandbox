package shell

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestCommonTextCommands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("beta:2\nalpha:1\nalpha:1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		script, out string
		code        int
	}{
		{"head -n 1 data", "beta:2\n", 0}, {"tail -n 1 data", "alpha:1\n", 0}, {"head -n 0 data", "", 0},
		{"cat data | wc -l", "3\n", 0}, {"printf 'a b' | wc -wc", "2 3\n", 0}, {"wc data", "3 3 23 data\n", 0},
		{"sort -u data", "alpha:1\nbeta:2\n", 0}, {"sort -r data", "beta:2\nalpha:1\nalpha:1\n", 0}, {"printf '10\n2\n1\n' | sort -n", "1\n2\n10\n", 0},
		{"sort data | uniq -c", "2 alpha:1\n1 beta:2\n", 0}, {"sort data | uniq", "alpha:1\nbeta:2\n", 0},
		{"cut -d : -f 2 data", "2\n1\n1\n", 0}, {"printf 'one\na:b\n' | cut -d : -f 2", "one\nb\n", 0},
		{"grep -n beta data", "1:beta:2\n", 0}, {"grep -i ALPHA data", "alpha:1\nalpha:1\n", 0}, {"grep -v alpha data", "beta:2\n", 0},
		{"grep -q alpha data", "", 0}, {"grep absent data", "", 1}, {"grep -F 'alpha:1' data", "alpha:1\nalpha:1\n", 0}, {"grep -En '^(beta|none)' data", "1:beta:2\n", 0},
		{"basename /work/a.txt .txt", "a\n", 0}, {"dirname /work/a.txt /tmp/b", "/work\n/tmp\n", 0},
		{"printf kept | tee saved; printf added | tee -a saved; cat saved", "keptaddedkeptadded", 0},
		{"printf no-newline | head -n 1", "no-newline", 0}, {"printf no-newline | tail -n 1", "no-newline", 0},
		{"printf '' | head", "", 0}, {"printf '' | sort", "", 0},
		{"grep '[' data", "", 2}, {"grep", "", 2}, {"head --unknown data", "", 2}, {"tail -n -1 data", "", 2},
		{"cut -d xx -f 1 data", "", 2}, {"cut -f x data", "", 2}, {"cut -f 0 data", "", 2}, {"cut -f 1 -d : data", "beta\nalpha\nalpha\n", 0},
		{"head missing", "", 2}, {"basename", "", 2}, {"basename a b c", "", 2}, {"tee /missing-directory/file < data", "beta:2\nalpha:1\nalpha:1\n", 2},
		{"grep -n beta data data", "data:1:beta:2\ndata:1:beta:2\n", 0},
	} {
		t.Run(tc.script, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			err := run(t.Context(), tc.script, dir, nil, strings.NewReader(""), &out, &diagnostic)
			code := 0
			if err != nil {
				status, ok := errors.AsType[interp.ExitStatus](err)
				if !ok {
					t.Fatal(err)
				}
				code = int(status)
			}
			if code != tc.code || out.String() != tc.out {
				t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), diagnostic.String())
			}
		})
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "value"), []byte("needle\n"), 0o600); err != nil {
		t.Error(err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), "grep -r needle sub", dir, nil, nil, &out, &out); err != nil || !strings.Contains(out.String(), "value:needle") {
		t.Fatal(out.String(), err)
	}
}

func TestInfiniteProducerPipelineTerminates(t *testing.T) {
	for _, tc := range []struct {
		script, out string
	}{
		{"yes | head -n 3", "y\ny\ny\n"},
		{"yes hello world | head -n 2", "hello world\nhello world\n"},
		{"yes | head -n 0", ""},
	} {
		t.Run(tc.script, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if err := run(t.Context(), tc.script, t.TempDir(), nil, nil, &out, &diagnostic); err != nil {
				t.Fatalf("err=%v stderr=%q", err, diagnostic.String())
			}
			if out.String() != tc.out {
				t.Fatalf("out=%q want %q", out.String(), tc.out)
			}

			_ = diagnostic
		})
	}
}

func TestScriptShebangThroughRegisteredInterpreter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/bash\necho from-script\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(dir, "run.sh")
	for _, script := range []string{"bash run.sh", "bash ./run.sh", "bash " + absolute} {
		t.Run(script, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if err := run(t.Context(), script, dir, nil, nil, &out, &diagnostic); err != nil {
				t.Fatalf("err=%v stderr=%q", err, diagnostic.String())
			}
			if out.String() != "from-script\n" {
				t.Fatalf("out=%q stderr=%q", out.String(), diagnostic.String())
			}
		})
	}
}

func TestScriptExecutedByShebangInterpreterPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/bash\necho from-script\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		script, out string
		code        int
	}{
		{"./run.sh", "from-script\n", 0},
		{"./run.sh extra", "from-script\n", 0},
		{"bash run.sh", "from-script\n", 0},

		{"run.sh", "", 127},
	} {
		t.Run(tc.script, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			err := run(t.Context(), tc.script, dir, nil, nil, &out, &diagnostic)
			code := 0
			if err != nil {
				status, ok := errors.AsType[interp.ExitStatus](err)
				if !ok {
					t.Fatalf("err=%v stderr=%q", err, diagnostic.String())
				}
				code = int(status)
			}
			if code != tc.code || out.String() != tc.out {
				t.Fatalf("code=%d out=%q want code=%d out=%q stderr=%q", code, out.String(), tc.code, tc.out, diagnostic.String())
			}
		})
	}
}
