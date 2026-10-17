package awkcmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"mvdan.cc/sh/v3/interp"
)

func runAwk(t *testing.T, input string, args ...string) (string, string, int) {
	t.Helper()
	result := commandtest.Run(t, Run, t.TempDir(), args, input)
	return result.Stdout, result.Stderr, result.Code
}

func TestAwk(t *testing.T) {
	cases := []struct {
		name  string
		input string
		args  []string
		want  string
	}{
		{"field", "a b c\n", []string{`{print $2}`}, "b\n"},
		{"sum", "1\n2\n3\n", []string{`{s+=$1} END{print s}`}, "6\n"},
		{"separator", "x:y\n", []string{"-F", ":", `{print $1}`}, "x\n"},
		{"empty-separator", "abc\n", []string{"-F", "", `{print NF, $1, $2, $3}`}, "3 a b c\n"},
		{"var", "", []string{"-v", "n=5", `BEGIN{print n*2}`}, "10\n"},
		{"record", "one\ntwo\nthree\n", []string{`NR==2`}, "two\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, errb, code := runAwk(t, c.input, c.args...)
			if out != c.want || errb != "" || code != 0 {
				t.Errorf("awk %v = (%q, %q, %d), want (%q, %q, 0)", c.args, out, errb, code, c.want, "")
			}
		})
	}
}

