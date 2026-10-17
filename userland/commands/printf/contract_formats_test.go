package printfcmd

import (
	"bytes"
	"io"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

func runPrintf(args ...string) (int, string, string) {
	var out, diagnostic bytes.Buffer
	rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{Out: &out, Err: &diagnostic}}
	code := cmd.Run(rc, args)
	return code, out.String(), diagnostic.String()
}

func TestFormatMatrix(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"a\\nb"}, "a\nb"},
		{[]string{"\\101"}, "A"},
		{[]string{"\\x41"}, "A"},
		{[]string{"\\x"}, "\\x"},
		{[]string{"\\"}, "\\"},
		{[]string{"\\q"}, "\\q"},
		{[]string{"\\a\\b\\e\\f\\r\\t\\v"}, "\a\b\x1b\f\r\t\v"},
		{[]string{"a\\cb"}, "a"},
		{[]string{"%%"}, "%"},
		{[]string{"%d", "42"}, "42"},
		{[]string{"%i", "42"}, "42"},
		{[]string{"%5d", "42"}, "   42"},
		{[]string{"%-5d", "42"}, "42   "},
		{[]string{"%05d", "42"}, "00042"},
		{[]string{"%+d", "42"}, "+42"},
		{[]string{"% d", "42"}, " 42"},
		{[]string{"%.3d", "42"}, "042"},
		{[]string{"%*d", "6", "42"}, "    42"},
		{[]string{"%.*d", "3", "42"}, "042"},
		{[]string{"%*d", "-5", "42"}, "42   "},
		{[]string{"%o", "8"}, "10"},
		{[]string{"%#o", "8"}, "010"},
		{[]string{"%u", "42"}, "42"},
		{[]string{"%x", "255"}, "ff"},
		{[]string{"%X", "255"}, "FF"},
		{[]string{"%#x", "255"}, "0xff"},
		{[]string{"%#X", "255"}, "0XFF"},
		{[]string{"%c", "abc"}, "a"},
		{[]string{"%c", ""}, "\x00"},
		{[]string{"%5c", "a"}, "    a"},
		{[]string{"%s", "abc"}, "abc"},
		{[]string{"%.2s", "abc"}, "ab"},
		{[]string{"%5s", "abc"}, "  abc"},
		{[]string{"%-5s", "abc"}, "abc  "},
		{[]string{"%b", `a\tb`}, "a\tb"},
		{[]string{"%b", `\0101`}, "A"},
		{[]string{"%b", `x\cy`}, "x"},
		{[]string{"%b", `\x41`}, "A"},
		{[]string{"%b", `\q`}, "\\q"},
		{[]string{"%b", `x\`}, "x\\"},
		{[]string{"%f", "1.5"}, "1.500000"},
		{[]string{"%.2f", "1.5"}, "1.50"},
		{[]string{"%8.2f", "1.5"}, "    1.50"},
		{[]string{"%e", "1234.5"}, "1.234500e+03"},
		{[]string{"%E", "1234.5"}, "1.234500E+03"},
		{[]string{"%g", "0.0001"}, "0.0001"},
		{[]string{"%G", "100000"}, "100000"},
		{[]string{"%f", "inf"}, "inf"},
		{[]string{"%F", "nan"}, "NAN"},
		{[]string{"%d", "0x1f"}, "31"},
		{[]string{"%d", "010"}, "8"},
		{[]string{"%d", "'A"}, "65"},
		{[]string{"%d", "'"}, "0"},
		{[]string{"%f", "'A"}, "65.000000"},
		{[]string{"%d", "'AB"}, "65"},
		{[]string{"%f", "1e3"}, "1000.000000"},
	} {
		code, out, diagnostic := runPrintf(test.args...)
		if code != 0 || out != test.want {
			t.Fatalf("args=%v 得到 code=%d out=%q stderr=%q，want %q", test.args, code, out, diagnostic, test.want)
		}
	}
}

func TestFormatReuseAndErrors(t *testing.T) {
	code, out, _ := runPrintf("%s\n", "a", "b")
	if code != 0 || out != "a\nb\n" {
		t.Fatalf("格式复用得到 code=%d out=%q", code, out)
	}
	for _, args := range [][]string{{"%z"}, {"%"}, {"%5"}, {"%d", "abc"}, {"%.2d", "abc"}} {
		code, _, diagnostic := runPrintf(args...)
		if code != 1 || diagnostic == "" {
			t.Fatalf("args=%v 得到 code=%d stderr=%q", args, code, diagnostic)
		}
	}
	rc := &toolcompat.RunContext{Stdio: toolcompat.Stdio{Out: failWriter{}, Err: io.Discard}}
	if code := cmd.Run(rc, []string{"%s", "x"}); code != 1 {
		t.Fatalf("写失败应返回 1，得到 %d", code)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
