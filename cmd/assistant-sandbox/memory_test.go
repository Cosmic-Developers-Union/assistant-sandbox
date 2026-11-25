package main

import "testing"

func TestParseMemoryAcceptsCanonicalValues(t *testing.T) {
	cases := map[string]uint64{
		"256m":   256 << 20,
		"256mb":  256 << 20,
		"300m":   300 << 20,
		"1.5g":   1536 << 20,
		"2g":     2 << 30,
		"16g":    16 << 30,
		"16384m": 16 << 30,
		"512k":   512 << 10,
		"1024":   1024,
	}
	for value, want := range cases {
		got, err := parseMemory(value)
		if err != nil || got != want {
			t.Fatalf("parseMemory(%q)=%d,%v want %d", value, got, err, want)
		}
	}
}

func TestParseMemoryRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "0", "-1g", "abc", "1.2.3g", "256x"} {
		if _, err := parseMemory(value); err == nil {
			t.Fatalf("parseMemory(%q) 应失败", value)
		}
	}
}
