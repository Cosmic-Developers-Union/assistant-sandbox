package ls

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestOutputFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0600); err != nil {
		t.Error(err)
	}
	for _, args := range [][]string{nil, {"file"}} {
		if err := commandtest.Invoke(t.Context(), Run, dir, args, nil, commandtest.FailWriter{}, io.Discard); err == nil {
			t.Fatal("输出失败被吞掉")
		}
	}
}
