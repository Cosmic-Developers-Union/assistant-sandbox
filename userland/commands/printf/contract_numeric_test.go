package printfcmd

import (
	"bytes"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

func runPrintfEnv(env []string, args ...string) (int, string, string) {
	var out, diagnostic bytes.Buffer
	rc := &toolcompat.RunContext{Env: env, Stdio: toolcompat.Stdio{Out: &out, Err: &diagnostic}}
	code := cmd.Run(rc, args)
	return code, out.String(), diagnostic.String()
}

func TestHexFloatAndNonFinite(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"%a", "1"}, "0x1p+0"},
		{[]string{"%A", "1"}, "0X1P+0"},
		{[]string{"%a", "0"}, "0x0p+0"},
		{[]string{"%10a", "1"}, "    0x1p+0"},
		{[]string{"%010a", "1"}, "0x00001p+0"},
		{[]string{"%-10a", "1"}, "0x1p+0    "},
		{[]string{"%.1a", "1.5"}, "0x1.8p+0"},
		{[]string{"%f", "0x1p4"}, "16.000000"},
		{[]string{"%f", "0x4d2"}, "1234.000000"},
		{[]string{"%f", "INFINITY"}, "inf"},
		{[]string{"%f", "-nan"}, "-nan"},
		{[]string{"%f", "nan(abc)"}, "nan"},
		{[]string{"%f", "  1.5"}, "1.500000"},
		{[]string{"%f", "inf"}, "inf"},
		{[]string{"%8f", "inf"}, "     inf"},
		{[]string{"%-8f", "inf"}, "inf     "},
		{[]string{"%+f", "inf"}, "+inf"},
		{[]string{"% f", "inf"}, " inf"},
		{[]string{"%f", "-inf"}, "-inf"},
		{[]string{"%F", "inf"}, "INF"},
		{[]string{"%A", "inf"}, "INF"},
		{[]string{"%g", "nan"}, "nan"},
	} {
		code, out, diagnostic := runPrintf(test.args...)
		if code != 0 || out != test.want {
			t.Fatalf("args=%v 得到 code=%d out=%q stderr=%q，want %q", test.args, code, out, diagnostic, test.want)
		}
	}
}

func TestNumericDiagnostics(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"%f", "infin"}, "inf"},
		{[]string{"%f", "1.5e"}, "1.500000"},
		{[]string{"%d", "99999999999999999999"}, "9223372036854775807"},
		{[]string{"%d", "-99999999999999999999"}, "-9223372036854775808"},
		{[]string{"%u", "abc"}, "0"},
		{[]string{"%f", "1e999"}, "inf"},
	} {
		code, out, diagnostic := runPrintf(test.args...)
		if code != 1 || diagnostic == "" {
			t.Fatalf("args=%v 得到 code=%d stderr=%q", test.args, code, diagnostic)
		}
		if out != test.want {
			t.Fatalf("args=%v 得到 out=%q，want %q", test.args, out, test.want)
		}
	}
}

func TestUnsignedAndRadix(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"%u", "-1"}, "18446744073709551615"},
		{[]string{"%x", "0x1f"}, "1f"},
		{[]string{"%u", "0x1f"}, "31"},
		{[]string{"%o", "0x10"}, "20"},
		{[]string{"%x", "0"}, "0"},
	} {
		code, out, diagnostic := runPrintf(test.args...)
		if code != 0 || out != test.want {
			t.Fatalf("args=%v 得到 code=%d out=%q stderr=%q，want %q", test.args, code, out, diagnostic, test.want)
		}
	}
}

func TestFieldWidthForms(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"%-5c", "a"}, "a    "},
		{[]string{"%.0s", "abc"}, ""},
		{[]string{"%.2b", "abcd"}, "ab"},
		{[]string{"%5b", "ab"}, "   ab"},
		{[]string{"%*s", "4", "ab"}, "  ab"},
		{[]string{"%.*s", "1", "ab"}, "a"},
		{[]string{"%s%s", "a"}, "a"},
	} {
		code, out, diagnostic := runPrintf(test.args...)
		if code != 0 || out != test.want {
			t.Fatalf("args=%v 得到 code=%d out=%q stderr=%q，want %q", test.args, code, out, diagnostic, test.want)
		}
	}
}

func TestNumericRadixLocale(t *testing.T) {
	code, out, _ := runPrintfEnv([]string{"LC_ALL=de_DE.UTF-8"}, "%f", "1,5")
	if code != 0 || out != "1,500000" {
		t.Fatalf("得到 code=%d out=%q", code, out)
	}
	code, out, _ = runPrintfEnv([]string{"LC_ALL=C", "LC_NUMERIC=de_DE"}, "%a", "1")
	if code != 0 || out != "0x1p+0" {
		t.Fatalf("LC_ALL 优先: code=%d out=%q", code, out)
	}
	code, out, _ = runPrintfEnv([]string{"LANG=de_DE.UTF-8"}, "%g", "1,5")
	if code != 0 || out != "1,5" {
		t.Fatalf("LANG 兜底: code=%d out=%q", code, out)
	}
}
