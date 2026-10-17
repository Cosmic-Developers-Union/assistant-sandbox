package commandio_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/cat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mktemp"
	"mvdan.cc/sh/v3/interp"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

func TestReadBudgetCancellationAndReaderFailure(t *testing.T) {
	if text, err := commandio.ReadText(t.Context(), nil); err != nil || text != "" {
		t.Fatal(text, err)
	}
	if _, err := commandio.ReadText(t.Context(), strings.NewReader(strings.Repeat("x", 4<<20+1))); err == nil {
		t.Fatal("输入未受限")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := commandio.ReadText(ctx, strings.NewReader("data")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := commandio.ReadText(t.Context(), failedReader{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestInputsPreservePathsAndEnforceAggregateBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	if err := os.WriteFile(path, []byte("内容"), 0600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	run := func(ctx context.Context, args []string) error {
		return commandio.Inputs(ctx, args, func(file, input string) error { seen = append(seen, file+":"+input); return nil })
	}
	r := commandtest.Run(t, run, dir, []string{"data", path, "-"}, "stdin")
	if r.Code != 0 || len(seen) != 3 || seen[0] != "data:内容" || seen[1] != path+":内容" || seen[2] != "-:stdin" {
		t.Fatal(r, seen)
	}
	r = commandtest.Run(t, run, dir, []string{"missing"}, "")
	if r.Code != 2 {
		t.Fatal(r)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 3<<20)), 0600); err != nil {
		t.Fatal(err)
	}
	r = commandtest.Run(t, run, dir, []string{"data", "data"}, "")
	if r.Code != 2 || !strings.Contains(r.Stderr, "累计输入") {
		t.Fatal(r)
	}
	sentinel := errors.New("处理失败")
	var got error
	fail := func(ctx context.Context, _ []string) error {
		got = commandio.Inputs(ctx, nil, func(string, string) error { return sentinel })
		return got
	}
	r = commandtest.Run(t, fail, dir, nil, "data")
	if r.Code != 2 || !errors.Is(got, sentinel) {
		t.Fatal(r, got)
	}
}

func TestExecuteReturnsIgnoredOutputFailure(t *testing.T) {
	run := func(ctx context.Context, _ []string) error {
		_, _ = io.WriteString(commandio.Context(ctx).Stdout, "output")
		return nil
	}
	err := commandtest.InvokeWith(t.Context(), run, t.TempDir(), nil, strings.NewReader(""), failedWriter{}, io.Discard, commandtest.Options{})
	if err == nil || !strings.Contains(err.Error(), "output unavailable") {
		t.Fatalf("ignored output failure was lost: %v", err)
	}
}

func TestArgumentAndRowContracts(t *testing.T) {
	run := func(ctx context.Context, args []string) error {
		flags := commandio.Flags(ctx, "example")
		value := flags.String("value", "默认", "值")
		if err := commandio.Parse(flags, args); err != nil {
			return err
		}
		_, err := io.WriteString(interp.HandlerCtx(ctx).Stdout, *value)
		return err
	}
	for _, test := range []struct {
		args []string
		want string
		code int
	}{{nil, "默认", 0}, {[]string{"--value", "配置"}, "配置", 0}, {[]string{"--unknown"}, "", 2}} {
		r := commandtest.Run(t, run, t.TempDir(), test.args, "")
		if r.Code != test.code || r.Stdout != test.want {
			t.Fatal(r)
		}
	}
	for _, test := range []struct {
		input string
		want  []string
	}{{"", nil}, {"a\n", []string{"a"}}, {"a\nb", []string{"a", "b"}}} {
		if !slices.Equal(commandio.Rows(test.input), test.want) {
			t.Fatal(test)
		}
	}
	status := func(context.Context, []string) error { return interp.ExitStatus(7) }
	if r := commandtest.Run(t, status, t.TempDir(), nil, ""); r.Code != 7 {
		t.Fatal(r)
	}
}
func TestVMAdapterBindsStdinDirectoryAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, args []string) error { return cat.Run(ctx, args) }
	for _, test := range []struct {
		args        []string
		input, want string
		code        int
	}{{nil, "stdin", "stdin", 0}, {[]string{"file"}, "", "data", 0}, {[]string{"missing"}, "", "", 2}} {
		r := commandtest.Run(t, run, dir, test.args, test.input)
		if r.Code != test.code || r.Stdout != test.want {
			t.Fatal(r)
		}
	}
	temporary := func(ctx context.Context, args []string) error { return mktemp.Run(ctx, args) }
	r := commandtest.Run(t, temporary, dir, nil, "")
	if r.Code != 0 || !strings.HasPrefix(r.Stdout, dir) {
		t.Fatal(r)
	}
}
