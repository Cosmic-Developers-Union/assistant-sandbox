package bre

import (
	"reflect"
	"testing"
)

func TestBytePatternSnapshotCopiesEveryField(t *testing.T) {
	input := syntheticBytePatternTables()
	input.dotAll = true
	input.multi = true
	got := input.snapshot()
	if !reflect.DeepEqual(got, input) {
		t.Fatal("snapshot dropped a bytePatternTables field")
	}

	got.classes["word"] = [256]bool{}
	if reflect.DeepEqual(got.classes["word"], input.classes["word"]) {
		t.Fatal("snapshot aliased the caller's class map")
	}
}

func TestLocaleByteEquivalenceIsGrammarIndependent(t *testing.T) {
	tables, err := SnapshotLocaleByteTables(fakeByteEquivalence{newFakeByteCtype()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pattern string
		input   byte
		want    bool
	}{
		{`[[=a=]]`, 'a', true},
		{`[[=a=]]`, 0xe4, true},
		{`[[=a=]]`, 'b', false},
		{`[[=b=]]`, 'b', true},
		{`[[=b=]]`, 0xe4, false},
		{`[[.a.]]`, 'a', true},
		{`[[.a.]]`, 0xe4, false},
	} {
		for _, syntax := range []struct {
			name string
			kind ByteRegexpSyntax
		}{{"BRE", ByteRegexpBRE}, {"ERE", ByteRegexpERE}} {
			re, err := CompileLocaleByteRegexpTables([]byte(tc.pattern), tables, ByteRegexpOptions{Syntax: syntax.kind})
			if err != nil {
				t.Fatalf("%s compile %q: %v", syntax.name, tc.pattern, err)
			}
			got, err := re.MatchString(string([]byte{tc.input}))
			if err != nil {
				t.Fatalf("%s match %q: %v", syntax.name, tc.pattern, err)
			}
			if got != tc.want {
				t.Errorf("%s %q against %#02x = %v, want %v", syntax.name, tc.pattern, tc.input, got, tc.want)
			}
		}
	}
}
