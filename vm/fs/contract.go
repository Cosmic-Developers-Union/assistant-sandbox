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

// DescriptorOwner 是按打开描述符更新属主的能力。fchown 与
// fchownat(AT_EMPTY_PATH)用于已经解链的临时文件,不能退化为路径 chown。
type DescriptorOwner interface {
	ChownDescriptor(uid, gid int64) error
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

	// Link 按 link(2)/linkat(2) 默认语义:oldpath 为符号链接时
	// 不解引用,硬链接指向链接本身(AT_SYMLINK_FOLLOW 见 LinkFollow)。
	Link(context.Context, string, string) error
	Symlink(context.Context, string, string) error

	Readlink(context.Context, string) (string, error)
}

// Lockable 由可参与字节范围文件锁的句柄实现(I14):返回锁对象标识
// (通常为 inode 号)——fcntl 锁按此键控,进程退出统一释放。
type Lockable interface {
	LockIdentity() uint64
}

// AccessorFS 由支持多身份检查的文件系统实现(I13/O01):WithAccessor
// 返回以 (uid,gid) 身份执行后续操作的视图——权限按 owner/group/other
// 分类判定,新建节点归属该身份;视图共享同一棵树,并发安全。
type AccessorFS interface {
	WithAccessor(uid, gid uint32) (System, error)
}

// AccessorFuncFS 是动态身份视图(O01):每次权限判定与新节点归属都
// 调用 owner 读取当前 UID/GID,使 setuid/setgid 立即生效。
type AccessorFuncFS interface {
	WithAccessorFunc(owner func() (uid, gid uint32)) (System, error)
}

// LinkFollow 是 linkat(2) AT_SYMLINK_FOLLOW 的可选能力:
// oldpath 为符号链接时解引用,对目标文件建立硬链接。
type LinkFollow interface {
	LinkFollowing(context.Context, string, string) error
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

// DirectoryResolver 是目录句柄的可选能力(I02):把相对路径解析为
// 以句柄目录为基准的客体绝对路径。实现必须跟随目录 inode:目录被
// 重命名后,解析结果反映新位置,与 Linux 目录描述符语义一致。
type DirectoryResolver interface {
	// ResolveGuestPath 解析相对路径;name 为绝对路径时原样返回。
	// 非目录句柄或不支持解析时报错(ENOTDIR/ENOTSUP)。
	ResolveGuestPath(name string) (string, error)
}
