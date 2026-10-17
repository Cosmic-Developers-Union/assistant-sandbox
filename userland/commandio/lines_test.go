package commandio_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func TestLinesStreamsStdinAndFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "one.txt", "a\nb\nc\n")
	writeFile(t, dir, "two.txt", "d\n")

	collect := func(stop int) commandio.Handler {
		return func(ctx context.Context, args []string) error {
			var got []string
			count := 0
			err := commandio.Lines(ctx, args, func(_, line string, hasNewline bool) (bool, error) {
				if !hasNewline {
					got = append(got, line+"<no-newline>")
				} else {
					got = append(got, line)
				}
				count++
				if stop > 0 && count >= stop {
					return false, nil
				}
				return true, nil
			})
			if err != nil {
				return err
			}
			_, err = io.WriteString(commandio.Context(ctx).Stdout, strings.Join(got, ","))
			return err
		}
	}
	for _, tc := range []struct {
		name string
		args []string
		in   string
		stop int
		want string
	}{
		{"stdin", nil, "x\ny\n", 0, "x,y"},
		{"stdin 末行无换行", nil, "x\ny", 0, "x,y<no-newline>"},
		{"文件", []string{"one.txt"}, "", 0, "a,b,c"},
		{"多文件顺序", []string{"one.txt", "two.txt"}, "", 0, "a,b,c,d"},
		{"读够即停", []string{"one.txt", "two.txt"}, "", 2, "a,b"},
		{"空输入", nil, "", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := commandtest.Run(t, collect(tc.stop), dir, tc.args, tc.in)
			if result.Code != 0 || result.Stdout != tc.want {
				t.Fatalf("result=%+v want %q", result, tc.want)
			}
		})
	}
}

func TestLinesReportsFileAndVisitFailures(t *testing.T) {
	dir := t.TempDir()
	missing := func(ctx context.Context, _ []string) error {
		return commandio.Lines(ctx, []string{"missing.txt"}, func(string, string, bool) (bool, error) {
			return true, nil
		})
	}
	if result := commandtest.Run(t, missing, dir, nil, ""); result.Code == 0 {
		t.Fatal("缺失文件必须失败")
	}
	sentinel := errors.New("访问者故障")
	failing := func(ctx context.Context, _ []string) error {
		return commandio.Lines(ctx, nil, func(string, string, bool) (bool, error) {
			return false, sentinel
		})
	}

	_ = commandtest.Run(t, failing, dir, nil, "line\n")
	var diagnostic strings.Builder
	if err := commandtest.Invoke(t.Context(), failing, dir, nil, strings.NewReader("line\n"), io.Discard, &diagnostic); err == nil {
		t.Fatal("访问者错误必须中止读取")
	}
	if !strings.Contains(diagnostic.String(), sentinel.Error()) {
		t.Fatalf("诊断=%q 未包含访问者错误", diagnostic.String())
	}
}

func TestLinesStopsAtBudget(t *testing.T) {
	var diagnostic strings.Builder
	handler := func(ctx context.Context, _ []string) error {
		return commandio.Lines(ctx, nil, func(string, string, bool) (bool, error) {
			return true, nil
		})
	}

	endless := stringRepeat("0123456789\n", 1<<20)
	if err := commandtest.Invoke(t.Context(), handler, t.TempDir(), nil, strings.NewReader(endless), io.Discard, &diagnostic); err == nil {
		t.Fatal("超预算输入必须失败")
	}
	if !strings.Contains(diagnostic.String(), "4 MiB") {
		t.Fatalf("诊断=%q 未包含预算说明", diagnostic.String())
	}
}

func TestLinesBoundsUnterminatedInput(t *testing.T) {
	var diagnostic strings.Builder
	handler := func(ctx context.Context, _ []string) error {
		return commandio.Lines(ctx, nil, func(string, string, bool) (bool, error) {
			return true, nil
		})
	}
	if err := commandtest.Invoke(t.Context(), handler, t.TempDir(), nil, endlessInput{}, io.Discard, &diagnostic); err == nil {
		t.Fatal("无换行无界输入必须失败")
	}
	if !strings.Contains(diagnostic.String(), "4 MiB") {
		t.Fatalf("诊断=%q 未包含预算说明", diagnostic.String())
	}
}

func TestStreamDeliversBoundedPrefix(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "one.txt", "abcdef")
	writeFile(t, dir, "two.txt", "ghij")
	handler := func(ctx context.Context, args []string) error {
		var out strings.Builder
		err := commandio.Stream(ctx, args, func(name string, reader io.Reader) error {
			prefix := make([]byte, 2)
			if _, err := io.ReadFull(reader, prefix); err != nil {
				return err
			}
			out.WriteString(name + "=" + string(prefix) + ";")
			return nil
		})
		if err != nil {
			return err
		}
		_, err = io.WriteString(commandio.Context(ctx).Stdout, out.String())
		return err
	}
	result := commandtest.Run(t, handler, dir, []string{"one.txt", "two.txt"}, "")
	if result.Code != 0 || result.Stdout != "one.txt=ab;two.txt=gh;" {
		t.Fatalf("Stream 得到 %+v", result)
	}
}

func TestStreamStopsAtBudget(t *testing.T) {
	var diagnostic strings.Builder
	handler := func(ctx context.Context, _ []string) error {
		return commandio.Stream(ctx, nil, func(_ string, reader io.Reader) error {
			_, err := io.Copy(io.Discard, reader)
			return err
		})
	}
	if err := commandtest.Invoke(t.Context(), handler, t.TempDir(), nil, endlessInput{}, io.Discard, &diagnostic); err == nil {
		t.Fatal("无界输入必须撞上读取预算")
	}
	if !strings.Contains(diagnostic.String(), "4 MiB") {
		t.Fatalf("诊断=%q 未包含预算说明", diagnostic.String())
	}
}

type endlessInput struct{}

func (endlessInput) Read(p []byte) (int, error) { return len(p), nil }

func TestLinesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	handler := func(ctx context.Context, _ []string) error {
		return commandio.Lines(ctx, nil, func(string, string, bool) (bool, error) {
			return true, nil
		})
	}
	if err := commandtest.Invoke(ctx, handler, t.TempDir(), nil, strings.NewReader("a\nb\n"), io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应中止读取: %v", err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stringRepeat(value string, times int) string {
	return strings.Repeat(value, times)
}
