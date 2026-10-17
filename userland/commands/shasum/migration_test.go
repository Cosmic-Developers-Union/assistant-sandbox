package shasum

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestVMFilesystemFailuresAreVisible(t *testing.T) {
	for _, tc := range []struct {
		operation string
		args      []string
	}{{"Read", []string{"file"}},
		{"MissingFS", []string{"file"}}} {
		t.Run(tc.operation, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte("value"), 0600); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sub", "child"), []byte("child"), 0600); err != nil {
				t.Error(err)
			}
			result := commandtest.RunFault(t, Run, dir, tc.args, "", tc.operation)
			if result.Code == 0 || result.Stderr == "" {
				t.Fatal(tc.operation, result)
			}
		})
	}
}

func TestVMCommandParameterAndDirectorySemantics(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
		code  int
	}{{[]string{"-a", "256"}, "input", 0},
		{[]string{"-a", "512"}, "input", 0},
		{[]string{"-a", "3"}, "input", 2}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte("value"), 0600); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "other"), []byte("other"), 0600); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "occupied"), []byte("occupied"), 0600); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sub", "child"), []byte("child"), 0600); err != nil {
			t.Error(err)
		}
		result := commandtest.Run(t, Run, dir, tc.args, tc.input)
		if result.Code != tc.code {
			t.Fatal(tc.args, result)
		}
	}
}
