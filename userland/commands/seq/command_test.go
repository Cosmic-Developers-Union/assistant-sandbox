package seq

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"3"}, "1\n2\n3\n"},
		{[]string{"2", "4"}, "2\n3\n4\n"},
		{[]string{"3", "2", "7"}, "3\n5\n7\n"},
		{[]string{"1", "1"}, "1\n"},

		{[]string{"5", "1"}, ""},
		{[]string{"1", "-1", "5"}, ""},
		{[]string{"5", "-1", "1"}, "5\n4\n3\n2\n1\n"},
		{[]string{"-3", "-1"}, "-3\n-2\n-1\n"},
		{[]string{"-1", "-1", "-3"}, "-1\n-2\n-3\n"},

		{[]string{"1000000", "1000002"}, "1000000\n1000001\n1000002\n"},

		{[]string{"0", "0.3", "1"}, "0.0\n0.3\n0.6\n0.9\n"},
		{[]string{"0", "0.1", "1"}, "0.0\n0.1\n0.2\n0.3\n0.4\n0.5\n0.6\n0.7\n0.8\n0.9\n1.0\n"},
		{[]string{"0.5", "2"}, "0.5\n1.5\n"},

		{[]string{"1", "1", "2.5"}, "1\n2\n"},

		{[]string{"-s", ",", "3"}, "1,2,3\n"},
		{[]string{"--separator", " ", "3"}, "1 2 3\n"},
		{[]string{"-w", "8", "10"}, "08\n09\n10\n"},
		{[]string{"-w", "1", "3"}, "1\n2\n3\n"},
		{[]string{"-w", "-5", "5", "10"}, "-5\n00\n05\n10\n"},
		{[]string{"-w", "1", "0.5", "2"}, "1.0\n1.5\n2.0\n"},
		{[]string{"-w", "9.5", "10.5"}, "09.5\n10.5\n"},

		{[]string{"-f", "%.2f", "1", "3"}, "1.00\n2.00\n3.00\n"},
		{[]string{"-f", "%.2e", "1", "2"}, "1.00e+00\n2.00e+00\n"},
		{[]string{"-f", "%f", "1", "1"}, "1.000000\n"},
		{[]string{"-f", "%g", "1", "1"}, "1\n"},
		{[]string{"-f", "%%%g", "1", "1"}, "%1\n"},
		{[]string{"-f", "==%.0f==", "1", "2"}, "==1==\n==2==\n"},

		{[]string{"1e2", "1e2"}, "100\n"},
		{[]string{"-t", ",", "3"}, "1\n2\n3,"},
		{[]string{"--terminator", "|", "2"}, "1\n2|"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 0 || result.Stdout != test.want || result.Stderr != "" {
			t.Fatalf("seq %v = %+v, want (%q, \"\", 0)", test.args, result, test.want)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args    []string
		errPart string
	}{
		{[]string{}, "missing operand"},
		{[]string{"1", "1", "1", "1"}, "extra operand"},
		{[]string{"x"}, "invalid floating point argument"},
		{[]string{"1", "y", "3"}, "invalid floating point argument"},
		{[]string{"1", "0", "3"}, "invalid zero increment"},
		{[]string{"-w", "-f", "%g", "3"}, "equal width"},
		{[]string{"-f", "%d", "3"}, "unknown %d directive"},
		{[]string{"-f", "no directive", "3"}, "no % directive"},
		{[]string{"-f", "%g%g", "3"}, "too many % directives"},
		{[]string{"-f", "%", "3"}, "ends in %"},
		{[]string{"--frobnicate", "3"}, "frobnicate"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 2 || !strings.Contains(result.Stderr, test.errPart) {
			t.Fatalf("seq %v: code=%d err=%q, want 2 containing %q", test.args, result.Code, result.Stderr, test.errPart)
		}
	}
}

func TestOutputBudget(t *testing.T) {

	result := commandtest.RunWith(t, Run, t.TempDir(), []string{"1", "1", "1048577"}, "", commandtest.Options{OutputLimit: 16 << 20})
	if result.Code != 2 || !strings.Contains(result.Stderr, "预算") {
		t.Fatalf("超大范围未触发预算: %+v", result)
	}
}

func TestCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := commandtest.Invoke(ctx, Run, t.TempDir(), []string{"1", "100000000"}, strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
}
