package bre

import (
	"fmt"
	"strings"
	"testing"
)

const awkFlagPrefix = "(?s)"

func compileERE(t *testing.T, pattern string) *Regexp {
	t.Helper()
	re, err := CompileEREWithFlags(pattern, awkFlagPrefix)
	if err != nil {
		t.Fatalf("CompileEREWithFlags(%q, %q): %v", pattern, awkFlagPrefix, err)
	}
	re.Longest()
	return re
}

func matchLen(re *Regexp, in string) int {
	loc := re.FindStringIndex(in)
	if loc == nil {
		return -1
	}
	return loc[1] - loc[0]
}

func TestAwkERELeadingZeroExactThrough255(t *testing.T) {
	for n := 0; n <= 255; n++ {
		padded := fmt.Sprintf(`a{%04d}`, n)
		canonical := fmt.Sprintf(`a{%d}`, n)
		paddedRe := compileERE(t, padded)
		canonRe := compileERE(t, canonical)

		probes := []int{0, 1}
		if n > 0 {
			probes = append(probes, n-1)
		}
		probes = append(probes, n, n+1)
		if n > 0 && 2*n <= 510 {
			probes = append(probes, 2*n)
		}
		for _, len := range probes {
			if len < 0 {
				continue
			}
			in := strings.Repeat("a", len)
			if got, want := paddedRe.MatchString(in), canonRe.MatchString(in); got != want {
				t.Errorf("leading-zero exact n=%d len=%d: %q match=%v, canonical %q match=%v",
					n, len, padded, got, canonical, want)
			}
		}
	}
}

func TestAwkERELeadingZeroUnboundedThrough255(t *testing.T) {
	for n := 0; n <= 255; n++ {
		padded := fmt.Sprintf(`a{%04d,}`, n)
		canonical := fmt.Sprintf(`a{%d,}`, n)
		paddedRe := compileERE(t, padded)
		canonRe := compileERE(t, canonical)
		for _, len := range []int{0, n, n + 1} {
			if len < 0 {
				continue
			}
			in := strings.Repeat("a", len)
			if got, want := paddedRe.MatchString(in), canonRe.MatchString(in); got != want {
				t.Errorf("leading-zero unbounded n=%d len=%d: %q match=%v, canonical %q match=%v",
					n, len, padded, got, canonical, want)
			}
		}
	}
}

func TestAwkERELeadingZeroRangeParity(t *testing.T) {
	bounds := []int{0, 1, 2, 7, 15, 63, 127, 200, 255}
	for _, lo := range bounds {
		for _, hi := range bounds {
			if hi < lo {
				continue
			}
			padded := fmt.Sprintf(`a{%04d,%04d}`, lo, hi)
			canonical := fmt.Sprintf(`a{%d,%d}`, lo, hi)
			paddedRe := compileERE(t, padded)
			canonRe := compileERE(t, canonical)
			probes := []int{0, lo, hi, lo + (hi-lo)/2, hi + 1}
			for _, len := range probes {
				if len < 0 || len > 510 {
					continue
				}
				in := strings.Repeat("a", len)
				if got, want := paddedRe.MatchString(in), canonRe.MatchString(in); got != want {
					t.Errorf("leading-zero range [%d,%d] len=%d: %q match=%v, canonical %q match=%v",
						lo, hi, len, padded, got, canonical, want)
				}
			}
		}
	}
}

func TestAwkEREAdjacentQuantifiersRejected(t *testing.T) {
	patterns := []string{

		`a{2}{3}`,
		`a{1}{5}`,
		`a*{2}`,
		`a+{2}`,
		`a?{2}`,

		`a{200}{200}`,
		`a{20}{20}`,

		`a{2}*`,
		`a{2}+`,
	}
	for _, p := range patterns {
		if _, err := CompileEREWithFlags(p, awkFlagPrefix); err == nil {
			t.Errorf("CompileEREWithFlags(%q) succeeded; want nested-repetition error", p)
		}
	}
}

func TestAwkERELazySuffixCardinality(t *testing.T) {
	pairs := []struct {
		greedy, lazy string
		in           string
		wantLen      int
	}{
		{`a{2}`, `a{2}?`, "aaaa", 2},
		{`a{2,4}`, `a{2,4}?`, "aaaaa", 4},
		{`a+`, `a+?`, "aaa", 3},
		{`a?`, `a??`, "aaa", 1},
		{`a{0,3}`, `a{0,3}?`, "aaaa", 3},
	}
	for _, c := range pairs {
		greedyRe := compileERE(t, c.greedy)
		lazyRe := compileERE(t, c.lazy)
		if got := matchLen(greedyRe, c.in); got != c.wantLen {
			t.Errorf("greedy %q on %q: match length %d, want %d", c.greedy, c.in, got, c.wantLen)
		}
		if got := matchLen(lazyRe, c.in); got != c.wantLen {
			t.Errorf("lazy %q on %q: match length %d, want %d (Longest overrides lazy)", c.lazy, c.in, got, c.wantLen)
		}
	}
}

