package sandbox

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"
)

func TestMessageSnapshotAndWASIRefresh(t *testing.T) {
	source := func(ctx context.Context) (string, []jsontext.Value, error) {
		latest, _ := ctx.Value(messageTestKey{}).(string)
		if latest == "" {
			return "", nil, nil
		}
		return latest, []jsontext.Value{jsontext.Value(`{"role":"user","content":[{"type":"text","text":"任务"}]}`)}, nil
	}
	r, err := newTestRuntime(t.Context(), Config{MessageSource: source})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	dir := t.TempDir()
	session, err := r.Bind(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.WithValue(t.Context(), messageTestKey{}, strings.Repeat("字", 2000))
	var out bytes.Buffer
	result, err := session.Execute(ctx, dir, `N=$(printf '%s' "$MESSAGE" | wc -m); printf '%s\n' "$N"; test "$N" -ge 2000; messages --role user`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "2000\n任务" {
		t.Fatal(result, err, out.String())
	}
	out.Reset()
	result, err = session.Execute(ctx, dir, `bash -c 'printf "%s" "$MESSAGE" | wc -m; messages --role user; exit 7'; printf ':%s' "$?"`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "2000\n任务:7" {
		t.Fatal("WASI 子解释器未继承数据或隔离退出", result, err, out.String())
	}
	out.Reset()
	result, err = session.Execute(t.Context(), dir, `printf '<%s>' "$MESSAGE"; messages --role all --format jsonl`, &out, &out)
	if err != nil || result.ExitCode != 0 || out.String() != "<>" {
		t.Fatal("跨调用泄露了旧消息", result, err, out.String())
	}
	oversized := context.WithValue(context.Background(), messageTestKey{}, strings.Repeat("x", maxMessagesBytes))
	if _, _, err := messageSnapshot(oversized, source); err == nil {
		t.Fatal("完整快照超限未明确拒绝")
	}
}

type messageTestKey struct{}
