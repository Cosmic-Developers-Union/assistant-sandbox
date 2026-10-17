package memfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdfs "io/fs"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

func populatedFS(t *testing.T) *FS {
	t.Helper()
	system, err := New(&clock.Virtual{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := system.Mkdir(ctx, "/etc", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := system.Mkdir(ctx, "/etc/deep", 0o700); err != nil {
		t.Fatal(err)
	}
	writeContent(t, system, "/etc/config", "key=value", 0o640)
	writeContent(t, system, "/etc/deep/inner", "inner-data", 0o600)
	if err := system.Symlink(ctx, "/etc/config", "/link"); err != nil {
		t.Fatal(err)
	}
	return system
}

func writeContent(t *testing.T, system *FS, path, content string, mode uint32) {
	t.Helper()
	file, err := system.Open(context.Background(), path, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, stdfs.FileMode(mode))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func readText(t *testing.T, system *FS, path string) string {
	t.Helper()
	data, err := readAll(context.Background(), system, path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSnapshotEncodeDecodeRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := populatedFS(t)
	snapshotted, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encodable, ok := snapshotted.(Encodable)
	if !ok {
		t.Fatal("memfs 快照应可序列化")
	}
	data, err := encodable.Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(SnapshotFormat)) {
		t.Fatalf("编码应携带格式标识: %s", data)
	}
	decoded, err := DecodeSnapshot(ctx, data)
	if err != nil {
		t.Fatal(err)
	}

	target, err := New(&clock.Virtual{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Restore(ctx, target); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, target, "/etc/config"); got != "key=value" {
		t.Fatalf("文件内容=%q", got)
	}
	if got := readText(t, target, "/etc/deep/inner"); got != "inner-data" {
		t.Fatalf("嵌套文件内容=%q", got)
	}

	if link, err := target.Readlink(ctx, "/link"); err != nil || link != "/etc/config" {
		t.Fatalf("链接=%q err=%v", link, err)
	}

	info, err := target.Stat(ctx, "/etc/config")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("权限=%v want 0640", info.Mode().Perm())
	}
	dirInfo, err := target.Stat(ctx, "/etc")
	if err != nil {
		t.Fatal(err)
	}
	if !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o750 {
		t.Fatalf("目录模式=%v", dirInfo.Mode())
	}

	entries, err := target.ReadDir(ctx, "/etc")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("目录项=%d want 2", len(entries))
	}
}

func TestSnapshotEncodingIsDeterministic(t *testing.T) {
	ctx := context.Background()
	source := populatedFS(t)
	first, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstData, err := first.(Encodable).Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secondData, err := second.(Encodable).Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstData, secondData) {
		t.Fatalf("同一棵树两次编码应一致:\n%s\n%s", firstData, secondData)
	}

	for attempt := 0; attempt < 5; attempt++ {
		again, err := first.(Encodable).Encode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstData, again) {
			t.Fatalf("第 %d 次编码与首次不一致", attempt)
		}
	}
}

func TestDecodeSnapshotRejectsCorruption(t *testing.T) {
	ctx := context.Background()
	source := populatedFS(t)
	snapshotted, err := source.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	good, err := snapshotted.(Encodable).Encode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label  string
		data   []byte
		target error
	}{
		{"空字节", nil, errno.Invalid},
		{"非 JSON", []byte("not json"), errno.Invalid},
		{"缺格式", []byte(`{"nodes":[]}`), errno.Version},
		{"版本不符", []byte(`{"format":"memfs-snapshot-0","nodes":[],"digest":""}`), errno.Version},
		{"未知字段", []byte(`{"format":"` + SnapshotFormat + `","nodes":[],"digest":"","extra":1}`), errno.Invalid},
		{"摘要不符", []byte(`{"format":"` + SnapshotFormat + `","nodes":[{"path":"/","kind":"dir"}],"digest":"sha256:00"}`), errno.Version},

		{"未知节点类型", payloadWithKind(t, "bogus"), errno.Invalid},
	} {
		if _, err := DecodeSnapshot(ctx, tc.data); !errors.Is(err, tc.target) {
			t.Errorf("%s: err=%v want %v", tc.label, err, tc.target)
		}
	}

	tampered := bytes.Replace(good, []byte("sha256:"), []byte("sha256:0"), 1)
	if _, err := DecodeSnapshot(ctx, tampered); err == nil {
		t.Fatal("改动摘要应被拒绝")
	}
}

func payloadWithKind(t *testing.T, kind string) []byte {
	t.Helper()
	records := []nodeRecord{{Path: "/", Kind: kind}}
	digest, err := payloadDigest(records)
	if err != nil {
		t.Fatal(err)
	}
	return mustMarshal(t, snapshotPayload{Format: SnapshotFormat, Nodes: records, Digest: digest})
}

func TestDecodeSnapshotRejectsBrokenStructure(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		label string
		nodes string
	}{
		{"无节点", `[]`},
		{"缺根", `[{"path":"/a","kind":"dir"}]`},
		{"根不是目录", `[{"path":"/","kind":"file"}]`},
		{"缺父目录", `[{"path":"/","kind":"dir"},{"path":"/a/b","kind":"dir"}]`},
		{"重复路径", `[{"path":"/","kind":"dir"},{"path":"/a","kind":"dir"},{"path":"/a","kind":"dir"}]`},
		{"链接缺目标", `[{"path":"/","kind":"dir"},{"path":"/l","kind":"symlink"}]`},
	} {
		data := buildPayload(t, tc.nodes)
		if _, err := DecodeSnapshot(ctx, data); err == nil {
			t.Errorf("%s: 应被拒绝", tc.label)
		}
	}
}

func buildPayload(t *testing.T, nodes string) []byte {
	t.Helper()

	var records []nodeRecord
	decoder := newStrictDecoder([]byte(`{"nodes":` + nodes + `}`))
	if err := decoder.Decode(&struct {
		Nodes *[]nodeRecord `json:"nodes"`
	}{Nodes: &records}); err != nil {
		t.Fatalf("构造测试输入失败: %v", err)
	}
	digest, err := payloadDigest(records)
	if err != nil {
		t.Fatal(err)
	}
	payload := snapshotPayload{Format: SnapshotFormat, Nodes: records, Digest: digest}
	return mustMarshal(t, payload)
}

func newStrictDecoder(data []byte) *json.Decoder {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSnapshotEncodeRejectsEmpty(t *testing.T) {
	ctx := context.Background()
	if _, err := (&snapshot{}).Encode(ctx); !errors.Is(err, errno.Invalid) {
		t.Fatalf("空快照应报 EINVAL: %v", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	empty, _ := New(&clock.Virtual{}, Options{})
	snapshotted, err := empty.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotted.(Encodable).Encode(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
	if _, err := DecodeSnapshot(cancelled, []byte("{}")); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应报取消: %v", err)
	}
}
