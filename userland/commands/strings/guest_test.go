package stringscmd

import (
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/platform/filesystem"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

func guestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	release := vfs.Bind(filesystem.NewFilesystem(dir), dir, nil)
	t.Cleanup(release)
	return dir
}

func bindDir(t *testing.T, dir string) {
	t.Helper()
	release := vfs.Bind(filesystem.NewFilesystem(dir), dir, nil)
	t.Cleanup(release)
}
