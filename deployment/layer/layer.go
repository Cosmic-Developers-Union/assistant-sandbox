package layer

import (
	"context"
	"errors"
	"io"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/tty"
)

type Layer struct {
	image    image.LayerImage
	session  *memfs.FS
	base     *memfs.FS
	readonly *readonly.FS
	clock    clock.Source
}

func NewLayer(ctx context.Context, image image.LayerImage, clk clock.Source) (*Layer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if image == nil {
		return nil, errno.Invalid
	}
	if clk == nil {
		clk = &clock.Virtual{}
	}
	base, err := newMemoryStore(clk)
	if err != nil {
		return nil, err
	}
	session, err := newMemoryStore(clk)
	if err != nil {
		return nil, err
	}
	if err := materialize(ctx, image, base); err != nil {
		return nil, err
	}
	if err := materialize(ctx, image, session); err != nil {
		return nil, err
	}
	view, err := newReadOnlyView(base)
	if err != nil {
		return nil, err
	}
	return &Layer{image: image, session: session, base: base, readonly: view, clock: clk}, nil
}

func materialize(ctx context.Context, image image.LayerImage, target vmfs.System) error {
	for _, name := range image.Paths() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ensureDir(ctx, target, path.Dir(name)); err != nil {
			return err
		}
		file, err := target.Open(ctx, name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, 0o644)
		if err != nil {
			return errno.Wrap(err, "物化镜像文件 "+name)
		}
		reader, err := image.Open(ctx, name)
		if err != nil {
			return errors.Join(err, file.Close())
		}
		_, copyErr := io.Copy(file, reader)
		fileErr := file.Close()
		readerErr := reader.Close()
		if err := errors.Join(copyErr, fileErr, readerErr); err != nil {
			return errno.Wrap(err, "写入镜像文件 "+name)
		}
	}
	return nil
}

func ensureDir(ctx context.Context, target vmfs.System, dir string) error {
	if dir == "/" || dir == "." || dir == "" {
		return nil
	}
	if err := ensureDir(ctx, target, path.Dir(dir)); err != nil {
		return err
	}
	if err := target.Mkdir(ctx, dir, 0o755); err != nil && !errors.Is(err, errno.Exist) {
		return errno.Wrap(err, "建立镜像目录 "+dir)
	}
	return nil
}

func (l *Layer) Base() vmfs.System { return l.readonly }

func (l *Layer) Session() vmfs.System { return l.session }

func (l *Layer) Manifest(ctx context.Context) (image.Manifest, error) {
	return l.image.Manifest(ctx)
}

func (l *Layer) Snapshot(ctx context.Context) (vmfs.Snapshotted, error) {
	return l.session.Snapshot(ctx)
}

func (l *Layer) Restore(ctx context.Context, snapshot vmfs.Snapshotted) error {
	if snapshot == nil {
		return errno.Invalid
	}
	return snapshot.Restore(ctx, l.session)
}

var (
	newMemoryStore  = func(clk clock.Source) (*memfs.FS, error) { return memfs.New(clk, memfs.Options{}) }
	newReadOnlyView = func(base *memfs.FS) (*readonly.FS, error) { return readonly.New(base) }
)

func AttachTerminal(terminals *tty.Terminal, kernel *vm.Kernel, process proc.Proc) (func(), error) {
	if terminals == nil || kernel == nil {
		return nil, errno.Invalid
	}
	attached := make([]uint32, 0, 3)
	for _, id := range []uint32{0, 1, 2} {
		if err := kernel.InstallFD(process.ID, id, terminals); err != nil {

			for _, done := range attached {
				_ = kernel.CloseFD(process.ID, done)
			}
			return nil, errno.Wrap(err, "接线终端标准流")
		}
		attached = append(attached, id)
	}
	cleanup := func() {
		for _, id := range attached {
			_ = kernel.CloseFD(process.ID, id)
		}
	}
	return cleanup, nil
}
