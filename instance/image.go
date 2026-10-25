package instance

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// Image 是一份可复用的 VM 镜像:根文件系统的只读模板。镜像与 VM
// 实例、连接句柄三者分离(C10)——同一份镜像可以创建多台互相隔离的
// VM;实例上的写入不改变镜像,断开连接也不改变镜像。
type Image struct {
	snapshot vmfs.Snapshotted
	digest   string
	encoded  []byte
}

// NewImage 建立一份默认镜像:根文件系统含 /work 工作区与 /tmp 临时
// 目录,可被任意多台 VM 复用。
func NewImage(ctx context.Context) (*Image, error) {
	return NewImageFrom(ctx, nil)
}

// NewImageFrom 以给定的根文件系统为模板建立镜像;root 为空时建立
// 默认布局(/work 与 /tmp)。
func NewImageFrom(ctx context.Context, root *memfs.FS) (*Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == nil {
		created, err := memfs.New(&clock.Virtual{}, memfs.Options{})
		if err != nil {
			return nil, err
		}
		for _, dir := range []string{WorkRoot, TempRoot} {
			if err := created.Mkdir(ctx, dir, 0o755); err != nil {
				return nil, err
			}
		}
		root = created
	}
	snapshot, err := root.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	encoder, ok := snapshot.(interface {
		Encode(context.Context) ([]byte, error)
	})
	if !ok {
		return nil, errno.Wrap(errno.NotSupported, "根文件系统快照不支持编码")
	}
	encoded, err := encoder.Encode(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	return &Image{snapshot: snapshot, digest: fmt.Sprintf("%x", sum), encoded: encoded}, nil
}

// Digest 返回镜像内容的摘要:镜像不可变,摘要恒定。
func (i *Image) Digest() string { return i.digest }

// Bytes 返回镜像的编码内容。
func (i *Image) Bytes() []byte { return i.encoded }

// Instantiate 用镜像创建一台独立 VM:根文件系统是镜像的副本,两台
// VM 的写入互不影响,镜像本身保持不变(C10)。
func (i *Image) Instantiate(ctx context.Context, opts Options) (*VM, error) {
	if i == nil || i.snapshot == nil {
		return nil, errno.Wrap(errno.Invalid, "镜像未初始化")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source := clockFor(opts)
	root, err := memfs.New(source, memfs.Options{})
	if err != nil {
		return nil, err
	}
	if err := i.snapshot.Restore(ctx, root); err != nil {
		return nil, err
	}
	return newVM(ctx, opts, root, source)
}