func TestAwkAbsentArrayReferenceCreatesElement(t *testing.T) {
	program := `BEGIN { print ("missing" in a); x = a["missing"]; print ("missing" in a), x }`
	out, errb, code := runAwk(t, "", program)
	if out != "0\n1 \n" || errb != "" || code != 0 {
		t.Fatalf("awk absent array reference = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, "0\n1 \n", "")
	}
}

func TestAwkHexNumberTokenRemainsDecimalOnly(t *testing.T) {
	program := `BEGIN {
		x1 = "value-of-x1"
		a = 0x1
		print a
		x2cp3 = "value-of-x2cp3"
		a = 0+0x2cp3
		print a
		X0P12 = "value-of-X0P12"
		print 0X0P12
		XAF4 = "value-of-XAF4"
		printf("%s\n", 0XAF4)
		print "0x1" + 0
	}`
	want := "0value-of-x1\n0value-of-x2cp3\n0value-of-X0P12\n0value-of-XAF4\n1\n"
	out, errb, code := runAwk(t, "", program)
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk hexadecimal NUMBER token = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkMatchOperatorsAreLeftAssociative(t *testing.T) {
	program := `{ y = $1 !~ /Get/ ~ /1/; z = $2 ~ /a/ !~ /[0-9]/; print y; print z }`
	out, errb, code := runAwk(t, "0 1 2\n", program)
	if out != "1\n0\n" || errb != "" || code != 0 {
		t.Fatalf("awk chained match operators = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, "1\n0\n")
	}
}

func TestAwkMatchUpdatesLengthForNaNSpelling(t *testing.T) {
	out, errb, code := runAwk(t, "", `BEGIN { print match("banana", "nan"), RSTART, RLENGTH }`)
	if out != "3 3 3\n" || errb != "" || code != 0 {
		t.Fatalf("awk match NaN spelling = (%q, %q, %d), want (%q, empty, 0)", out, errb, code, "3 3 3\n")
	}
}

func TestAwkEREConstantsDecodeControlCharacterEscapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		pattern string
	}{
		{"alert", "\aentry\n", `/^[\a]/`},
		{"backspace", "\bentry\n", `/^[\b]/`},
		{"form-feed", "\fentry\n", `/^[\f]/`},
		{"carriage-return", "\rentry\n", `/^[\r]/`},
		{"tab", "\tentry\n", `/^[\t]/`},
		{"vertical-tab", "\ventry\n", `/^[\v]/`},
		{"octal", "@entry\n", `/^[\100]/`},
		{"octal-metacharacter", ".entry\n", `/^\056/`},
		{"octal-bracket-metacharacter", "-entry\n", `/^[\055]/`},
		{"backslash", "\\entry\n", `/^\\/`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errb, code := runAwk(t, tc.input, tc.pattern+` { print "matched" }`)
			if out != "matched\n" || errb != "" || code != 0 {
				t.Fatalf("awk ERE escape = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, "matched\n", "")
			}
		})
	}
}

func TestAwkRecordSeparatorReassignmentAffectsNextRecord(t *testing.T) {
	input := "one\ntwo\nleftXrightXupYdownYendZtail"
	program := `NR == 2 { RS = "X" }
NR == 4 { RS = "Y" }
NR == 6 { RS = "Z" }
{ print NR ":" $0 }`
	want := "1:one\n2:two\n3:left\n4:right\n5:up\n6:down\n7:end\n8:tail\n"
	out, errb, code := runAwk(t, input, program)
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk dynamic RS = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkInfinityAndNaNSemantics(t *testing.T) {
	out, errb, code := runAwk(t, "inf nan +inf -inf infinity\n", `{ print $1+0, $2+0, $3+0, $4+0, $5+0 }`)
	if out != "inf nan inf -inf inf\n" || errb != "" || code != 0 {
		t.Fatalf("awk special-value conversions = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, "inf nan inf -inf inf\n", "")
	}
	program := `BEGIN {
		print 1e400, -1e400
		n = "nan" + 0
		print n, (n == n)
	}`
	out, errb, code = runAwk(t, "", program)
	want := "inf -inf\nnan 0\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk overflow and NaN identity = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkPOSIXSpecialFloatFormatting(t *testing.T) {
	program := `BEGIN {
		posinf = "INF" + 0; neginf = "-INF" + 0
		posnan = "NAN" + 0; negnan = "-NAN" + 0
		printf "%e|%f|%g|%E|%G\n", posinf, posinf, posinf, posinf, posinf
		printf "%e|%f|%g|%E|%G\n", neginf, neginf, neginf, neginf, neginf
		printf "%a|%e|%f|%g|%A|%E|%F|%G\n", posnan, posnan, posnan, posnan, posnan, posnan, posnan, posnan
		printf "%a|%e|%f|%g|%A|%E|%F|%G\n", negnan, negnan, negnan, negnan, negnan, negnan, negnan, negnan
		printf "<%6a><%6A><%6F><%-06a><%-06A><%-06F>\n", negnan, negnan, negnan, negnan, negnan, negnan
		printf "<%010e><%010f><%010g><%010E><%010G>\n", posinf, posinf, posinf, posinf, posinf
		printf "<%010e><%010f><%010g><%010E><%010G>\n", neginf, neginf, neginf, neginf, neginf
		print negnan
	}`
	want := "inf|inf|inf|INF|INF\n" +
		"-inf|-inf|-inf|-INF|-INF\n" +
		"nan|nan|nan|nan|NAN|NAN|NAN|NAN\n" +
		"-nan|-nan|-nan|-nan|-NAN|-NAN|-NAN|-NAN\n" +
		"<  -nan><  -NAN><  -NAN><-nan  ><-NAN  ><-NAN  >\n" +
		"<       inf><       inf><       inf><       INF><       INF>\n" +
		"<      -inf><      -inf><      -inf><      -INF><      -INF>\n" +
		"-nan\n"
	out, errb, code := runAwk(t, "", program)
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk POSIX special float formatting = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkDivisionByZeroIsADiagnosedError(t *testing.T) {
	out, errb, code := runAwk(t, "", `BEGIN { print 1/0 }`)
	if code == 0 || !strings.Contains(errb, "division by zero") || out != "" {
		t.Fatalf("awk division by zero = (%q, %q, %d), want empty stdout, a diagnostic, and a nonzero status", out, errb, code)
	}
}

func TestAwkPOSIXFloatFormats(t *testing.T) {
	program := `BEGIN {
		printf "<%a><%A><%a>\n", 0, 0.125, -0.125
		printf "<%#.0a><%.2A>\n", 0.125, 0.125
		printf "<%015.6a><%+015.6a><% 015.6a><%-15.6a>\n", 0.125, 0.125, 0.125, 0.125
		printf "<%F><%.2F><%#.0F><%010F><%+F><% F>\n", 0.125, 0.125, 1, 0.125, 0.125, 0.125
		print sprintf("<%*.*a><%*.*F><%*.*a>", 12, 3, 0.125, 8, 2, -0.125, -12, -1, 0.125)
		printf "<%.*a><%.*F><%.*f>\n", -0.5, 0.1, -0.5, 0.125, -0.5, 0.125
		printf "<%g><%G><%#g><%.*g>\n", 4.323232245, 0.00004323232245, 4.3, -1, 4.323232245
		print sprintf("<%12g><%.*g><%.3G><%.0g>", 4.323232245, 3, 4.323232245, 4.323232245, 4.323232245)
		OFMT = "%g"; print 4.323232245
		OFMT = "%G"; print 0.00004323232245
		CONVFMT = "%g"; print 4.323232245 ""
		CONVFMT = "%G"; print 0.00004323232245 ""
	}`
	want := "<0x0p+0><0X1P-3><-0x1p-3>\n" +
		"<0x1.p-3><0X1.00P-3>\n" +
		"<0x001.000000p-3><+0x01.000000p-3>< 0x01.000000p-3><0x1.000000p-3  >\n" +
		"<0.125000><0.12><1.><000.125000><+0.125000>< 0.125000>\n" +
		"<  0x1.000p-3><   -0.12><0x1p-3      >\n" +
		"<0x2p-4><0><0>\n" +
		"<4.32323><4.32323E-05><4.30000><4.32323>\n" +
		"<     4.32323><4.32><4.32><4>\n" +
		"4.32323\n4.32323E-05\n4.32323\n4.32323E-05\n"

	out, errb, code := runAwk(t, "", program)
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk POSIX float formats = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkPOSIXOctalAlternateFormZeroPrecision(t *testing.T) {
	program := `BEGIN {
		printf "<%#.o><%#.0o><%#.00o><%#.*o><%.0o>\n", 0, 0, 0, 0, 0, 0
		print sprintf("<%#5.0o><%-#5.0o><%#.1o><%#.0o>", 0, 0, 0, 7)
	}`
	want := "<0><0><0><0><>\n<    0><0    ><0><07>\n"

	out, errb, code := runAwk(t, "", program)
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk POSIX octal alternate form = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkPOSIXEREBackendExpressions(t *testing.T) {
	input := strings.Repeat("a", 252) + "\n" +
		strings.Repeat("b", 253) + "\n" +
		strings.Repeat("c", 254) + "\n" +
		strings.Repeat("d", 255) + "\n"
	program := `/^a{252}$/ { print "literal" }
$0 ~ "^b{253}$" { print "dynamic-match" }
$0 !~ "^z{254}$" && /^c/ { print "dynamic-not-match" }
match($0, "d{255}") { print "match", RSTART, RLENGTH }`

	out, errb, code := runAwk(t, input, program)
	want := "literal\ndynamic-match\ndynamic-not-match\nmatch 1 255\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk ERE expressions = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkPOSIXEREDotNewlineAndLeftmostLongest(t *testing.T) {
	program := `BEGIN {
		print ("a\nb" ~ /a.b/), ("a\nb" ~ "a.b")
		print match("ab", "a|ab"), RSTART, RLENGTH
	}`
	out, errb, code := runAwk(t, "", program)
	want := "1 1\n1 1 2\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk ERE semantics = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkPOSIXEREOpenIntervalMatches(t *testing.T) {
	program := `BEGIN {
		print match("aabcaab", /([a-c]*){0,}/), RSTART, RLENGTH
		print match("abababccccccd", /(ab){2,}/), RSTART, RLENGTH
		print match("abababccccccd", /(ab){4,}/), RSTART, RLENGTH
	}`
	out, errb, code := runAwk(t, "", program)
	want := "1 1 7\n1 1 6\n0 0 -1\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk open interval EREs = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkPOSIXERERejectsAboveAdvertisedLimit(t *testing.T) {
	program := `BEGIN { print match("", /c{256,}/) }`
	_, errb, code := runAwk(t, "", program)
	if code == 0 || errb == "" {
		t.Fatalf("awk above-limit interval = (err %q, code %d), want diagnostic failure", errb, code)
	}
}

func TestAwkEREBackendLeavesRecordAndSubstitutionRegexesUnchanged(t *testing.T) {
	program := `BEGIN { FS = "[,:]"; RS = ";+" }
{
		n = split("a,b:c", parts, "[,:]")
		s = "aaabbb"
		sub("a+", "x", s)
		gsub("b+", "y", s)
		print NF, n, s
}`
	out, errb, code := runAwk(t, "one:two;;", program)
	want := "2 3 xy\n"
	if out != want || errb != "" || code != 0 {
		t.Fatalf("awk FS/RS/split/sub/gsub = (%q, %q, %d), want (%q, %q, 0)", out, errb, code, want, "")
	}
}

func TestAwkEREAdapterPreservesPatternSource(t *testing.T) {
	source := `^([[:alpha:]]|\\.){00255}$`
	re, err := (awkERECompiler{}).Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.String(); got != source {
		t.Fatalf("Regexp.String() = %q, want exact source %q", got, source)
	}
}

func TestAwkProgramFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "print2.awk"), []byte("{print $2}\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-f", "print2.awk"}, "a b c\n")
	if result.Stdout != "b\n" || result.Stderr != "" || result.Code != 0 {
		t.Errorf("awk -f = (%q, %q, %d), want (%q, %q, 0)", result.Stdout, result.Stderr, result.Code, "b\n", "")
	}
}

func TestAwkPOSIXInterfaceProgramFileAndAssignments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first.awk"), []byte(`BEGIN { print prefix }`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "second.awk"), []byte(`{ print tag ":" $1 }`), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "input"), []byte("row\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"-v", "prefix=pre", "-f", "first.awk", "-f", "second.awk", "tag=late", "input"}, "")
	if result.Code != 0 || result.Stderr != "" || result.Stdout != "pre\nlate:row\n" {
		t.Fatalf("awk -v/-f/file assignments = (%q, %q, %d), want POSIX ordering", result.Stdout, result.Stderr, result.Code)
	}
}

