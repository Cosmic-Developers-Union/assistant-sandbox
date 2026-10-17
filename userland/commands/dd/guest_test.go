package ddcmd

import (
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

func bindDir(t *testing.T, dir string) {
	t.Helper()
	release := vfs.Bind(filesystem.NewFilesystem(dir), dir, nil)
	t.Cleanup(release)
}
