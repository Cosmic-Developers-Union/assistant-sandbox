package shell

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestLoopProtocolFailuresAndTermination(t *testing.T) {
	for _, input := range []string{`{`, `{"ID":0,"Command":"true","OutputLimit":1}`, `{"ID":1,"Command":"true","OutputLimit":0}`, `{"ID":1,"Command":"true","OutputLimit":1048577}`, `{"ID":1,"Command":"true","OutputLimit":1,"extra":true}`} {
		if err := loop(t.Context(), strings.NewReader(input+"\n"), new(bytes.Buffer), t.TempDir(), nil); err == nil {
			t.Fatal(input)
		}
	}
	for _, command := range []string{"if", "echo denied &", "exit 7", "printf 'too much output'"} {
		request := map[string]any{"ID": 1, "Command": command, "OutputLimit": 4}
		data, _ := json.Marshal(request)
		var out bytes.Buffer
		if err := loop(t.Context(), strings.NewReader(string(data)+"\n"), &out, t.TempDir(), nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"Code":`) || !strings.Contains(out.String(), `"ID":1`) {
			t.Fatal(out.String())
		}
	}
	if err := loop(t.Context(), strings.NewReader(""), io.Discard, "/missing-directory", nil); err == nil {
		t.Fatal("无效目录")
	}
	if err := loop(t.Context(), failedReader{}, io.Discard, t.TempDir(), nil); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if err := loop(t.Context(), strings.NewReader("{\"ID\":1,\"Command\":\"true\",\"OutputLimit\":8}\n"), failedWriter{}, t.TempDir(), nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

func TestExtensionProtocolAndOutputFailures(t *testing.T) {
	for _, tc := range []struct {
		response string
		stdin    io.Reader
		writer   io.Writer
		wantErr  bool
	}{
		{`{"Code":0,"Stdout":"result","Stderr":"diagnostic"}`, strings.NewReader("input"), new(bytes.Buffer), false},
		{`{"Code":7,"Stdout":"","Stderr":"failed"}`, nil, new(bytes.Buffer), true},
		{`{`, nil, new(bytes.Buffer), true},
		{`{"Code":0,"Stdout":"result","Stderr":""}`, nil, failedWriter{}, true},
		{`{"Code":0,"Stdout":"","Stderr":"diagnostic"}`, nil, failedWriter{}, true},

		{`{}`, strings.NewReader(strings.Repeat("x", 1<<20+1)), new(bytes.Buffer), true},
	} {
		runner, err := interp.New(interp.Dir(t.TempDir()), interp.StdIO(tc.stdin, tc.writer, tc.writer), interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				return extensionWith(ctx, args, func(request, response []byte) uint32 { copy(response, tc.response); return uint32(len(tc.response)) })
			}
		}))
		if err != nil {
			t.Fatal(err)
		}
		tree, _ := syntax.NewParser().Parse(strings.NewReader("extension"), "")
		err = runner.Run(t.Context(), tree)
		if (err != nil) != tc.wantErr {
			t.Fatal(tc.response, err)
		}
	}
}
