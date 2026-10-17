package shell

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestEnvExecutesUtility(t *testing.T) {
	for _, tc := range []struct {
		script, out string
		code        int
	}{
		{`env KEY=value sh -c 'printf "%s" "$KEY"'`, "value", 0},
		{`env KEY=value true`, "", 0},
		{`env false`, "", 1},
		{`env -i sh -c 'printf "%s" "${KEY-未设置}"'`, "未设置", 0},
		{`env first=1 second=2 sh -c 'printf "%s:%s" "$first" "$second"'`, "1:2", 0},
		{`env -i KEY=value`, "KEY=value\n", 0},
		{`env missing-sandbox-command`, "", 127},
	} {
		t.Run(tc.script, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			err := run(t.Context(), tc.script, t.TempDir(), nil, strings.NewReader(""), &out, &diagnostic)
			code := 0
			if err != nil {
				status, ok := errors.AsType[interp.ExitStatus](err)
				if !ok {
					t.Fatal(err)
				}
				code = int(status)
			}
			if code != tc.code || out.String() != tc.out {
				t.Fatalf("code %d, stdout %q, stderr %q", code, out.String(), diagnostic.String())
			}
		})
	}

	var out, diagnostic bytes.Buffer
	err := run(t.Context(), `EXPORTED=parent; export EXPORTED; env -i sh -c 'printf "%s" "${EXPORTED-未设置}"'`,
		t.TempDir(), nil, strings.NewReader(""), &out, &diagnostic)
	if err != nil || out.String() != "未设置" {
		t.Fatalf("stdout %q, stderr %q, err %v", out.String(), diagnostic.String(), err)
	}
}
