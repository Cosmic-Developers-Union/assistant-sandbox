package ddcmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestConvTablesAsciiEbcdicArePermutationsAndInverses(t *testing.T) {
	isPermutation := func(name string, table [256]byte) {
		t.Helper()
		var seen [256]bool
		for _, b := range table {
			if seen[b] {
				t.Fatalf("%s: byte 0x%02x appears more than once; table is not a bijection", name, b)
			}
			seen[b] = true
		}
	}
	isPermutation("e2aPOSIX", e2aPOSIX)
	isPermutation("a2ePOSIX", a2ePOSIX)

	for i := range 256 {
		if got := e2aPOSIX[a2ePOSIX[i]]; got != byte(i) {
			t.Fatalf("round trip ascii->ebcdic->ascii for 0x%02x: got 0x%02x", i, got)
		}
	}
}

func TestConvTablesIbmTableIsIntentionallyNotBijective(t *testing.T) {
	collisions := map[byte][2]byte{
		0xad: {'[', 0xd5},
		0xbd: {']', 0xe5},
	}
	for target, sources := range collisions {
		for _, src := range sources {
			if got := a2ibmPOSIX[src]; got != target {
				t.Errorf("a2ibmPOSIX[0x%02x] = 0x%02x, want 0x%02x", src, got, target)
			}
		}
	}
}

func TestConvTablesIbmDiffersOnlyAtDocumentedPoints(t *testing.T) {
	want := map[byte]bool{'^': true, '~': true, 0xcb: true, 0xd5: true, 0xe5: true}
	for i := range 256 {
		differs := a2ePOSIX[i] != a2ibmPOSIX[i]
		if differs != want[byte(i)] {
			t.Errorf("codepoint 0x%02x: a2e=0x%02x a2ibm=0x%02x differs=%v want=%v",
				i, a2ePOSIX[i], a2ibmPOSIX[i], differs, want[byte(i)])
		}
	}
}

func TestConvTablesKnownAnchors(t *testing.T) {
	cases := []struct {
		name      string
		ascii     byte
		ebcdic    byte
		ebcdicIBM byte
	}{
		{name: "space", ascii: ' ', ebcdic: 0x40},
		{name: "digit 0", ascii: '0', ebcdic: 0xf0},
		{name: "digit 9", ascii: '9', ebcdic: 0xf9},
		{name: "upper A", ascii: 'A', ebcdic: 0xc1},
		{name: "upper Z", ascii: 'Z', ebcdic: 0xe9},
		{name: "lower a", ascii: 'a', ebcdic: 0x81},
		{name: "lower z", ascii: 'z', ebcdic: 0xa9},
		{name: "caret", ascii: '^', ebcdic: 0x9a, ebcdicIBM: 0x5f},
		{name: "tilde", ascii: '~', ebcdic: 0x5f, ebcdicIBM: 0xa1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a2ePOSIX[tc.ascii]; got != tc.ebcdic {
				t.Errorf("a2ePOSIX[%q] = 0x%02x, want 0x%02x", tc.ascii, got, tc.ebcdic)
			}
			if got := e2aPOSIX[tc.ebcdic]; got != tc.ascii {
				t.Errorf("e2aPOSIX[0x%02x] = %q, want %q", tc.ebcdic, got, tc.ascii)
			}
			if tc.ebcdicIBM != 0 {
				if got := a2ibmPOSIX[tc.ascii]; got != tc.ebcdicIBM {
					t.Errorf("a2ibmPOSIX[%q] = 0x%02x, want 0x%02x", tc.ascii, got, tc.ebcdicIBM)
				}
			}
		})
	}
}

func TestDdConvAsciiEbcdicIbmRequireCbs(t *testing.T) {
	for _, conv := range []string{"ascii", "ebcdic", "ibm"} {
		t.Run(conv, func(t *testing.T) {
			_, errb, code := runTool(t, t.TempDir(), "", "conv="+conv, "status=none")
			if code != 2 || !strings.Contains(errb, "cbs=") {
				t.Fatalf("conv=%s without cbs=: code=%d err=%q, want usage error naming cbs=", conv, code, errb)
			}
		})
	}
}

func TestDdConvMutualExclusion(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"ascii+ebcdic", []string{"conv=ascii,ebcdic", "cbs=4"}},
		{"ascii+ibm", []string{"conv=ascii,ibm", "cbs=4"}},
		{"ebcdic+ibm", []string{"conv=ebcdic,ibm", "cbs=4"}},
		{"ascii+block", []string{"conv=ascii,block", "cbs=4"}},
		{"ebcdic+unblock", []string{"conv=ebcdic,unblock", "cbs=4"}},
		{"ibm+unblock", []string{"conv=ibm,unblock", "cbs=4"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "status=none")
			_, errb, code := runTool(t, t.TempDir(), "", args...)
			if code != 2 || !strings.Contains(errb, "mutually exclusive") {
				t.Fatalf("args=%v: code=%d err=%q, want mutually-exclusive usage error", tc.args, code, errb)
			}
		})
	}
}

