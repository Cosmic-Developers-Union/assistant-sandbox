package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCRLFWriterNormalizesTerminalLines(t *testing.T) {
	var output bytes.Buffer
	writer := &crlfWriter{writer: &output}
	if _, err := writer.Write([]byte("one\ntwo\r\nthree")); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "one\r\ntwo\r\nthree"; got != want {
		t.Fatalf("输出=%q want %q", got, want)
	}
}

func TestTerminalPairRejectsNonFiles(t *testing.T) {
	if terminalPair(strings.NewReader(""), &bytes.Buffer{}) {
		t.Fatal("内存流不应启用宿主 PTY")
	}
}
