package shell

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestXargsExecutesBatches(t *testing.T) {
	for _, tc := range []struct {
		script, out string
		code        int
	}{
		{`printf 'a b c\n' | xargs echo`, "a b c\n", 0},
		{`printf '1\n2\n3\n' | xargs -n2 echo`, "1 2\n3\n", 0},
		{`printf 'x\ny\n' | xargs -I{} echo '[{}]'`, "[x]\n[y]\n", 0},
		{`printf 'a\0b c\0' | xargs -0 echo`, "a b c\n", 0},
		{`printf '   \n' | xargs -r echo ran`, "", 0},
		{`printf 'a\n' | xargs -t echo`, "a\n", 0},

		{`printf 'a\n' | xargs false`, "", 123},
		{`printf 'a\n' | xargs missing-sandbox-command`, "", 127},
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
}
