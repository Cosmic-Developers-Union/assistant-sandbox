package mountfs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type mount struct {
	point string
	sys   vmfs.System
}

type Mounts struct {
	mounts []mount
}

func (m *Mounts) Mount(point string, system vmfs.System) error {
	if system == nil {
		return errno.Wrap(errno.Invalid, "挂载目标不能为空")
	}
	if !strings.HasPrefix(point, "/") {
		return errno.Wrap(errno.Invalid, "挂载点必须是客体绝对路径")
	}
	clean := path.Clean(point)
	for _, existing := range m.mounts {
		if existing.point == clean {
			return errno.Wrap(errno.Exist, "挂载点已存在: "+clean)
		}
	}

	if parent, ok := m.parentMount(clean); ok {
		if err := parent.sys.Mkdir(context.Background(), m.relative(parent, clean), 0o755); err != nil &&
			!errors.Is(err, errno.Exist) {
			return errno.Wrap(err, "在父挂载建立挂载点")
		}
	}
	m.mounts = append(m.mounts, mount{point: clean, sys: system})

	for i := 1; i < len(m.mounts); i++ {
		for j := i; j > 0 && len(m.mounts[j].point) > len(m.mounts[j-1].point); j-- {
			m.mounts[j], m.mounts[j-1] = m.mounts[j-1], m.mounts[j]
		}
	}
	return nil
}

func (m *Mounts) parentMount(point string) (mount, bool) {
	for candidate := path.Dir(point); ; candidate = path.Dir(candidate) {
		for _, entry := range m.mounts {
			if entry.point == candidate {
				return entry, true
			}
		}
		if candidate == "/" {
			return mount{}, false
		}
	}
}

func (m *Mounts) resolve(name string) (mount, error) {
	if !strings.HasPrefix(name, "/") {
		return mount{}, errno.Wrap(errno.Invalid, "路径必须是客体绝对路径")
	}
	clean := path.Clean(name)
	for _, entry := range m.mounts {
		if entry.point == "/" {
			return entry, nil
		}
		if clean == entry.point || strings.HasPrefix(clean, entry.point+"/") {
			return entry, nil
		}
	}
	return mount{}, errno.Wrap(errno.NotFound, "路径未挂载: "+clean)
}

func (m *Mounts) sameMount(oldPath, newPath string) (mount, error) {
	source, err := m.resolve(oldPath)
	if err != nil {
		return mount{}, err
	}
	target, err := m.resolve(newPath)
	if err != nil {
		return mount{}, err
	}
	if source.point != target.point {
		return mount{}, errno.Wrap(errno.CrossDevice,
			"跨挂载操作不被支持（"+source.point+" → "+target.point+"）")
	}
	return source, nil
}

func (m *Mounts) Open(ctx context.Context, name string, flags uint32, mode stdfs.FileMode) (vmfs.File, error) {
	entry, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	return entry.sys.Open(ctx, m.relative(entry, name), flags, mode)
}

func (m *Mounts) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	entry, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	return entry.sys.Stat(ctx, m.relative(entry, name))
}

func (m *Mounts) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	entry, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	return entry.sys.Lstat(ctx, m.relative(entry, name))
}

func (m *Mounts) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	entry, err := m.resolve(name)
	if err != nil {
		return nil, err
	}
	return entry.sys.ReadDir(ctx, m.relative(entry, name))
}

func (m *Mounts) Mkdir(ctx context.Context, name string, mode stdfs.FileMode) error {
	entry, err := m.resolve(name)
	if err != nil {
		return err
	}
	return entry.sys.Mkdir(ctx, m.relative(entry, name), mode)
}

func (m *Mounts) Remove(ctx context.Context, name string) error {
	entry, err := m.resolve(name)
	if err != nil {
		return err
	}
	return entry.sys.Remove(ctx, m.relative(entry, name))
}

func (m *Mounts) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := m.sameMount(oldPath, newPath)
	if err != nil {
		return err
	}
	return entry.sys.Rename(ctx, m.relative(entry, oldPath), m.relative(entry, newPath))
}

func (m *Mounts) Link(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := m.sameMount(oldPath, newPath)
	if err != nil {
		return err
	}
	return entry.sys.Link(ctx, m.relative(entry, oldPath), m.relative(entry, newPath))
}

// LinkFollowing 转发 linkat(2) AT_SYMLINK_FOLLOW 能力到挂载目标;
// 目标不支持时明确 ENOTSUP。
func (m *Mounts) LinkFollowing(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := m.sameMount(oldPath, newPath)
	if err != nil {
		return err
	}
	follow, ok := entry.sys.(vmfs.LinkFollow)
	if !ok {
		return errno.Wrap(errno.NotSupported, "挂载目标不支持跟随符号链接建链")
	}
	return follow.LinkFollowing(ctx, m.relative(entry, oldPath), m.relative(entry, newPath))
}

func (m *Mounts) Symlink(ctx context.Context, target, link string) error {
	entry, err := m.resolve(link)
	if err != nil {
		return err
	}
	return entry.sys.Symlink(ctx, target, m.relative(entry, link))
}

func (m *Mounts) Readlink(ctx context.Context, name string) (string, error) {
	entry, err := m.resolve(name)
	if err != nil {
		return "", err
	}
	return entry.sys.Readlink(ctx, m.relative(entry, name))
}

func (m *Mounts) relative(entry mount, name string) string {
	clean := path.Clean(name)
	if entry.point == "/" {
		return clean
	}
	rest := strings.TrimPrefix(clean, entry.point)
	if rest == "" {
		return "/"
	}
	return rest
}
