package messages

import (
	"context"
	"encoding/json/jsontext"
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestSnapshotQuery(t *testing.T) {
	records := []jsontext.Value{jsontext.Value(`{"role":"user","content":[{"type":"text","text":"任务"}]}`), jsontext.Value(`{"role":"assistant","content":[{"type":"text","text":"第一条"}]}`), jsontext.Value(`{"role":"assistant","content":[{"type":"text","text":"第二条"}]}`)}
	run := func(ctx context.Context, args []string) error { return Run(WithSnapshot(ctx, records), args) }
	for _, test := range []struct {
		args []string
		want string
		code int
	}{
		{nil, "第一条\n\n第二条", 0}, {[]string{"--last", "1"}, "第二条", 0}, {[]string{"--role", "user"}, "任务", 0}, {[]string{"--role", "all", "--format", "jsonl"}, "\"role\":\"user\"", 0}, {[]string{"--role", "invalid"}, "", 2}, {[]string{"--last", "-1"}, "", 2}, {[]string{"--bad"}, "", 2}, {[]string{"--help"}, "", 0},
	} {
		r := commandtest.Run(t, run, t.TempDir(), test.args, "")
		if r.Code != test.code || !strings.Contains(r.Stdout, test.want) {
			t.Fatal(test, r)
		}
	}
	bad := func(ctx context.Context, args []string) error {
		return Run(WithSnapshot(ctx, []jsontext.Value{jsontext.Value("broken")}), args)
	}
	if r := commandtest.Run(t, bad, t.TempDir(), nil, ""); r.Code != 2 {
		t.Fatal(r)
	}
}

func TestDownstreamOutputFailure(t *testing.T) {
	err := commandtest.Invoke(t.Context(), func(ctx context.Context, args []string) error {
		return Run(WithSnapshot(ctx, []jsontext.Value{jsontext.Value(`{"role":"assistant","content":[{"type":"text","text":"内容"}]}`)}), args)
	}, t.TempDir(), []string{}, strings.NewReader("a:1\nb:2\n"), commandtest.FailWriter{}, io.Discard)
	if err == nil {
		t.Fatal("输出失败被忽略")
	}
}
