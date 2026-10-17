package memfs

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func TestChownDeclaresOwnership(t *testing.T) {
	ctx := context.Background()
	var system vmfs.System = mustFS(t)
	owner, ok := system.(vmfs.Ownership)
	if !ok {
		t.Fatal("memfs 应实现 vm/fs.Ownership")
	}
	if _, err := system.Open(ctx, "/a", vmfs.Create|vmfs.WriteOnly, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := system.Stat(ctx, "/a")
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := info.Sys().(*vmfs.Metadata)
	if !ok {
		t.Fatalf("Sys() 应是 *vm/fs.Metadata: %T", info.Sys())
	}
	if meta.UID != 0 || meta.GID != 0 {
		t.Fatalf("新节点默认属主 = %d:%d", meta.UID, meta.GID)
	}

	if err := owner.Chown(ctx, "/a", vmfs.ChownUnchanged, 5); err != nil {
		t.Fatalf("Chown: %v", err)
	}
	info, _ = system.Stat(ctx, "/a")
	meta = info.Sys().(*vmfs.Metadata)
	if meta.UID != 0 || meta.GID != 5 {
		t.Fatalf("只改属组后 = %d:%d", meta.UID, meta.GID)
	}

	if err := owner.Chown(ctx, "/a", 3, vmfs.ChownUnchanged); err != nil {
		t.Fatal(err)
	}
	info, _ = system.Stat(ctx, "/a")
	meta = info.Sys().(*vmfs.Metadata)
	if meta.UID != 3 || meta.GID != 5 {
		t.Fatalf("改属主后 = %d:%d", meta.UID, meta.GID)
	}
}

func TestChownRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	var system vmfs.System = mustFS(t)
	owner := system.(vmfs.Ownership)
	if err := owner.Chown(ctx, "/missing", 1, 1); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失路径应报 ENOENT: %v", err)
	}
	if _, err := system.Open(ctx, "/a", vmfs.Create|vmfs.WriteOnly, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := owner.Chown(ctx, "/a", -2, 0); !errors.Is(err, errno.Invalid) {
		t.Fatalf("负属主应报 EINVAL: %v", err)
	}
	if err := owner.Chown(ctx, "/a", 0, 1<<40); !errors.Is(err, errno.Invalid) {
		t.Fatalf("越界属组应报 EINVAL: %v", err)
	}
}

func mustFS(t *testing.T) *FS {
	t.Helper()
	system, err := New(&clock.Virtual{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return system
}
