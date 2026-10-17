package ls

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestGuestAdapter(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("alpha\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"."}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "data") {
		t.Fatal(result)
	}

	result = commandtest.Run(t, Run, dir, []string{"--assistant-invalid-flag"}, "")
	if result.Code != 2 || result.Stderr == "" {
		t.Fatal(result)
	}
}
