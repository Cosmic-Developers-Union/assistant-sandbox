package rmdir

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestMissingFilesystemCapability(t *testing.T) {
	result := commandtest.RunFault(t, Run, t.TempDir(), []string{"anything"}, "", "MissingFS")
	if result.Code == 0 || result.Stderr == "" {
		t.Fatalf("得到 %+v", result)
	}
}