func TestAwkEREBracketAtoms(t *testing.T) {
	cases := []struct {
		pattern string
		in      string
		want    bool
	}{
		{`[[:alpha:]]`, "a", true},
		{`[[:alpha:]]`, "9", false},
		{`[[:digit:]]`, "7", true},
		{`[[:digit:]]`, "x", false},
		{`[[:upper:]]`, "A", true},
		{`[[:lower:]]`, "a", true},
		{`[[:alnum:]]+`, "abc123", true},
		{`[[:space:]]`, " ", true},
		{`[[:punct:]]`, "!", true},
		{`[a-z[:digit:]]`, "5", true},
		{`[a-z[:digit:]]`, "_", false},
	}
	for _, c := range cases {
		re := compileERE(t, c.pattern)
		if got := re.MatchString(c.in); got != c.want {
			t.Errorf("%q.MatchString(%q) = %v, want %v", c.pattern, c.in, got, c.want)
		}
	}
}

func TestAwkEREEscapedBrackets(t *testing.T) {

	re := compileERE(t, `[]a]`)
	if !re.MatchString("]") {
		t.Errorf(`[]a] should match ]`)
	}
	if re.MatchString("b") {
		t.Errorf(`[]a] should not match b`)
	}

	re2 := compileERE(t, `[^]a]`)
	if !re2.MatchString("b") {
		t.Errorf(`[^]a] should match b`)
	}
	if re2.MatchString("]") {
		t.Errorf(`[^]a] should not match ]`)
	}

	re3 := compileERE(t, `[a\]x]`)
	if !re3.MatchString("ax]") {
		t.Errorf(`[a\]x] should match "ax]" (class {a,\} then literal "x]")`)
	}
	if re3.MatchString("ax") {
		t.Errorf(`[a\]x] should not match "ax" (missing literal "]")`)
	}
}

func TestAwkEREUnclosedClass(t *testing.T) {
	patterns := []string{
		`[a-`,
		`[`,
		`[abc`,
		`[[:alpha:`,
		`[^abc`,
	}
	for _, p := range patterns {
		if _, err := CompileEREWithFlags(p, awkFlagPrefix); err == nil {
			t.Errorf("CompileEREWithFlags(%q) succeeded; want unmatched-bracket error", p)
		}
	}
}

func TestAwkEREUTF8Atoms(t *testing.T) {
	cases := []struct {
		pattern string
		in      string
		want    bool
	}{
		{`^é{2}$`, "éé", true},
		{`^é{2}$`, "é", false},
		{`^é{2,3}$`, "ééé", true},
		{`^π{3}$`, "πππ", true},
		{`^π{3}$`, "ππ", false},
		{`^(日本){2}$`, "日本日本", true},
		{`^日本{2}$`, "日本本", true},
		{`^café+$`, "caféé", true},
	}
	for _, c := range cases {
		re := compileERE(t, c.pattern)
		if got := re.MatchString(c.in); got != c.want {
			t.Errorf("%q.MatchString(%q) = %v, want %v", c.pattern, c.in, got, c.want)
		}
	}
}

func TestAwkEREMalformedQuantifiersFailClosed(t *testing.T) {
	reject := []string{

		`a**`, `a++`, `a{2}??`, `a{2}*`, `a{2}+`, `a{2}{3}`,

		`*a`, `+a`, `?a`, `{2}`, `*`, `**`,

		`a{`, `a{}`, `a{,}`, `a{x}`, `a{2,3,4}`,

		`a{3,2}`,
	}
	for _, p := range reject {
		if _, err := CompileEREWithFlags(p, awkFlagPrefix); err == nil {
			t.Errorf("CompileEREWithFlags(%q) succeeded; want error", p)
		}
	}

	accept := []string{`a??`, `a*?`, `a+?`, `a{2}?`, `a{2,4}?`}
	for _, p := range accept {
		if _, err := CompileEREWithFlags(p, awkFlagPrefix); err != nil {
			t.Errorf("CompileEREWithFlags(%q) failed: %v; want accepted lazy suffix", p, err)
		}
	}
}
