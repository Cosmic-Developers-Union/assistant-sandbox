package image

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestStoreRejectsDamagedUpdateAndKeepsActive 验证 N12:损坏/不兼容的
// 更新不会替换可用镜像,执行前复核仍通过。
func TestStoreRejectsDamagedUpdateAndKeepsActive(t *testing.T) {
	ctx := context.Background()
	current := manifestFixture(t)
	requirements := Requirements{Version: ManifestVersion, Architecture: ArchitectureRV64, ABI: ABILinuxRV64, Entry: "/bin/python3"}
	store, err := NewStore(ctx, current, requirements)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := current.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	damaged := &digestStub{manifest: manifest, digests: map[string]string{
		"/bin/python3": manifest.Files[0].Digest,
		"/lib/libc.so": ContentDigest([]byte("damaged")),
	}}
	if err := store.Update(ctx, damaged, requirements); !errors.Is(err, errno.Version) {
		t.Fatalf("损坏更新 err=%v want EPROTO", err)
	}
	if store.Active() != current {
		t.Fatal("失败更新替换了可用镜像")
	}
	if err := store.VerifyActive(ctx, requirements); err != nil {
		t.Fatalf("失败更新后活动镜像不可用: %v", err)
	}
	// 架构不兼容的更新同样拒绝。
	wrongArch := &digestStub{manifest: manifest, digests: map[string]string{
		"/bin/python3": manifest.Files[0].Digest,
		"/lib/libc.so": manifest.Files[1].Digest,
	}}
	wrongArch.manifest.Architecture = "amd64"
	if err := store.Update(ctx, wrongArch, requirements); !errors.Is(err, errno.Version) {
		t.Fatalf("错误架构 err=%v", err)
	}
	if store.Active() != current {
		t.Fatal("错误架构更新替换了可用镜像")
	}
}

// TestStoreActivatesVerifiedUpdate 验证 N12:校验通过的更新原子生效,
// 之后执行前复核使用新镜像。
func TestStoreActivatesVerifiedUpdate(t *testing.T) {
	ctx := context.Background()
	first, err := New(ABILinuxRV64, "/bin/tool", map[string][]byte{"/bin/tool": []byte("v1")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(ABILinuxRV64, "/bin/tool", map[string][]byte{"/bin/tool": []byte("v2")})
	if err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Version: ManifestVersion, Architecture: ArchitectureRV64, ABI: ABILinuxRV64, Entry: "/bin/tool"}
	store, err := NewStore(ctx, first, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, second, requirements); err != nil {
		t.Fatal(err)
	}
	if store.Active() != second {
		t.Fatal("更新未生效")
	}
	if err := store.VerifyActive(ctx, requirements); err != nil {
		t.Fatal(err)
	}
	// 初始镜像损坏时不能创建发布槽。
	if _, err := NewStore(ctx, second, Requirements{Architecture: "amd64"}); !errors.Is(err, errno.Version) {
		t.Fatalf("不兼容初始镜像 err=%v", err)
	}
}
