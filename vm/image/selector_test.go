package image

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// TestCatalogSelectsOnlyVerifiedImages 验证 R03:缺失、版本不兼容与
// 摘要损坏的镜像都在启动前被拒绝,且不会隐式下载。
func TestCatalogSelectsOnlyVerifiedImages(t *testing.T) {
	ctx := context.Background()
	good := manifestFixture(t)
	manifest, err := good.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	damaged := &digestStub{manifest: manifest, digests: map[string]string{
		"/bin/python3": manifest.Files[0].Digest,
		"/lib/libc.so": ContentDigest([]byte("damaged")),
	}}
	catalog := NewCatalog()
	if err := catalog.Add("good", good); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Add("damaged", damaged); err != nil {
		t.Fatal(err)
	}
	requirements := Requirements{Version: ManifestVersion, Architecture: ArchitectureRV64, ABI: ABILinuxRV64, Entry: "/bin/python3"}
	if _, err := catalog.Select(ctx, "good", requirements); err != nil {
		t.Fatalf("可用镜像: %v", err)
	}
	if _, err := catalog.Select(ctx, "missing", requirements); !errors.Is(err, errno.NotFound) {
		t.Fatalf("缺失镜像 err=%v want ENOENT", err)
	}
	if _, err := catalog.Select(ctx, "damaged", requirements); !errors.Is(err, errno.Version) {
		t.Fatalf("损坏镜像 err=%v want EPROTO", err)
	}
	if _, err := catalog.Select(ctx, "good", Requirements{Architecture: "amd64"}); !errors.Is(err, errno.Version) {
		t.Fatalf("不兼容版本 err=%v want EPROTO", err)
	}
	if err := catalog.Add("good", good); !errors.Is(err, errno.Exist) {
		t.Fatalf("重复登记 err=%v", err)
	}
}
