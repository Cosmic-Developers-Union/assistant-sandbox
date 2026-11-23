package main

import (
	"bytes"
	"io"
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

func TestBridgeInputCompletesUniqueCommand(t *testing.T) {
	var output bytes.Buffer
	completer := func(prefix string) []string {
		if prefix == "ech" {
			return []string{"echo"}
		}
		return nil
	}
	if err := bridgeInputWithCompletion(bytes.NewBufferString("ech\t\n"), &output, io.Discard, completer, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "echo\n"; got != want {
		t.Fatalf("输出=%q want %q", got, want)
	}
}

func TestBridgeInputListsAmbiguousCommands(t *testing.T) {
	var output, display bytes.Buffer
	completer := func(prefix string) []string {
		if prefix == "l" {
			return []string{"ls", "less"}
		}
		return nil
	}
	prompt := func() string { return "sandbox:/work# " }
	if err := bridgeInputWithCompletion(bytes.NewBufferString("l\t"), &output, &display, completer, prompt); err != nil {
		t.Fatal(err)
	}
	if output.String() != "l" {
		t.Fatalf("PTY 输入=%q", output.String())
	}
	if got, want := display.String(), "\r\nless  ls\r\nsandbox:/work# l"; got != want {
		t.Fatalf("候选=%q want %q", got, want)
	}
}
