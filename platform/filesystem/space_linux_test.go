//go:build !wasip1 && linux

package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func vmfsQuota(total, used, free uint64) vmfs.Quota {
	return vmfs.Quota{Declared: true, Total: total, Used: used, Free: free}
}

func TestSpaceReportsHostFilesystemFacts(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	store := NewFilesystem(root)
	space, err := store.Space(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if space.Root != root || space.Name != root {
		t.Fatalf("这个后端的身份就是它的授权根: %+v", space)
	}
	if !space.Bytes.Declared {
		t.Fatal("宿主适配层必须报告字节容量")
	}
	if space.Bytes.Total == 0 {
		t.Fatalf("宿主容量不应为零: %+v", space.Bytes)
	}

	if sum := space.Bytes.Used + space.Bytes.Free; sum > space.Bytes.Total {
		t.Fatalf("已用 + 可用超过总量: %+v", space.Bytes)
	}

	list, err := store.Filesystems(context.Background())
	if err != nil || len(list) != 1 || list[0].Root != space.Root || !list[0].Bytes.Declared {
		t.Fatalf("单棵授权子树应报它自己那一项: %+v %v", list, err)
	}
}

func TestSpaceRejectsMissingPathAndCancel(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	store := NewFilesystem(root)
	missing := filepath.ToSlash(filepath.Join(root, "nope"))
	if _, err := store.Space(context.Background(), missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不存在的路径应报 ENOENT: %v", err)
	}

	if _, err := store.Space(context.Background(), "/etc"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("授权根之外应报 EPERM: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Space(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
	if _, err := store.Filesystems(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
}

func TestHostSpaceKeepsReservedBlocksAndInodeGaps(t *testing.T) {
	space, err := hostSpace(syscall.Statfs_t{
		Bsize: 4096, Blocks: 100, Bfree: 30, Bavail: 20, Files: 50, Ffree: 10,
	}, "/grant")
	if err != nil {
		t.Fatal(err)
	}
	want := vmfsQuota(100*4096, 70*4096, 20*4096)
	if space.Bytes != want {
		t.Fatalf("字节口径 = %+v，期望 %+v", space.Bytes, want)
	}

	if reserved := space.Bytes.Total - space.Bytes.Used - space.Bytes.Free; reserved != 10*4096 {
		t.Fatalf("保留量 = %d", reserved)
	}
	if space.Inodes != vmfsQuota(50, 40, 10) {
		t.Fatalf("inode 口径 = %+v", space.Inodes)
	}

	noGap, err := hostSpace(syscall.Statfs_t{Bsize: 512, Blocks: 4, Bfree: 4, Bavail: 4}, "/grant")
	if err != nil {
		t.Fatal(err)
	}
	if noGap.Inodes.Declared {
		t.Fatalf("没有 inode 上限时不得报一个数字: %+v", noGap.Inodes)
	}

	odd, err := hostSpace(syscall.Statfs_t{Bsize: 512, Blocks: 4, Bfree: 9, Bavail: 9}, "/grant")
	if err != nil {
		t.Fatal(err)
	}
	if odd.Bytes.Used != 0 {
		t.Fatalf("不可信的读数不得回绕: %+v", odd.Bytes)
	}

	if _, err := hostSpace(syscall.Statfs_t{}, "/grant"); err == nil {
		t.Fatal("块大小无效必须报错")
	}
}

func TestMkfifoCreatesHostNamedPipe(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	store := NewFilesystem(root)
	name := filepath.ToSlash(filepath.Join(root, "pipe"))
	if err := store.Mkfifo(context.Background(), name, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(name)
	if err != nil || info.Mode()&fs.ModeNamedPipe == 0 {
		t.Fatalf("宿主节点类型: %v %v", info.Mode(), err)
	}

	if info.Mode().Perm()&^fs.FileMode(0o600) != 0 {
		t.Fatalf("权限不应宽于请求值: %v", info.Mode())
	}
	if err := store.Mkfifo(context.Background(), "/etc/pipe", 0o600); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("授权根之外应报 EPERM: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Mkfifo(ctx, name, 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
}
