package jobs

import (
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func TestJobsReportsEmptyTable(t *testing.T) {
	run := func(args ...string) commandtest.Result {
		return commandtest.Run(t, Run, t.TempDir(), args, "")
	}
	if r := run(); r.Code != 0 || r.Stdout != "" || r.Stderr != "" {
		t.Fatalf("jobs = %+v", r)
	}
	if r := run("-l"); r.Code != 0 || r.Stdout != "" {
		t.Fatalf("jobs -l = %+v", r)
	}
	if r := run("-p"); r.Code != 0 || r.Stdout != "" {
		t.Fatalf("jobs -p = %+v", r)
	}
	if r := run("%1", "42"); r.Code != 1 || r.Stdout != "" ||
		r.Stderr != "jobs: %1: 没有这样的作业\njobs: 42: 没有这样的作业\n" {
		t.Fatalf("jobs %%1 42 = %+v", r)
	}
}