func TestAwkPOSIXProgramFromStdinAndEmptyProgram(t *testing.T) {
	if out, errb, code := runAwk(t, `{ print $1 }`, "-f", "-"); code != 0 || errb != "" || out != "" {
		t.Fatalf("awk -f - should read program source only, got (%q, %q, %d)", out, errb, code)
	}
	if out, errb, code := runAwk(t, "", ""); code != 0 || out != "" || errb != "" {
		t.Fatalf("empty awk program = (%q, %q, %d), want no output and status 0", out, errb, code)
	}
}

func TestAwkARGV0FallsBackToCommandName(t *testing.T) {
	out, errb, code := runAwk(t, "", `BEGIN { print ARGV[0] }`)
	if code != 0 || errb != "" || out != "awk\n" {
		t.Fatalf("awk ARGV[0] = (%q, %q, %d), want fallback command name", out, errb, code)
	}
}

func TestAwkInvalidAssignmentAndPOSIXMissingInput(t *testing.T) {
	if _, errb, code := runAwk(t, "", "-v", "1bad=x", `BEGIN { print "bad" }`); code != 2 || !strings.Contains(errb, "invalid -v assignment") {
		t.Fatalf("invalid -v assignment = (%q, %d), want usage error", errb, code)
	}
	if out, errb, code := runAwk(t, "", `END { print "must-not-run" }`, "missing-input"); code == 0 || !strings.Contains(errb, "missing-input") || out != "" {
		t.Fatalf("missing input = (%q, %q, %d), want fatal diagnostic without END action", out, errb, code)
	}
}

