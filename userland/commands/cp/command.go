package cp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func Run(ctx context.Context, args []string) error {
	f := commandio.Flags(ctx, "cp")
	recursive := false
	f.BoolVarP(&recursive, "recursive", "r", false, "递归复制")
	f.BoolVar(&recursive, "R", false, "递归复制")
	preserve := false
	f.BoolVarP(&preserve, "preserve", "p", false, "保留模式与时间戳")
	if err := commandio.Parse(f, args); err != nil {
		return err
	}
	a := f.Args()
	if len(a) < 2 {
		return fmt.Errorf("cp 需要源和目标")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	h := commandio.Directory(ctx)
	target := commandio.Path(h, a[len(a)-1])
	info, targetErr := system.Stat(ctx, target)
	if len(a) > 2 && (targetErr != nil || !info.IsDir()) {
		return fmt.Errorf("cp 多个源需要目标目录")
	}
	for _, source := range a[:len(a)-1] {
		from := commandio.Path(h, source)
		to := target
		if targetErr == nil && info.IsDir() {
			to = path.Join(target, path.Base(from))
		}
		if err := copyFile(ctx, system, from, to, recursive, preserve); err != nil {
			return err
		}
	}
	return nil
}
func copyFile(ctx context.Context, system vmfs.System, from, to string, recursive, preserve bool) error {
	remaining := 4096
	return copyNode(ctx, system, from, to, recursive, preserve, &remaining, 0)
}

type sourceAttributes struct {
	mode  fs.FileMode
	atime int64
	mtime int64
}

func readAttributes(ctx context.Context, system vmfs.System, from string, info fs.FileInfo) sourceAttributes {
	attributes := sourceAttributes{mode: info.Mode(), atime: info.ModTime().UnixNano(), mtime: info.ModTime().UnixNano()}
	if access, ok := system.(vmfs.AccessTimes); ok {
		if atime, err := access.AccessTime(ctx, from); err == nil {
			attributes.atime = atime
		}
	}
	return attributes
}

func applyAttributes(ctx context.Context, system vmfs.System, to string, attributes sourceAttributes) error {
	if modes, ok := system.(vmfs.Modes); ok {
		if err := modes.Chmod(ctx, to, attributes.mode); err != nil && !errors.Is(err, errno.NotSupported) {
			return err
		}
	}
	if timestamps, ok := system.(vmfs.Timestamps); ok {
		if err := timestamps.SetTimes(ctx, to, attributes.atime, attributes.mtime); err != nil && !errors.Is(err, errno.NotSupported) {
			return err
		}
	}
	return nil
}
func copyNode(ctx context.Context, system vmfs.System, from, to string, recursive, preserve bool, remaining *int, depth int) error {
	if *remaining <= 0 || depth > 128 {
		return fmt.Errorf("cp 超过 VM 递归预算")
	}
	*remaining--
	if err := ctx.Err(); err != nil {
		return err
	}
	if from == to {
		return fmt.Errorf("cp 源和目标相同")
	}
	info, err := system.Stat(ctx, from)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if !recursive {
			return fmt.Errorf("cp 目录需要 -r")
		}
		if to == from || len(to) > len(from) && to[:len(from)+1] == from+"/" {
			return fmt.Errorf("cp 不能复制到源目录内部")
		}

		attributes := readAttributes(ctx, system, from, info)
		if _, err := system.Stat(ctx, to); err != nil {
			if err := system.Mkdir(ctx, to, info.Mode().Perm()); err != nil {
				return err
			}
		}
		entries, err := system.ReadDir(ctx, from)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyNode(ctx, system, path.Join(from, e.Name()), path.Join(to, e.Name()), recursive, preserve, remaining, depth+1); err != nil {
				return err
			}
		}
		if preserve {
			return applyAttributes(ctx, system, to, attributes)
		}
		return nil
	}

	attributes := readAttributes(ctx, system, from, info)
	input, err := system.Open(ctx, from, vmfs.ReadOnly, 0)
	if err != nil {
		return err
	}
	output, err := system.Open(ctx, to, vmfs.Create|vmfs.WriteOnly|vmfs.Truncate, info.Mode().Perm())
	if err != nil {
		return errors.Join(err, input.Close())
	}
	_, err = io.Copy(output, input)
	inputCloseErr := input.Close()
	closed := output.Close()
	if closeErr := errors.Join(err, inputCloseErr, closed); closeErr != nil {
		return closeErr
	}
	if preserve {
		return applyAttributes(ctx, system, to, attributes)
	}
	return nil
}
