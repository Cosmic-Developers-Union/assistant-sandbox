package image

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func manifestFixture(t *testing.T) *Memory {
	t.Helper()
	img, err := New(ABILinuxRV64, "/bin/python3", map[string][]byte{
		"/bin/python3": []byte("python-bytes"),
		"/lib/libc.so": []byte("libc-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	img.DeclareDependencies(Dependency{Name: "musl", Version: "1.2.6", Digest: ContentDigest([]byte("musl"))})
	return img
}

// TestManifestRecordsArchitectureABIEntryFilesAndDependencies 验证 N01:
// 清单记录架构/ABI/入口/文件权限与摘要/依赖,编码解码往返稳定。
func TestManifestRecordsArchitectureABIEntryFilesAndDependencies(t *testing.T) {
	img := manifestFixture(t)
	manifest, err := img.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != ManifestVersion || manifest.Architecture != ArchitectureRV64 ||
		manifest.ABI != ABILinuxRV64 || manifest.Entry != "/bin/python3" {
		t.Fatalf("清单头=%+v", manifest)
	}
	if len(manifest.Files) != 2 || manifest.Files[0].Path != "/bin/python3" || manifest.Files[0].Mode != 0o755 {
		t.Fatalf("文件清单=%+v", manifest.Files)
	}
	if manifest.Files[1].Path != "/lib/libc.so" || manifest.Files[1].Mode != 0o644 || manifest.Files[1].Size != int64(len("libc-bytes")) {
		t.Fatalf("普通文件条目=%+v", manifest.Files[1])
	}
	if len(manifest.Dependencies) != 1 || manifest.Dependencies[0].Name != "musl" || manifest.Dependencies[0].Version != "1.2.6" {
		t.Fatalf("依赖=%+v", manifest.Dependencies)
	}
	encoded, err := manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeManifest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Digest != manifest.Digest || decoded.Architecture != manifest.Architecture ||
		decoded.ABI != manifest.ABI || decoded.Entry != manifest.Entry ||
		len(decoded.Files) != len(manifest.Files) || len(decoded.Dependencies) != len(manifest.Dependencies) {
		t.Fatalf("往返后=%+v", decoded)
	}
}

// TestManifestValidationRejectsIncompatibleImages 验证 N01:启动前
// 兼容性检查对版本、架构、ABI、入口与摘要差异明确失败。
func TestManifestValidationRejectsIncompatibleImages(t *testing.T) {
	img := manifestFixture(t)
	manifest, err := img.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	good := Requirements{Version: ManifestVersion, Architecture: ArchitectureRV64, ABI: ABILinuxRV64, Entry: "/bin/python3", Digest: manifest.Digest}
	if err := Verify(context.Background(), img, good); err != nil {
		t.Fatalf("兼容镜像应通过: %v", err)
	}
	cases := []struct {
		name string
		req  Requirements
	}{
		{"版本", Requirements{Version: ManifestVersion + 1}},
		{"架构", Requirements{Architecture: "amd64"}},
		{"ABI", Requirements{ABI: "linux-x86_64"}},
		{"入口", Requirements{Entry: "/bin/sh"}},
		{"摘要", Requirements{Digest: ContentDigest([]byte("other"))}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := Verify(context.Background(), img, test.req); !errors.Is(err, errno.Version) {
				t.Fatalf("不兼容镜像 err=%v want EPROTO", err)
			}
		})
	}
	// 结构损坏:重复文件条目与非法摘要。
	broken := manifest
	broken.Files = append([]FileEntry(nil), manifest.Files...)
	broken.Files[0].Digest = "sha256:short"
	if err := broken.Validate(Requirements{}); !errors.Is(err, errno.Version) {
		t.Fatalf("非法摘要 err=%v", err)
	}
}

// TestVerifyDetectsContentDigestMismatch 验证 N01:清单声明与真实内容
// 摘要不一致时启动前拒绝;错误可由主机只读缓存损坏等触发。
func TestVerifyDetectsContentDigestMismatch(t *testing.T) {
	img := manifestFixture(t)
	manifest, err := img.Manifest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stub := &digestStub{manifest: manifest, digests: map[string]string{
		"/bin/python3": manifest.Files[0].Digest,
		"/lib/libc.so": ContentDigest([]byte("tampered")),
	}}
	if err := Verify(context.Background(), stub, Requirements{}); !errors.Is(err, errno.Version) {
		t.Fatalf("摘要不一致 err=%v want EPROTO", err)
	}
}

// TestDecodeRejectsUnknownVersionAndMalformedJSON 验证 N01:未知清单
// 版本或损坏文档不会被当作可用镜像。
func TestDecodeRejectsUnknownVersionAndMalformedJSON(t *testing.T) {
	if _, err := DecodeManifest([]byte("{")); !errors.Is(err, errno.Version) {
		t.Fatalf("损坏 JSON err=%v", err)
	}
	encoded := []byte(`{"version":99,"digest":"sha256:` + strings.Repeat("0", 64) + `","abi":"x","entry":"/e","architecture":"riscv64"}`)
	if _, err := DecodeManifest(encoded); !errors.Is(err, errno.Version) {
		t.Fatalf("未知版本 err=%v", err)
	}
}

type digestStub struct {
	manifest Manifest
	digests  map[string]string
}

func (s *digestStub) Manifest(context.Context) (Manifest, error) { return s.manifest, nil }

func (s *digestStub) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errno.NotFound
}

func (s *digestStub) DigestFor(_ context.Context, path string) (string, error) {
	digest, ok := s.digests[path]
	if !ok {
		return "", errno.NotFound
	}
	return digest, nil
}