func TestAwkOutputErrorsAndExplicitExitStatus(t *testing.T) {
	var errb strings.Builder
	err := commandtest.Invoke(t.Context(), Run, t.TempDir(), []string{`BEGIN { print "data" }`}, strings.NewReader(""), commandtest.FailWriter{}, &errb)
	status, ok := errors.AsType[interp.ExitStatus](err)
	if !ok || int(status) != 2 || errb.Len() == 0 {
		t.Fatalf("output error = (%q, %v), want diagnostic and status 2", errb.String(), err)
	}

	out, stderr, code := runAwk(t, "", `BEGIN { exit 7 }`)
	if code != 7 || out != "" || stderr != "" {
		t.Fatalf("explicit exit = (%q,%q,%d), want (empty,empty,7)", out, stderr, code)
	}
}

func TestResolveFilesPreservesOperandSpelling(t *testing.T) {
	files := []string{" 123456789 ", "1.234", "+12345", "-12345", "x=1", "-"}
	if got := resolveFiles(files); strings.Join(got, "\x00") != strings.Join(files, "\x00") {
		t.Fatalf("resolveFiles() = %q, want original POSIX-visible operands %q", got, files)
	}
}

func TestAwkFileOperandUsesGuestVFS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input"), []byte("record\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{`{ print ARGV[1], FILENAME, $0 }`, "input"}, "")
	if result.Code != 0 || result.Stderr != "" || result.Stdout != "input input record\n" {
		t.Fatalf("file operand = (%q,%q,%d), want preserved visible filename", result.Stdout, result.Stderr, result.Code)
	}
}

