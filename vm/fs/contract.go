package fs

import (
	"context"
	"io"
	stdfs "io/fs"
)

type Metadata struct {
	Inode uint64
	NLink uint64
	UID   uint32
	GID   uint32
	// AtimeNanos/MtimeNanos/CtimeNanos 是文件系统自行维护的纳秒时间戳;
	// 未单独维护 ctime 的实现以 mtime 充当。
	AtimeNanos int64
	MtimeNanos int64
	CtimeNanos int64
}

const ChownUnchanged int64 = -1

type Ownership interface {
	Chown(context.Context, string, int64, int64) error
}

type File interface {
	io.Reader
	io.Writer
	io.Seeker
	io.Closer
	Stat() (stdfs.FileInfo, error)
}

type System interface {
	Open(context.Context, string, uint32, stdfs.FileMode) (File, error)
	Stat(context.Context, string) (stdfs.FileInfo, error)

	Lstat(context.Context, string) (stdfs.FileInfo, error)
	ReadDir(context.Context, string) ([]stdfs.DirEntry, error)
	Mkdir(context.Context, string, stdfs.FileMode) error

	Remove(context.Context, string) error

	Rename(context.Context, string, string) error

	Link(context.Context, string, string) error
	Symlink(context.Context, string, string) error

	Readlink(context.Context, string) (string, error)
}

type Timestamps interface {
	SetTimes(context.Context, string, int64, int64) error
}

// LinkTimestamps 是 utimensat 所需的时间更新能力:
// follow=false 时不解析路径末端的符号链接(AT_SYMLINK_NOFOLLOW)。
type LinkTimestamps interface {
	SetTimesFollow(context.Context, string, int64, int64, bool) error
}

type AccessTimes interface {
	AccessTime(context.Context, string) (int64, error)
}

type Snapshotter interface {
	Snapshot(context.Context) (Snapshotted, error)
}

type Modes interface {
	Chmod(context.Context, string, stdfs.FileMode) error
}

func ModeBits(mode stdfs.FileMode) stdfs.FileMode {
	return mode & (stdfs.ModePerm | stdfs.ModeSetuid | stdfs.ModeSetgid | stdfs.ModeSticky)
}

type Snapshotted interface {
	Restore(context.Context, System) error
}

type Fifos interface {
	Mkfifo(context.Context, string, stdfs.FileMode) error
}

type Quota struct {
	Declared bool
	Total    uint64
	Used     uint64
	Free     uint64
}

type Space struct {
	Name, Root string

	Bytes  Quota
	Inodes Quota
}

type Capacity interface {
	Space(context.Context, string) (Space, error)

	Filesystems(context.Context) ([]Space, error)
}
