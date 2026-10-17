package shell

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestNestedBashReapsProcessesAndRetainsParent(t *testing.T) {
	var out bytes.Buffer
	s, err := newSession(t.TempDir(), nil, strings.NewReader(""), &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	tree, err := parse(`for ((i=0;i<300;i++)); do bash -c 'basename /work/value'; done`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.runner.Run(proc.WithContext(t.Context(), s.parent), tree); err != nil {
		t.Fatal(err, out.String())
	}
	if got := s.kernel.Processes(); len(got) != 2 {
		t.Fatal(fmt.Sprint(got))
	}
	if strings.Count(out.String(), "value\n") != 300 {
		t.Fatal("嵌套 Bash 输出不完整")
	}
}
