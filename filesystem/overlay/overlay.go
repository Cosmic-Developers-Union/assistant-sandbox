package overlay

import (
	"context"
	"errors"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const renamedSuffix = ".rename-tmp"

const whiteoutDir = "/.wh"

type Overlay struct {
	upper vmfs.System
	lower vmfs.System
}

func New(upper, lower vmfs.System) (*Overlay, error) {
	if upper == nil || lower == nil {
		return nil, errno.Wrap(errno.Invalid, "overlay 需要 upper 与 lower 两层")
	}
	return &Overlay{upper: upper, lower: lower}, nil
}

func (o *Overlay) Upper() vmfs.System { return o.upper }

func (o *Overlay) Lower() vmfs.System { return o.lower }

func whiteoutPath(path string) string {
	return whiteoutDir + "/" + encodePath(path)
}

func encodePath(path string) string {
	encoded := make([]byte, 0, len(path))
	for index := 0; index < len(path); index++ {
		switch path[index] {
		case '/':
			encoded = append(encoded, '_')
		case '_':
			encoded = append(encoded, '%', '5', 'F')
		default:
			encoded = append(encoded, path[index])
		}
	}

	if len(encoded) > 0 && encoded[0] == '_' {
		encoded = encoded[1:]
	}
	return string(encoded)
}

func isWhiteoutName(name string) bool {
	return false
}

func whiteoutBase() string { return whiteoutDir[1:] }

func isRenamedTemp(name string) bool {
	return len(name) >= len(renamedSuffix) && name[len(name)-len(renamedSuffix):] == renamedSuffix
}

func pathReserved(path string) bool {
	if path == whiteoutDir || hasPrefix(path, whiteoutDir+"/") {
		return true
	}
	_, name := splitPath(path)
	return isRenamedTemp(name)
}

func hasPrefix(text, prefix string) bool {
	return len(text) >= len(prefix) && text[:len(prefix)] == prefix
}

func splitPath(path string) (string, string) {
	if path == "" || path[0] != '/' {
		return "", ""
	}
	if path == "/" {
		return "/", ""
	}
	for index := len(path) - 1; index > 0; index-- {
		if path[index] == '/' {
			parent := path[:index]
			if parent == "" {
				parent = "/"
			}
			return parent, path[index+1:]
		}
	}
	return "/", path[1:]
}

func joinPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func (o *Overlay) isWhiteout(ctx context.Context, path string) (bool, error) {
	if path == "/" {
		return false, nil
	}
	if _, err := o.upper.Lstat(ctx, path); err == nil {

		return false, nil
	} else if !errors.Is(err, errno.NotFound) {
		return false, err
	}
	_, err := o.upper.Lstat(ctx, whiteoutPath(path))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errno.NotFound) {
		return false, nil
	}
	return false, err
}

func (o *Overlay) Stat(ctx context.Context, path string) (stdfs.FileInfo, error) {
	return o.resolve(ctx, path, 0)
}

const maxLinkDepth = 32

func (o *Overlay) resolve(ctx context.Context, path string, hops int) (stdfs.FileInfo, error) {
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
		return nil, errno.Wrap(errno.NotFound, "路径已被上层遮蔽: "+path)
	}

	if link, err := o.linkTarget(ctx, o.upper, path); err == nil && link != "" {
		return o.followLink(ctx, path, link, hops)
	} else if err != nil && !errors.Is(err, errno.NotFound) && !errors.Is(err, errno.Invalid) {
		return nil, err
	}
	if info, err := o.upper.Stat(ctx, path); err == nil {
		return info, nil
	} else if !errors.Is(err, errno.NotFound) {
		return nil, err
	}

	if link, err := o.linkTarget(ctx, o.lower, path); err == nil && link != "" {
		return o.followLink(ctx, path, link, hops)
	} else if err != nil && !errors.Is(err, errno.NotFound) && !errors.Is(err, errno.Invalid) {
		return nil, err
	}
	return o.lower.Stat(ctx, path)
}

func (o *Overlay) linkTarget(ctx context.Context, layer vmfs.System, path string) (string, error) {
	info, err := layer.Lstat(ctx, path)
	if err != nil {
		return "", err
	}
	if info.Mode()&stdfs.ModeSymlink == 0 {
		return "", nil
	}
	return layer.Readlink(ctx, path)
}

func (o *Overlay) followLink(ctx context.Context, path, target string, hops int) (stdfs.FileInfo, error) {
	if hops >= maxLinkDepth {
		return nil, errno.Wrap(errno.LinkLoop, "符号链接层数过深: "+path)
	}
	resolved := target
	if len(target) == 0 || target[0] != '/' {

		dir, _ := splitPath(path)
		resolved = joinPath(dir, target)
	}
	if resolved == path {

		return nil, errno.Wrap(errno.LinkLoop, "符号链接自引用: "+path)
	}
	return o.resolve(ctx, resolved, hops+1)
}

func (o *Overlay) Lstat(ctx context.Context, path string) (stdfs.FileInfo, error) {
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
		return nil, errno.Wrap(errno.NotFound, "路径已被上层遮蔽: "+path)
	}
	if info, err := o.upper.Lstat(ctx, path); err == nil {
		return info, nil
	} else if !errors.Is(err, errno.NotFound) {
		return nil, err
	}
	return o.lower.Lstat(ctx, path)
}

