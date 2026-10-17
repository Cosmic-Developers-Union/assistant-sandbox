package executable

import (
	"context"
	"errors"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func TestFixedProgramsAndRejectedFallback(t *testing.T) {
	program := Func(func(context.Context, proc.Proc) (uint8, error) { return 3, nil })
	entries := map[string]Program{"ok": program}
	r, err := New(entries)
	if err != nil {
		t.Fatal(err)
	}
	delete(entries, "ok")
	p, err := r.Lookup("ok")
	if err != nil {
		t.Fatal(err)
	}
	code, err := p.Run(t.Context(), proc.Proc{})
	if code != 3 || err != nil {
		t.Fatal(code, err)
	}
	if _, err := r.Lookup("/usr/bin/unknown"); !errors.Is(err, errno.NotFound) {
		t.Fatal(err)
	}
	for _, name := range []string{"", "a/b", "a\\b", "a b", "a\x00b"} {
		if _, err := New(map[string]Program{name: program}); !errors.Is(err, errno.Invalid) {
			t.Fatal(name, err)
		}
	}
	for _, p := range []Program{nil, Func(nil)} {
		if _, err := New(map[string]Program{"nil": p}); !errors.Is(err, errno.Invalid) {
			t.Fatal(err)
		}
	}
}