func TestAwkHostAccessIsNarrowed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input"), []byte("from-guest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		program string
	}{
		{"getline-read", `BEGIN { getline line < "input" }`},
		{"print-write", `BEGIN { print "x" > "output" }`},
		{"pipe", `BEGIN { "printf x" | getline line }`},
		{"system", `BEGIN { system("printf x") }`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := commandtest.Run(t, Run, dir, []string{c.program}, "")
			if result.Code == 0 || result.Stderr == "" {
				t.Fatalf("宿主访问 %s = (%q,%q,%d), want explicit failure", c.name, result.Stdout, result.Stderr, result.Code)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "output")); !os.IsNotExist(err) {
		t.Fatalf("重定向不应在客体之外产生文件: %v", err)
	}
}

func TestAwkUnsupportedLocaleFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want int
	}{
		{"numeric-foreign", []string{"LC_ALL=de_DE.ISO-8859-1"}, 2},
		{"numeric-foreign-category", []string{"LANG=C", "LC_NUMERIC=fr_FR.iso8859-1"}, 2},
		{"utf8-served-as-bytes", []string{"LANG=C", "LC_CTYPE=en_US.UTF-8"}, 0},
		{"utf8-collate", []string{"LANG=C", "LC_COLLATE=fr_FR.UTF-8"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb strings.Builder
			rc := &toolcompat.RunContext{
				Ctx: t.Context(), Dir: t.TempDir(), Env: tc.env,
				Stdio: toolcompat.Stdio{In: strings.NewReader(""), Out: &out, Err: &errb},
			}
			code := cmd.Run(rc, []string{`BEGIN { print 1 }`})
			if code != tc.want {
				t.Fatalf("环境 %v 得到 (%q,%q,%d), want 退出码 %d", tc.env, out.String(), errb.String(), code, tc.want)
			}
			if tc.want == 2 && (errb.Len() == 0 || out.Len() != 0) {
				t.Fatalf("环境 %v want 诊断且无 stdout, 得到 (%q,%q)", tc.env, out.String(), errb.String())
			}
		})
	}
	if point, ok := awkNumericDecimalPoint("POSIX"); !ok || point != '.' {
		t.Fatalf("POSIX 小数点 = (%q,%v), want '.'", point, ok)
	}
	if point, ok := awkNumericDecimalPoint("fr_FR.iso8859-1"); ok || point != 0 {
		t.Fatalf("未支持语言环境应报不支持，得到 (%q,%v)", point, ok)
	}
}

func TestAwkCaseMappingAndOptionEscapes(t *testing.T) {
	out, errb, code := runAwk(t, "", `BEGIN { print toupper("aBc1"), tolower("AbC1") }`)
	if out != "ABC1 abc1\n" || errb != "" || code != 0 {
		t.Fatalf("awk case mapping = (%q,%q,%d), want C 语言环境映射", out, errb, code)
	}
	out, errb, code = runAwk(t, "a\tb\n", "-F", `\t`, `{ print $2 }`)
	if out != "b\n" || errb != "" || code != 0 {
		t.Fatalf("awk -F 转义 = (%q,%q,%d), want 制表符分隔", out, errb, code)
	}
	out, errb, code = runAwk(t, "", "-v", `s=a\tb`, `BEGIN { print s }`)
	if out != "a\tb\n" || errb != "" || code != 0 {
		t.Fatalf("awk -v 转义 = (%q,%q,%d), want 制表符", out, errb, code)
	}
	if got := unescape(`\101`); got != "A" {
		t.Fatalf("八进制转义应解码, 得到 %q", got)
	}
}

func TestAwkEnvironmentAndReaderHelpers(t *testing.T) {
	if got := environPairs([]string{"A=1", "BROKEN", "B=2"}); strings.Join(got, ",") != "A,1,B,2" {
		t.Fatalf("environPairs = %v", got)
	}
	if got := readerOrEmpty(nil); got == nil {
		t.Fatal("nil 输入应有空流兜底")
	}
}

func TestGuestAdapter(t *testing.T) {
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}
