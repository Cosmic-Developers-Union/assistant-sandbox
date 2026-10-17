package digest

import (
	"strings"
	"testing"
)

func TestContentIsStableAndFormatFixed(t *testing.T) {
	first := Content([]byte("#!/bin/bash\necho hi\n"))
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("格式不符: %q", first)
	}
	if second := Content([]byte("#!/bin/bash\necho hi\n")); second != first {
		t.Fatal("相同内容必须得到相同身份")
	}
}

func TestContentSeparatesLengthFromContent(t *testing.T) {
	if Content([]byte("ab")) == Content([]byte("a")) {
		t.Fatal("不同内容不应相同")
	}
	if Content(nil) == Content([]byte{}) {
		t.Log("nil 与空切片视为同一内容（长度都是 0）")
	}

	if Content([]byte("23")) == Content([]byte("3")) {
		t.Fatal("不同内容不应相同")
	}
}
