package mv

import (
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"data", "moved"}, "")
	if result.Code != 0 {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "moved"))
	if string(body) != "alpha\n" {
		t.Fatal(string(body))
	}
	result = commandtest.Run(t, Run, dir, []string{"--assistant-invalid-flag"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatal(result)
	}
}
