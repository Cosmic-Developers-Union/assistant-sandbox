package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInterpreterAdaptationFailsClosedOnUnknownSource(t *testing.T) {
	for _, names := range [][]string{nil, {"vars.go", "runner.go", "builtin.go"}} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "interp"), 0700); err != nil {
			t.Error(err)
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(root, "interp", name), []byte("package interp\n"), 0600); err != nil {
				t.Error(err)
			}
		}
		if err := adaptInterpreter(root); err == nil {
			t.Fatal("不认识的上游代码生成了客体")
		}
	}
	for _, tc := range []struct {
		source, start, end, replacement string
		ok                              bool
	}{
		{"before START body END after", "START", "END", "replacement", true},
		{"unrecognized", "START", "END", "", false},
		{"before START body", "START", "END", "", false},
	} {
		result, err := replaceRegion(tc.source, tc.start, tc.end, tc.replacement)
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
		if tc.ok && result != "before replacementEND after" {
			t.Fatal(result)
		}
	}
}
