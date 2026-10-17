package parser

import "testing"

func TestProgramString(t *testing.T) {
	program, err := ParseProgram([]byte("BEGIN { print \"ready\" }"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := program.String(); got != "BEGIN {\n    print \"ready\"\n}" {
		t.Fatalf("Program.String() = %q", got)
	}
}
