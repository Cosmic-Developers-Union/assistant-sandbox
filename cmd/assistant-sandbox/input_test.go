package main

import (
	"bytes"
	"testing"
)

func TestBridgeInputRecallsHistory(t *testing.T) {
	var output bytes.Buffer
	input := bytes.NewBufferString("echo hi\n\x1b[A\x1b[B\n")
	if err := bridgeInput(input, &output); err != nil {
		t.Fatal(err)
	}
	want := "echo hi\n\x15echo hi\x15\n"
	if got := output.String(); got != want {
		t.Fatalf("输出=%q want %q", got, want)
	}
}

func TestBridgeInputIgnoresUnknownEscape(t *testing.T) {
	var output bytes.Buffer
	if err := bridgeInput(bytes.NewBufferString("echo ok\n\x1b[C"), &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "echo ok\n"; got != want {
		t.Fatalf("输出=%q want %q", got, want)
	}
}