func TestDdConvRedundantMatchingBlockUnblockAccepted(t *testing.T) {
	implicitOut, implicitErr, implicitCode := runTool(t, t.TempDir(), "HI\n", "conv=ebcdic", "cbs=4", "status=none")
	explicitOut, explicitErr, explicitCode := runTool(t, t.TempDir(), "HI\n", "conv=ebcdic,block", "cbs=4", "status=none")
	if implicitCode != 0 || explicitCode != 0 {
		t.Fatalf("code implicit=%d explicit=%d err=%q/%q", implicitCode, explicitCode, implicitErr, explicitErr)
	}
	if implicitOut != explicitOut {
		t.Fatalf("implicit conv=ebcdic output %q != explicit conv=ebcdic,block output %q", implicitOut, explicitOut)
	}

	ebcdicH := string(a2ePOSIX['H'])
	implicitOut, implicitErr, implicitCode = runTool(t, t.TempDir(), ebcdicH, "conv=ascii", "cbs=4", "status=none")
	explicitOut, explicitErr, explicitCode = runTool(t, t.TempDir(), ebcdicH, "conv=ascii,unblock", "cbs=4", "status=none")
	if implicitCode != 0 || explicitCode != 0 {
		t.Fatalf("code implicit=%d explicit=%d err=%q/%q", implicitCode, explicitCode, implicitErr, explicitErr)
	}
	if implicitOut != explicitOut {
		t.Fatalf("implicit conv=ascii output %q != explicit conv=ascii,unblock output %q", implicitOut, explicitOut)
	}
}

func TestDdConvAsciiTranslatesThenUnblocks(t *testing.T) {

	in := []byte{a2ePOSIX['H'], a2ePOSIX['I'], ebcdicSpace, ebcdicSpace}
	out, errb, code := runTool(t, t.TempDir(), string(in), "cbs=4", "conv=ascii", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "HI\n" {
		t.Fatalf("out=%q want %q", out, "HI\n")
	}
}

func TestDdConvEbcdicBlocksThenTranslates(t *testing.T) {
	out, errb, code := runTool(t, t.TempDir(), "HI\n", "cbs=4", "conv=ebcdic", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	want := []byte{a2ePOSIX['H'], a2ePOSIX['I'], a2ePOSIX[' '], a2ePOSIX[' ']}
	if !bytes.Equal([]byte(out), want) {
		t.Fatalf("out=%v want=%v", []byte(out), want)
	}
}

func TestDdConvIbmUsesIbmTableNotPlainEbcdic(t *testing.T) {
	out, errb, code := runTool(t, t.TempDir(), "^\n", "cbs=4", "conv=ibm", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	got := []byte(out)[0]
	if got != a2ibmPOSIX['^'] {
		t.Fatalf("conv=ibm translated '^' to 0x%02x, want a2ibmPOSIX value 0x%02x", got, a2ibmPOSIX['^'])
	}
	if got == a2ePOSIX['^'] {
		t.Fatalf("conv=ibm produced the plain EBCDIC value 0x%02x; ibm and ebcdic must diverge at '^'", a2ePOSIX['^'])
	}
}

func TestDdConvEbcdicTruncatesOverlongLine(t *testing.T) {
	out, errb, code := runTool(t, t.TempDir(), "ABCDE\n", "cbs=3", "conv=ebcdic", "status=noxfer")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	want := []byte{a2ePOSIX['A'], a2ePOSIX['B'], a2ePOSIX['C']}
	if !bytes.Equal([]byte(out), want) {
		t.Fatalf("out=%v want=%v", []byte(out), want)
	}
	if !strings.Contains(errb, "1 truncated record") {
		t.Fatalf("stderr=%q want mention of 1 truncated record", errb)
	}
}

func TestDdConvAsciiEbcdicRoundTrip(t *testing.T) {
	original := "AB\nCD\nE\n"
	ebcdic, errb, code := runTool(t, t.TempDir(), original, "cbs=4", "conv=ebcdic", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("ebcdic pass: code=%d err=%q", code, errb)
	}
	back, errb, code := runTool(t, t.TempDir(), ebcdic, "cbs=4", "conv=ascii", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("ascii pass: code=%d err=%q", code, errb)
	}
	if back != original {
		t.Fatalf("round trip = %q, want %q", back, original)
	}
}

func TestDdConvAsciiThenLcaseAppliesToTranslatedResult(t *testing.T) {

	in := []byte{a2ePOSIX['H'], ebcdicSpace, ebcdicSpace, ebcdicSpace}
	out, errb, code := runTool(t, t.TempDir(), string(in), "cbs=4", "conv=ascii,lcase", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	if out != "h\n" {
		t.Fatalf("out=%q want %q", out, "h\n")
	}
}

func TestDdConvUcaseThenEbcdicAppliesToAsciiBeforeTranslate(t *testing.T) {

	out, errb, code := runTool(t, t.TempDir(), "hi\n", "cbs=4", "conv=ebcdic,ucase", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	want := []byte{a2ePOSIX['H'], a2ePOSIX['I'], a2ePOSIX[' '], a2ePOSIX[' ']}
	if !bytes.Equal([]byte(out), want) {
		t.Fatalf("out=%v want=%v (uppercased then translated)", []byte(out), want)
	}
}

func TestDdConvSyncAsciiTranslatesLiteralSpacePadding(t *testing.T) {

	in := string([]byte{a2ePOSIX['H']})
	out, errb, code := runTool(t, t.TempDir(), in, "ibs=4", "cbs=4", "conv=ascii,sync", "status=none")
	if code != 0 || errb != "" {
		t.Fatalf("code=%d err=%q", code, errb)
	}
	want := []byte{'H', e2aPOSIX[' '], e2aPOSIX[' '], e2aPOSIX[' '], '\n'}
	if !bytes.Equal([]byte(out), want) {
		t.Fatalf("out=%v want=%v (literal sync padding must be translated)", []byte(out), want)
	}
}
