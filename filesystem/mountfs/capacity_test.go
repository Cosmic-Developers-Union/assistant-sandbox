package mountfs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/readonly"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func capacityOf(t *testing.T, mounts *Mounts) vmfs.Capacity {
	t.Helper()
	capacity, ok := any(mounts).(vmfs.Capacity)
	if !ok {
		t.Fatal("挂载表必须实现 vmfs.Capacity")
	}
	return capacity
}

func TestSpaceNamesTheMountPoint(t *testing.T) {
	inner := newFS(t)
	mounts := &Mounts{}
	if err := mounts.Mount("/a/b", inner); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mounts, "/a/b/one.txt", "1234")
	space, err := capacityOf(t, mounts).Space(context.Background(), "/a/b/one.txt")
	if err != nil {
		t.Fatal(err)
	}
	if space.Name != "/a/b" || space.Root != "/a/b" {
		t.Fatalf("文件系统身份应是挂载点: %+v", space)
	}

	if !space.Inodes.Declared || space.Inodes.Used != 2 {
		t.Fatalf("配额应来自后端且是真实计数: %+v", space.Inodes)
	}
}

func TestSpaceRejectsBackendWithoutCapacity(t *testing.T) {
	base, err := readonly.New(newFS(t))
	if err != nil {
		t.Fatal(err)
	}
	mounts := &Mounts{}
	if err := mounts.Mount("/", base); err != nil {
		t.Fatal(err)
	}
	if _, err := capacityOf(t, mounts).Space(context.Background(), "/"); !errors.Is(err, errno.NotSupported) {
		t.Fatalf("没有容量能力的后端应报 ENOTSUP: %v", err)
	}
}

func TestSpaceRejectsUnmountedPath(t *testing.T) {
	mounts := &Mounts{}
	if err := mounts.Mount("/a", newFS(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := capacityOf(t, mounts).Space(context.Background(), "/elsewhere"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未挂载路径应报 ENOENT: %v", err)
	}
}

func TestFilesystemsListsEveryMountIncludingWithoutCapacity(t *testing.T) {
	base, err := readonly.New(newFS(t))
	if err != nil {
		t.Fatal(err)
	}

	mounts := &Mounts{}
	if err := mounts.Mount("/", newFS(t)); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Mount("/a", base); err != nil {
		t.Fatal(err)
	}
	list, err := capacityOf(t, mounts).Filesystems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("两个挂载都要列出: %+v", list)
	}
	byRoot := map[string]vmfs.Space{}
	for _, entry := range list {
		byRoot[entry.Root] = entry
	}
	raw, ok := byRoot["/a"]
	if !ok || raw.Bytes.Declared || raw.Inodes.Declared {
		t.Fatalf("没有容量能力的挂载必须保留并标记未声明: %+v", raw)
	}
	inner, ok := byRoot["/"]
	if !ok || !inner.Inodes.Declared {
		t.Fatalf("有容量能力的挂载必须报出真实配额: %+v", inner)
	}
}

func TestFilesystemsHonorsCancel(t *testing.T) {
	mounts := &Mounts{}
	if err := mounts.Mount("/", newFS(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := capacityOf(t, mounts).Filesystems(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应上报: %v", err)
	}
}

// aggregateFS 模拟 /dev:自身没有容量语义,但内部可写子目录有。
type aggregateFS struct{ *memfs.FS }

func (aggregateFS) Space(context.Context, string) (vmfs.Space, error) {
	return vmfs.Space{}, errno.Wrap(errno.NotSupported, "聚合文件系统没有自身容量")
}

func (aggregateFS) Filesystems(ctx context.Context) ([]vmfs.Space, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []vmfs.Space{{
		Name: "/shm", Root: "/shm",
		Bytes:  vmfs.Quota{Declared: true, Total: 4096, Used: 12, Free: 4084},
		Inodes: vmfs.Quota{Declared: true, Total: 16, Used: 1, Free: 15},
	}}, nil
}

// TestFilesystemsExpandsAggregateMounts 验证挂载点自身不报告容量时,
// 仍展开其可写子目录(/dev/shm),而不是让整个存储快照失败。
func TestFilesystemsExpandsAggregateMounts(t *testing.T) {
	mounts := &Mounts{}
	if err := mounts.Mount("/dev", aggregateFS{newFS(t)}); err != nil {
		t.Fatal(err)
	}
	list, err := capacityOf(t, mounts).Filesystems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "/dev/shm" || list[0].Root != "/dev/shm" {
		t.Fatalf("聚合挂载应展开子目录: %+v", list)
	}
	if !list[0].Bytes.Declared || list[0].Bytes.Used != 12 {
		t.Fatalf("子目录容量应保留: %+v", list[0])
	}
}

func TestMkfifoDispatchesToOwningMount(t *testing.T) {
	ctx := context.Background()
	inner := newFS(t)
	mounts := &Mounts{}
	if err := mounts.Mount("/a", inner); err != nil {
		t.Fatal(err)
	}
	if err := mounts.Mkfifo(ctx, "/a/pipe", 0o640); err != nil {
		t.Fatal(err)
	}
	info, err := inner.Lstat(ctx, "/pipe")
	if err != nil || info.Mode()&stdfs.ModeNamedPipe == 0 || info.Mode().Perm() != 0o640 {
		t.Fatalf("应落在内层挂载的相对路径上: %v %v", info.Mode(), err)
	}
	if err := mounts.Mkfifo(ctx, "/a/pipe", 0o644); !errors.Is(err, errno.Exist) {
		t.Fatalf("同名应报 EEXIST: %v", err)
	}
	if err := mounts.Mkfifo(ctx, "/elsewhere/pipe", 0o644); !errors.Is(err, errno.NotFound) {
		t.Fatalf("未挂载路径应报 ENOENT: %v", err)
	}
}
