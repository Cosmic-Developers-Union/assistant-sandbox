package shasum

import (
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestOutputFailureIsReported(t *testing.T) {
	if err := commandtest.Invoke(t.Context(), Run, t.TempDir(), nil, strings.NewReader("value"), commandtest.FailWriter{}, io.Discard); err == nil {
		t.Fatal("输出失败被吞掉")
	}
}