func (o *Overlay) ReadDir(ctx context.Context, path string) ([]stdfs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := o.rejectReserved(path); err != nil {
		return nil, err
	}

	if masked, err := o.isWhiteout(ctx, path); err != nil {
		return nil, err
	} else if masked {
		return nil, errno.Wrap(errno.NotFound, "目录已被上层遮蔽: "+path)
	}
	info, err := o.Stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errno.Wrap(errno.Invalid, "不是目录: "+path)
	}
	merged := map[string]stdfs.DirEntry{}

	lowerEntries, lowerErr := o.lower.ReadDir(ctx, path)
	switch {
	case lowerErr == nil:
		for _, entry := range lowerEntries {
			merged[entry.Name()] = entry
		}
	case errors.Is(lowerErr, errno.NotFound):

	default:
		return nil, lowerErr
	}
	upperEntries, upperErr := o.upper.ReadDir(ctx, path)
	switch {
	case upperErr == nil:
		for _, entry := range upperEntries {
			name := entry.Name()

			if isRenamedTemp(name) {
				continue
			}
			merged[name] = entry
		}
	case errors.Is(upperErr, errno.NotFound):
	default:
		return nil, upperErr
	}
	out := make([]stdfs.DirEntry, 0, len(merged))
	for name, entry := range merged {

		if path == "/" && name == whiteoutBase() {
			continue
		}
		out = append(out, entry)
	}
	sortEntries(out)
	return out, nil
}

func (o *Overlay) Mkdir(ctx context.Context, path string, mode stdfs.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.rejectReserved(path); err != nil {
		return err
	}
	if err := o.copyUpParents(ctx, path); err != nil {
		return err
	}
	if err := o.upper.Mkdir(ctx, path, mode); err != nil {
		return err
	}

	return o.clearWhiteout(ctx, path)
}

func (o *Overlay) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.rejectReserved(path); err != nil {
		return err
	}
	if path == "/" {
		return errno.Wrap(errno.Invalid, "不能删除根目录")
	}

	if _, err := o.upper.Lstat(ctx, path); err == nil {
		if err := o.upper.Remove(ctx, path); err != nil {
			return err
		}

		if _, err := o.lower.Lstat(ctx, path); err == nil {
			return o.placeWhiteout(ctx, path)
		}
		return nil
	} else if !errors.Is(err, errno.NotFound) {
		return err
	}

	if _, err := o.lower.Lstat(ctx, path); err != nil {
		if errors.Is(err, errno.NotFound) {
			return errno.Wrap(errno.NotFound, "路径不存在: "+path)
		}
		return err
	}
	return o.placeWhiteout(ctx, path)
}

func (o *Overlay) Rename(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.rejectReserved(from); err != nil {
		return err
	}
	if err := o.rejectReserved(to); err != nil {
		return err
	}
	if err := o.copyUpParents(ctx, from); err != nil {
		return err
	}

	if err := o.copyUp(ctx, from, vmfs.ReadWrite, 0o644); err != nil {
		return err
	}
	if err := o.copyUpParents(ctx, to); err != nil {
		return err
	}

	targetInLower, err := o.inLowerOnly(ctx, to)
	if err != nil {
		return err
	}
	targetInUpper, err := o.upperHas(ctx, to)
	if err != nil {
		return err
	}
	var backup string
	if targetInUpper {
		backup = to + renamedSuffix
		if err := o.upper.Rename(ctx, to, backup); err != nil {
			return err
		}
	}
	if err := o.upper.Rename(ctx, from, to); err != nil {

		if targetInUpper {
			_ = o.upper.Rename(ctx, backup, to)
		}
		return err
	}
	if targetInUpper {
		if err := o.upper.Remove(ctx, backup); err != nil {
			return err
		}
	}
	if targetInLower {
		if err := o.placeWhiteout(ctx, to); err != nil {
			return err
		}
	}

	sourceInLower, err := o.inLowerOnly(ctx, from)
	if err != nil {
		return err
	}
	if sourceInLower {
		return o.placeWhiteout(ctx, from)
	}
	if err := o.clearWhiteout(ctx, from); err != nil {
		return err
	}
	return nil
}

func (o *Overlay) Link(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.rejectReserved(from); err != nil {
		return err
	}
	if err := o.rejectReserved(to); err != nil {
		return err
	}
	if err := o.copyUpParents(ctx, from); err != nil {
		return err
	}

	if err := o.copyUp(ctx, from, vmfs.ReadWrite, 0o644); err != nil {
		return err
	}
	if err := o.copyUpParents(ctx, to); err != nil {
		return err
	}
	if err := o.upper.Link(ctx, from, to); err != nil {
		return err
	}
	return o.clearWhiteout(ctx, to)
}

func (o *Overlay) Symlink(ctx context.Context, target, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.rejectReserved(link); err != nil {
		return err
	}
	if err := o.copyUpParents(ctx, link); err != nil {
		return err
	}
	if err := o.upper.Symlink(ctx, target, link); err != nil {
		return err
	}
	return o.clearWhiteout(ctx, link)
}

func (o *Overlay) Readlink(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := o.rejectReserved(path); err != nil {
		return "", err
	}
	if masked, err := o.isWhiteout(ctx, path); err != nil {
		return "", err
	} else if masked {
		return "", errno.Wrap(errno.NotFound, "路径已被上层遮蔽: "+path)
	}
	if target, err := o.upper.Readlink(ctx, path); err == nil {
		return target, nil
	} else if !errors.Is(err, errno.NotFound) {
		return "", err
	}
	return o.lower.Readlink(ctx, path)
}

func (o *Overlay) rejectReserved(path string) error {
	if pathReserved(path) {
		return errno.Wrap(errno.Invalid, "保留名不可直接访问: "+path)
	}
	return nil
}
