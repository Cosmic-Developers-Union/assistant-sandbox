package devroot

import (
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
)

func TestInstallProvidesDevelopmentTools(t *testing.T) {
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{MaxFileBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := Install(t.Context(), system); err != nil {
		t.Fatal(err)
	}
	t.Logf("install took %s", time.Since(start))
	for _, name := range []string{
		"/usr/bin/python", "/usr/bin/python3.12", "/usr/bin/node-bin",
		"/usr/bin/npm", "/usr/bin/cc", "/usr/bin/git", "/sbin/apk",
	} {
		if _, err := system.Stat(t.Context(), name); err != nil {
			t.Fatalf("缺少 %s: %v", name, err)
		}
	}
}
