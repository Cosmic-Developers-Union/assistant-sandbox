package pastecmd

import (
	"testing"

	"mvdan.cc/sh/v3/interp"
)

func TestEnvironmentHelpers(t *testing.T) {
	if got := environ(interp.HandlerContext{}); got != nil {
		t.Fatalf("无环境应返回 nil: %v", got)
	}
	if got := envMap(interp.HandlerContext{}); len(got) != 0 {
		t.Fatalf("无环境应返回空 map: %v", got)
	}
}

func TestCharacterModel(t *testing.T) {
	for _, test := range []struct {
		env     []string
		model   encodingMode
		failure bool
	}{
		{nil, encodingSingleByte, false},
		{[]string{"LC_CTYPE=C"}, encodingSingleByte, false},
		{[]string{"LC_CTYPE=POSIX"}, encodingSingleByte, false},
		{[]string{"LC_CTYPE=C.UTF-8"}, encodingUTF8, false},
		{[]string{"LC_ALL=C.utf8"}, encodingUTF8, false},
		{[]string{"LC_CTYPE=de_DE.ISO-8859-1"}, encodingSingleByte, false},
		{[]string{"LC_CTYPE=en_US.UTF-8"}, 0, true},
		{[]string{"LC_CTYPE=de_DE.UTF-8"}, 0, true},
	} {
		model, err := resolveCharacterModel(test.env)
		if test.failure {
			if err == nil {
				t.Fatalf("env=%v 应报错", test.env)
			}
			continue
		}
		if err != nil || model.encoding != test.model {
			t.Fatalf("env=%v 得到 %+v err=%v，want %v", test.env, model, err, test.model)
		}
	}
	if got := normalizeCodeset("iso-8859_1"); got != "ISO88591" {
		t.Fatalf("normalizeCodeset=%q", got)
	}
	if base, codeset := splitLocaleName("de_DE.ISO-8859-1@euro"); base != "de_DE" || codeset != "ISO-8859-1" {
		t.Fatalf("splitLocaleName=%q %q", base, codeset)
	}
	if base, codeset := splitLocaleName("C"); base != "C" || codeset != "" {
		t.Fatalf("无代码集: %q %q", base, codeset)
	}
}

func TestParseDelims(t *testing.T) {
	model, err := resolveCharacterModel(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		in   string
		want string
		fail bool
	}{
		{":", ":", false},
		{`\n`, "\n", false},
		{`\t`, "\t", false},
		{`\\`, `\`, false},
		{`\q`, "q", false},
		{"", "", true},
	} {
		delims, message := parseDelims(test.in, model)
		if test.fail {
			if message == "" {
				t.Fatalf("%q 应报错", test.in)
			}
			continue
		}
		if message != "" || len(delims) == 0 || string(delims[0]) != test.want {
			t.Fatalf("parseDelims(%q)=%v message=%q，want %q", test.in, delims, message, test.want)
		}
	}
	if size := characterSize("a", model); size != 1 {
		t.Fatalf("单字节字符宽度 %d", size)
	}
	utf8Model, err := resolveCharacterModel([]string{"LC_CTYPE=C.UTF-8"})
	if err != nil {
		t.Fatal(err)
	}
	if size := characterSize("é", utf8Model); size != 2 {
		t.Fatalf("UTF-8 字符宽度 %d", size)
	}
	if size := characterSize("", model); size != 1 {
		t.Fatalf("空串按一个字节处理，得到 %d", size)
	}
}

func TestPOSIXMode(t *testing.T) {
	if !pastePOSIXMode([]string{"POSIXLY_CORRECT=1"}) {
		t.Fatal("POSIXLY_CORRECT 应被识别")
	}
	if pastePOSIXMode(nil) || pastePOSIXMode([]string{"OTHER=1"}) {
		t.Fatal("没有 POSIXLY_CORRECT 时不应进入 POSIX 模式")
	}
}
