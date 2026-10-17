package image

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

func validFiles() map[string][]byte {
	return map[string][]byte{
		"/bin/probe":  []byte("guest-binary"),
		"/etc/config": []byte("key=value"),
	}
}

func TestNewValidatesEntryAndABI(t *testing.T) {
	if _, err := New("", "/bin/probe", validFiles()); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := New(ABIBuiltin, "", validFiles()); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := New(ABIBuiltin, "/missing", validFiles()); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	if _, err := New(ABIBuiltin, "/bin/probe", nil); !errors.Is(err, errno.Invalid) {
		t.Fatal(err)
	}
	if _, err := New(ABIBuiltin, "bin/probe", map[string][]byte{"bin/probe": []byte("x")}); !errors.Is(err, errno.Invalid) {
		t.Fatal("镜像路径必须为绝对路径")
	}
}

func TestDigestBindsContentAndPaths(t *testing.T) {
	a, err := New(ABIBuiltin, "/bin/probe", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(ABIBuiltin, "/bin/probe", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	if a.man.Digest != b.man.Digest {
		t.Fatal("相同内容应产生相同摘要")
	}
	changed := validFiles()
	changed["/bin/probe"] = []byte("guest-binary-v2")
	c, _ := New(ABIBuiltin, "/bin/probe", changed)
	if c.man.Digest == a.man.Digest {
		t.Fatal("内容变化必须改变摘要")
	}

	renamed := validFiles()
	delete(renamed, "/etc/config")
	renamed["/etc/other"] = []byte("key=value")
	d, _ := New(ABIBuiltin, "/bin/probe", renamed)
	if d.man.Digest == a.man.Digest {
		t.Fatal("路径变化必须改变摘要")
	}
	man, err := a.Manifest(context.Background())
	if err != nil || man.ABI != ABIBuiltin || man.Entry != "/bin/probe" {
		t.Fatalf("man=%+v err=%v", man, err)
	}
	if !strings.HasPrefix(man.Digest, "sha256:") {
		t.Fatalf("digest=%q", man.Digest)
	}
}

func TestOpenRejectsUnknownPaths(t *testing.T) {
	m, err := New(ABIBuiltin, "/bin/probe", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	file, err := m.Open(context.Background(), "/bin/probe")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "guest-binary" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), "/etc/unknown"); !errors.Is(err, errno.NotFound) {
		t.Fatal("镜像外的路径必须失败")
	}
	if paths := m.Paths(); len(paths) != 2 || paths[0] != "/bin/probe" {
		t.Fatalf("paths=%v", paths)
	}
}

func TestDeclareBuilderRecordsProvenanceWithoutChangingDigest(t *testing.T) {
	ctx := context.Background()
	image, err := New(ABIBuiltin, "/bin/probe", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	before, err := image.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Registry != "" || before.Language != "" {
		t.Fatalf("未声明时来源应为空: %+v", before)
	}
	image.DeclareBuilder("registry-v1", "bash")
	after, err := image.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Registry != "registry-v1" || after.Language != "bash" {
		t.Fatalf("来源未记录: %+v", after)
	}
	if after.Digest != before.Digest || after.ABI != before.ABI || after.Entry != before.Entry {
		t.Fatalf("声明来源不得改变内容身份: before=%+v after=%+v", before, after)
	}

	other, err := New(ABIBuiltin, "/bin/probe", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	other.DeclareBuilder("registry-v2", "bash")
	otherManifest, err := other.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if otherManifest.Digest != after.Digest {
		t.Fatal("相同内容必须得到相同摘要")
	}
	if otherManifest.Registry == after.Registry {
		t.Fatal("来源不同必须可区分")
	}

	image.DeclareBuilder("registry-v3", "other")
	final, err := image.Manifest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.Registry != "registry-v3" || final.Language != "other" {
		t.Fatalf("重复声明应以最后一次为准: %+v", final)
	}
	if final.Digest != before.Digest {
		t.Fatal("重复声明不得改变摘要")
	}
}

func TestDigestForReportsContentIdentity(t *testing.T) {
	ctx := context.Background()
	image, err := New(ABIBuiltin, "/bin/probe", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	first, err := image.DigestFor(ctx, "/bin/probe")
	if err != nil || first == "" {
		t.Fatalf("digest=%q err=%v", first, err)
	}
	if again, _ := image.DigestFor(ctx, "/bin/probe"); again != first {
		t.Fatal("同一路径必须得到同一身份")
	}

	if want := digest.Content(validFiles()["/bin/probe"]); first != want {
		t.Fatalf("digest=%q want %q", first, want)
	}

	changed := map[string][]byte{"/bin/probe": []byte("different")}
	other, err := New(ABIBuiltin, "/bin/probe", changed)
	if err != nil {
		t.Fatal(err)
	}
	if otherDigest, err := other.DigestFor(ctx, "/bin/probe"); err != nil || otherDigest == first {
		t.Fatalf("不同内容应得到不同身份: %q err=%v", otherDigest, err)
	}

	if _, err := image.DigestFor(ctx, "/etc/unknown"); !errors.Is(err, errno.NotFound) {
		t.Fatalf("镜像外路径应报 ENOENT: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := image.DigestFor(cancelled, "/bin/probe"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}
