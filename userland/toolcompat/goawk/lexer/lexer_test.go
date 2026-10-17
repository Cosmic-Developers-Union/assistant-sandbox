package lexer

import "testing"

func TestUnescapeRejectsOutOfRangeUnicode(t *testing.T) {
	for _, value := range []string{`\uFFFFFFFF`, `\uD800`} {
		if _, err := Unescape(value); err == nil {
			t.Fatalf("Unescape(%q) 应拒绝无效 Unicode", value)
		}
	}
}
