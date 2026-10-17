package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestJournalResultsRemainDistinct(t *testing.T) {
	body := "0|start|TCC Start\n10|0 /grep 00:00|TC Start\n"
	for i, name := range []string{"PASS", "FAIL", "UNRESOLVED", "NOTINUSE", "UNSUPPORTED", "UNTESTED", "UNINITIATED", "NORESULT", "INSPECT"} {
		body += fmt.Sprintf("220|0 %d %d 00:01|%s\n", i+1, i, name)
	}
	body += "900|end|TCC End\n"
	r, err := parseJournal(strings.NewReader(body))
	if err != nil || !r.RunComplete || r.Counts["PASS"] != 1 || r.Counts["UNTESTED"] != 1 || r.Counts["OTHER"] != 1 || r.Results[0].Case != "/grep" || r.Results[8].Description != "INSPECT" {
		t.Fatal(r, err)
	}
	incomplete, err := parseJournal(strings.NewReader(strings.Split(body, "900|")[0]))
	if err != nil || incomplete.RunComplete {
		t.Fatal(incomplete, err)
	}
}
func TestJournalRejectsMalformedContradictoryAndDuplicateResults(t *testing.T) {
	prefix := "0|start|TCC Start\n"
	result := "220|0 1 0 00:01|PASS\n"
	for _, bad := range []string{"broken", "x|x|x", "900|x|x", result, prefix + "0|x|x", prefix + "220|0 1|PASS", prefix + "220|0 invalid 0|PASS", prefix + "220|-1 1 0|PASS", prefix + result + result, prefix + result + "900|x|x\n20|x|x", prefix + "220|0 1 0|FAIL", prefix + "10|0|start", prefix + "10|bad /case|start", strings.Repeat("x", (1<<20)+1)} {
		if _, err := parseJournal(strings.NewReader(bad)); err == nil {
			t.Fatal(bad)
		}
	}
	if _, err := parseJournal(failedReader{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }
