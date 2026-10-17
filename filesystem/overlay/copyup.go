package overlay

import (
	"context"
	"errors"
	"io"
	stdfs "io/fs"
	"sort"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func (o *Overlay) Open(ctx context.Context, path string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := o.rejectReserved(path); err != nil {
		return nil, err
	}
	masked, err := o.isWhiteout(ctx, path)
	if err != nil {
		return nil, err
	}

	if masked {
		if flags&vmfs.Create == 0 {
			return nil, errno.Wrap(errno.NotFound, "路径已被上层遮蔽: "+path)
		}
		if err := o.clearWhiteout(ctx, path); err != nil {
			return nil, err
		}
	}

	writable := flags&(vmfs.WriteOnly|vmfs.ReadWrite|vmfs.Truncate|vmfs.Append) != 0
	if !writable {

		if _, err := o.upper.Lstat(ctx, path); err == nil {
			return o.upper.Open(ctx, path, flags, mode)
		} else if !errors.Is(err, errno.NotFound) {
			return nil, err
		}
		return o.lower.Open(ctx, path, flags, mode)
	}

	if err := o.copyUpParents(ctx, path); err != nil {
		return nil, err
	}

	upperHasEntry, err := o.upperHas(ctx, path)
	if err != nil {
		return nil, err
	}
	openFlags := flags
	if !upperHasEntry {
		if err := o.copyUp(ctx, path, flags, mode); err != nil {
			return nil, err
		}
		if flags&vmfs.Create == 0 {

			openFlags |= vmfs.Create
		}
	}
	return o.upper.Open(ctx, path, openFlags, mode)
}

func (o *Overlay) upperHas(ctx context.Context, path string) (bool, error) {
	if _, err := o.upper.Lstat(ctx, path); err == nil {
		return true, nil
	} else if !errors.Is(err, errno.NotFound) {
		return false, err
	}
	return false, nil
}

func (o *Overlay) copyUp(ctx context.Context, path string, flags uint32, mode stdfs.FileMode) error {
	if path == "/" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := o.upper.Lstat(ctx, path); err == nil {
		return nil
	} else if !errors.Is(err, errno.NotFound) {
		return err
	}
	info, err := o.lower.Lstat(ctx, path)
	if err != nil {
		if errors.Is(err, errno.NotFound) {

			return nil
		}
		return err
	}
	switch {
	case info.IsDir():
		return o.upper.Mkdir(ctx, path, info.Mode().Perm())
	case info.Mode()&stdfs.ModeSymlink != 0:
		target, err := o.lower.Readlink(ctx, path)
		if err != nil {
			return err
		}
		return o.upper.Symlink(ctx, target, path)
	default:

		return o.copyFileContents(ctx, path, mode, flags|vmfs.Create)
	}
}

func (o *Overlay) copyFileContents(ctx context.Context, path string, mode stdfs.FileMode, flags uint32) (resultErr error) {
	source, err := o.lower.Open(ctx, path, vmfs.ReadOnly, 0)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, source.Close())
	}()

	createFlags := flags | vmfs.WriteOnly | vmfs.Create
	if flags&vmfs.Truncate != 0 {
		createFlags |= vmfs.Truncate
	}
	target, err := o.upper.Open(ctx, path, createFlags, mode)
	if err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, target.Close())
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			if _, writeErr := target.Write(buffer[:read]); writeErr != nil {
				return errors.Join(writeErr, target.Close())
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return errors.Join(readErr, target.Close())
		}
	}
	return target.Close()
}

func (o *Overlay) copyUpParents(ctx context.Context, path string) error {
	dir, _ := splitPath(path)
	if dir == "/" || dir == "" {
		return nil
	}
	return o.ensureDir(ctx, dir)
}

func (o *Overlay) ensureDir(ctx context.Context, path string) error {
	if path == "/" || path == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := o.upper.Lstat(ctx, path); err == nil {
		return nil
	} else if !errors.Is(err, errno.NotFound) {
		return err
	}
	dir, _ := splitPath(path)
	if err := o.ensureDir(ctx, dir); err != nil {
		return err
	}

	mode := stdfs.FileMode(0o755)
	if info, err := o.lower.Lstat(ctx, path); err == nil {
		if !info.IsDir() {

			return errno.Wrap(errno.NotDirectory, "上层写路径的父级在镜像里是文件: "+path)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, errno.NotFound) {
		return err
	}

	return o.upper.Mkdir(ctx, path, mode)
}

func (o *Overlay) placeWhiteout(ctx context.Context, path string) error {
	if path == "/" {
		return errno.Wrap(errno.Invalid, "不能遮蔽根目录")
	}
	marker := whiteoutPath(path)
	if err := o.ensureDir(ctx, mustDir(marker)); err != nil {
		return err
	}

	if _, err := o.upper.Lstat(ctx, marker); err == nil {
		return nil
	} else if !errors.Is(err, errno.NotFound) {
		return err
	}
	file, err := o.upper.Open(ctx, marker, vmfs.Create|vmfs.Exclusive|vmfs.WriteOnly, 0)
	if err != nil {
		return err
	}
	return file.Close()
}

func (o *Overlay) clearWhiteout(ctx context.Context, path string) error {
	marker := whiteoutPath(path)
	if _, err := o.upper.Lstat(ctx, marker); err != nil {
		if errors.Is(err, errno.NotFound) {
			return nil
		}
		return err
	}
	return o.upper.Remove(ctx, marker)
}

func (o *Overlay) inLowerOnly(ctx context.Context, path string) (bool, error) {
	if _, err := o.upper.Lstat(ctx, path); err == nil {
		return false, nil
	} else if !errors.Is(err, errno.NotFound) {
		return false, err
	}
	if _, err := o.lower.Lstat(ctx, path); err != nil {
		if errors.Is(err, errno.NotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func mustDir(path string) string {
	dir, _ := splitPath(path)
	return dir
}

func sortEntries(entries []stdfs.DirEntry) {
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
}
