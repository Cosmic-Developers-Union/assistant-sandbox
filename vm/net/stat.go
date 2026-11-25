package net

import (
	stdfs "io/fs"
	"time"
)

// socketFileInfo 是 fstat(2) 对套接字的只读视图:Linux 报告
// S_IFSOCK 与 0777 权限,大小为零,时间字段不参与套接字判定。
type socketFileInfo struct{ name string }

// SocketStat 返回套接字的只读 fstat 视图,供非 vm/net 端点复用。
func SocketStat(name string) stdfs.FileInfo { return socketFileInfo{name: name} }

func (i socketFileInfo) Name() string         { return i.name }
func (i socketFileInfo) Size() int64          { return 0 }
func (i socketFileInfo) Mode() stdfs.FileMode { return stdfs.ModeSocket | 0o777 }
func (i socketFileInfo) ModTime() time.Time   { return time.Time{} }
func (i socketFileInfo) IsDir() bool          { return false }
func (i socketFileInfo) Sys() any             { return nil }
