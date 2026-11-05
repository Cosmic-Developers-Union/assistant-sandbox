package vm

import (
	"context"
	"errors"
	"testing"

	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/executor/elf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/entropy"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/executable"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/identity"
)

// pidIdentity 把进程号映射为客体身份:pid 2 → uid 1000(属主),
// pid 3 → uid 2000(他人);测试矩阵的两个"用户"。
type pidIdentity struct{}

func (pidIdentity) Lookup(_ context.Context, pid uint64) (identity.Identity, error) {
	if err := ctxErr(pid); err != nil {
		return identity.Identity{}, err
	}
	switch pid {
	case 2:
		return identity.Identity{UID: 1000, GID: 1000, Name: "alice"}, nil
	case 3:
		return identity.Identity{UID: 2000, GID: 2000, Name: "bob"}, nil
	default:
		return identity.Identity{UID: 0, GID: 0, Name: "root"}, nil
	}
}

func ctxErr(pid uint64) error {
	if pid == 0 {
		return errno.NoProcess
	}
	return nil
}

// identityMatrixKernel 构造双身份内核:memfs 由 alice 建立属主文件,
// bob 以"他人"身份访问。
func identityMatrixKernel(t *testing.T) (*Kernel, vmfs.System) {
	t.Helper()
	system, err := memfs.New(&clock.Virtual{}, memfs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// 工作目录:根目录 0755 他人无写,先由基系统建 0o777 的 /work。
	if err := system.Mkdir(context.Background(), "/work", 0o777); err != nil {
		t.Fatal(err)
	}
	// 属主文件:alice 建 0o640 文件(bob 无读权)与 0o604 文件(bob 可读)。
	alice, err := system.WithAccessor(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]stdfs.FileMode{
		"/work/secret": 0o640,
		"/work/open":   0o604,
	} {
		file, err := alice.Open(context.Background(), path, vmfs.Create|vmfs.ReadWrite, mode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("data")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sticky, err := system.WithAccessor(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	// sticky 目录:stdfs.ModeSticky 位(01777 语义)。
	if err := sticky.Mkdir(context.Background(), "/work/tmpx", stdfs.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	if err := sticky.Mkdir(context.Background(), "/work/plain", 0o777); err != nil {
		t.Fatal(err)
	}

	registry, err := executable.New(map[string]executable.Program{})
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := New(Services{
		Set: capability.Set{
			FS:       system,
			FD:       fd.New(),
			Identity: pidIdentity{},
			Clock:    &clock.Virtual{},
			Entropy:  entropy.NewSeeded(1),
		},
		Programs: registry,
		Binaries: elf.Factory{},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	return kernel, system
}

// TestMultiIdentityPermissionMatrix 验证 I13:两个身份的允许/拒绝矩阵
// ——属主位/其他位分类、目录写与 sticky 约束、新建归属,全部由客体
// 文件系统按身份判定(不由宿主权限替代)。
func TestMultiIdentityPermissionMatrix(t *testing.T) {
	kernel, system := identityMatrixKernel(t)
	defer func() { _ = kernel.Close(context.Background()) }()

	alice, err := system.(vmfs.AccessorFS).WithAccessor(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := system.(vmfs.AccessorFS).WithAccessor(2000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// 属主可读写自己的 0o640;他人不可读不可写。
	if file, err := alice.Open(ctx, "/work/secret", vmfs.ReadWrite, 0); err != nil {
		t.Fatalf("属主读写 0o640: %v", err)
	} else {
		_ = file.Close()
	}
	if _, err := bob.Open(ctx, "/work/secret", vmfs.ReadWrite, 0); !errors.Is(err, errno.Permission) {
		t.Fatalf("他人读写 0o640: %v want EACCES", err)
	}
	if _, err := bob.Open(ctx, "/work/secret", vmfs.ReadOnly, 0); !errors.Is(err, errno.Permission) {
		t.Fatalf("他人读 0o640: %v want EACCES", err)
	}

	// 0o604:他人可读不可写。
	if file, err := bob.Open(ctx, "/work/open", vmfs.ReadOnly, 0); err != nil {
		t.Fatalf("他人读 0o604: %v", err)
	} else {
		_ = file.Close()
	}
	if _, err := bob.Open(ctx, "/work/open", vmfs.WriteOnly, 0); !errors.Is(err, errno.Permission) {
		t.Fatalf("他人写 0o604: %v want EACCES", err)
	}

	// 目录写:0o777 目录他人可建;新建归属他人身份。
	if file, err := bob.Open(ctx, "/work/plain/mine", vmfs.Create|vmfs.ReadWrite, 0o600); err != nil {
		t.Fatalf("他人建文件于 0o777 目录: %v", err)
	} else {
		_ = file.Close()
	}
	info, err := bob.Stat(ctx, "/work/plain/mine")
	if err != nil {
		t.Fatal(err)
	}
	if meta, ok := info.Sys().(vmfs.Metadata); ok {
		if meta.UID != 2000 || meta.GID != 2000 {
			t.Fatalf("新建归属=%d:%d want 2000:2000", meta.UID, meta.GID)
		}
	}

	// sticky 约束:/tmpx(01777,属主 alice)内 alice 的文件,bob 不可删。
	if file, err := alice.Open(ctx, "/work/tmpx/alices", vmfs.Create|vmfs.ReadWrite, 0o600); err != nil {
		t.Fatal(err)
	} else {
		_ = file.Close()
	}
	if err := bob.Remove(ctx, "/work/tmpx/alices"); !errors.Is(err, errno.Permission) {
		t.Fatalf("sticky 目录他人删除: %v want EACCES", err)
	}
	// 非 sticky 的 0o777 目录:他人可删。
	if err := bob.Remove(ctx, "/work/plain/mine"); err != nil {
		t.Fatalf("非 sticky 目录他人删除: %v", err)
	}

	// 遍历:无 x 位的目录他人不可进入(重建 0o600 目录验证)。
	if err := bob.Mkdir(ctx, "/work/dark", 0o600); err != nil {
		// bob 建 0o600 目录(本人可进);换 alice 尝试进入。
		_ = err
	}
	if err := alice.Mkdir(ctx, "/work/dark2", 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.Stat(ctx, "/work/dark2/none"); !errors.Is(err, errno.Permission) {
		t.Fatalf("无 x 位目录遍历: %v want EACCES", err)
	}
}
