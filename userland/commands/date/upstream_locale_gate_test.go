package datecmd

import "testing"

func TestFormatNeedsLocale(t *testing.T) {
	for _, f := range []string{
		"%Y%m%d-%H%M", "%F", "%T", "%s", "%Y-%m-%dT%H:%M:%S%z",
		"%D", "%R", "%I:%M", "%j", "%%b", "plain text", "",
	} {
		if formatNeedsLocale(f) {
			t.Errorf("formatNeedsLocale(%q) = true, want false — it reads no locale data", f)
		}
	}
	for _, f := range []string{
		"%a", "%A", "%b", "%B", "%h", "%c", "%p", "%P", "%r", "%x", "%X",
		"%Y %b %d", "%Ex", "%OY", "%%%b",
	} {
		if !formatNeedsLocale(f) {
			t.Errorf("formatNeedsLocale(%q) = false, want true — it prints locale text", f)
		}
	}
}

func TestEscapedPercentDoesNotConsumeTheNextSpecifier(t *testing.T) {
	if formatNeedsLocale("%%b") {
		t.Error("formatNeedsLocale of an escaped percent = true: %% is a literal percent, so the b is text")
	}
	if !formatNeedsLocale("%%%b") {
		t.Error("formatNeedsLocale of escaped-percent-then-conversion = false: the third %% starts a real month-name conversion")
	}
}
