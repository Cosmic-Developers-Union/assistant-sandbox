package vfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const (
	O_RDONLY = vmfs.ReadOnly
	O_WRONLY = vmfs.WriteOnly
	O_RDWR   = vmfs.ReadWrite
	O_APPEND = vmfs.Append
	O_CREATE = vmfs.Create
	O_EXCL   = vmfs.Exclusive
	O_TRUNC  = vmfs.Truncate
)

const ModePerm = fs.ModePerm

var (
	ErrNotExist   = errno.NotFound
	ErrExist      = errno.Exist
	ErrPermission = errno.Permission
)

func NotExist(err error) bool {
	return errors.Is(err, ErrNotExist) || errors.Is(err, fs.ErrNotExist)
}

func Exist(err error) bool {
	return errors.Is(err, ErrExist) || errors.Is(err, fs.ErrExist)
}

type sameFileReporter interface {
	SameFile(a, b fs.FileInfo) bool
}

func SameFile(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	if left, lok := a.Sys().(*vmfs.Metadata); lok {
		if right, rok := b.Sys().(*vmfs.Metadata); rok {
			return left.Inode == right.Inode
		}
	}
	if system, _, err := Sys(); err == nil {
		if reporter, ok := system.(sameFileReporter); ok {
			return reporter.SameFile(a, b)
		}
	}
	return false
}

type binding struct {
	system vmfs.System
	dir    string
	env    map[string]string
}

var current *binding

func Bind(system vmfs.System, dir string, env map[string]string) func() {
	previous := current
	current = &binding{system: system, dir: dir, env: env}
	return func() { current = previous }
}

func Sys() (vmfs.System, string, error) {
	if current == nil || current.system == nil {
		return nil, "", errno.Wrap(errno.NotImplemented, "vfs 未绑定客体文件能力")
	}
	return current.system, current.dir, nil
}

func Path(name string) string {
	system, dir, err := Sys()
	if err != nil {
		return name
	}
	_ = system
	if path.IsAbs(name) {
		return name
	}
	return path.Join(dir, name)
}

func Getwd() (string, error) {
	_, dir, err := Sys()
	return dir, err
}

func Getenv(name string) string {
	if current == nil {
		return ""
	}
	return current.env[name]
}

func Environ() []string {
	if current == nil {
		return nil
	}
	environment := make([]string, 0, len(current.env))
	for name, value := range current.env {
		environment = append(environment, name+"="+value)
	}
	return environment
}

func Open(name string) (vmfs.File, error) {
	return OpenFile(name, int(O_RDONLY), 0)
}

func OpenFile(name string, flag int, perm fs.FileMode) (vmfs.File, error) {
	system, _, err := Sys()
	if err != nil {
		return nil, err
	}
	if flag < 0 || uint64(flag) > uint64(^uint32(0)) {
		return nil, errno.Wrap(errno.Invalid, "文件打开标志超出客体范围")
	}
	return system.Open(context.Background(), Path(name), uint32(flag), perm)
}

func Stat(name string) (fs.FileInfo, error) {
	system, _, err := Sys()
	if err != nil {
		return nil, err
	}
	return system.Stat(context.Background(), Path(name))
}

func Lstat(name string) (fs.FileInfo, error) {
	system, _, err := Sys()
	if err != nil {
		return nil, err
	}
	return system.Lstat(context.Background(), Path(name))
}

func ReadDir(name string) ([]fs.DirEntry, error) {
	system, _, err := Sys()
	if err != nil {
		return nil, err
	}
	return system.ReadDir(context.Background(), Path(name))
}

func ReadFile(name string) (data []byte, err error) {
	file, err := Open(name)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	data, err = io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 4<<20 {
		return nil, errno.Wrap(errno.Range, "文件超过 4 MiB 读取预算")
	}
	return data, nil
}

func WriteFile(name string, data []byte, perm fs.FileMode) error {
	file, err := OpenFile(name, int(O_WRONLY|O_CREATE|O_TRUNC), perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func Create(name string) (vmfs.File, error) {
	return OpenFile(name, int(O_WRONLY|O_CREATE|O_TRUNC), 0o666)
}

func Remove(name string) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	return system.Remove(context.Background(), Path(name))
}

func RemoveAll(name string) error {
	info, err := Lstat(name)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return Remove(name)
	}
	entries, err := ReadDir(name)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := RemoveAll(path.Join(Path(name), entry.Name())); err != nil {
			return err
		}
	}
	return Remove(name)
}

func Mkdir(name string, perm fs.FileMode) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	return system.Mkdir(context.Background(), Path(name), perm)
}

func MkdirAll(name string, perm fs.FileMode) error {
	if _, err := Stat(name); err == nil {
		return nil
	}
	parent := path.Dir(Path(name))
	if parent != "/" && parent != "." && parent != Path(name) {
		if err := MkdirAll(parent, perm); err != nil {
			return err
		}
	}
	if err := Mkdir(name, perm); err != nil {
		if _, statErr := Stat(name); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func Rename(oldName, newName string) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	return system.Rename(context.Background(), Path(oldName), Path(newName))
}

func Link(oldName, newName string) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	return system.Link(context.Background(), Path(oldName), Path(newName))
}

func Symlink(target, linkName string) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	return system.Symlink(context.Background(), target, Path(linkName))
}

func Readlink(name string) (string, error) {
	system, _, err := Sys()
	if err != nil {
		return "", err
	}
	return system.Readlink(context.Background(), Path(name))
}

func Chmod(name string, mode fs.FileMode) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	modes, ok := system.(vmfs.Modes)
	if !ok {
		return errno.Wrap(errno.NotSupported, "客体文件系统不支持 chmod")
	}
	return modes.Chmod(context.Background(), Path(name), mode)
}

func Chtimes(name string, atime, mtime int64) error {
	system, _, err := Sys()
	if err != nil {
		return err
	}
	timestamps, ok := system.(vmfs.Timestamps)
	if !ok {
		return errno.Wrap(errno.NotSupported, "客体文件系统不支持 chtimes")
	}
	return timestamps.SetTimes(context.Background(), Path(name), atime, mtime)
}
