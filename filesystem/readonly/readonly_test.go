package readonly

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func setup(t *testing.T) (*FS, vmfs.System) {
	t.Helper()
	base, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped, base
}

func TestNewRejectsNilBase(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func TestWritesRejectedAndReadsPass(t *testing.T) {
	wrapped, base := setup(t)
	ctx := context.Background()
	file, err := base.Open(ctx, "/data", vmfs.Create|vmfs.ReadWrite, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("read me")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}

	if _, err := wrapped.Open(ctx, "/data", vmfs.WriteOnly, 0); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if _, err := wrapped.Open(ctx, "/new", vmfs.Create, 0o644); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if err := wrapped.Mkdir(ctx, "/d", 0o755); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if err := wrapped.Remove(ctx, "/data"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if err := wrapped.Rename(ctx, "/data", "/x"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if err := wrapped.Link(ctx, "/data", "/y"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if err := wrapped.Symlink(ctx, "/data", "/z"); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}
	if err := wrapped.SetTimes(ctx, "/data", 0, 0); !errors.Is(err, errno.ReadOnlyFS) {
		t.Fatal(err)
	}

	data, err := readAll(ctx, wrapped, "/data")
	if err != nil || string(data) != "read me" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err := wrapped.Stat(ctx, "/data"); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.Lstat(ctx, "/data"); err != nil {
		t.Fatal(err)
	}
	if err := base.Mkdir(ctx, "/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if entries, err := wrapped.ReadDir(ctx, "/"); err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if _, err := wrapped.Readlink(ctx, "/data"); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
}

func readAll(ctx context.Context, system vmfs.System, path string) (data []byte, resultErr error) {
	file, err := system.Open(ctx, path, vmfs.ReadOnly, 0)
	if err != nil {
		return nil, err
	}
	data, resultErr = io.ReadAll(file)
	return data, errors.Join(resultErr, file.Close())
}
