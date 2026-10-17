package bccmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	bcengine "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/internal/bc"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var cmd = &toolcompat.Tool{Name: "bc", Synopsis: "Evaluate an arbitrary-precision calculator language.", Usage: "bc [-l] [file ...]"}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	fs.SetInterspersed(false)
	mathlib := fs.BoolP("mathlib", "l", false, "load the standard math library")
	files, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	stdin := rc.In
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	eng := bcengine.New(rc.Out, stdin)
	if *mathlib {
		eng.Scale = 20
		eng.Mathlib = true
	}
	for _, name := range files {
		f, err := vfs.Open(name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "bc: %s: %v\n", name, err)
			return 1
		}
		data, err := io.ReadAll(f)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "bc: %s: %v\n", name, err)
			return 1
		}
		if err = eng.Execute(string(data)); err != nil && !errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintf(rc.Err, "bc: %s: %v\n", name, err)
			return 1
		}
		if errors.Is(err, io.EOF) {
			return 0
		}
	}
	data, err := io.ReadAll(eng.In)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "bc: %v\n", err)
		return 1
	}
	if err = eng.Execute(string(data)); err != nil && !errors.Is(err, io.EOF) {
		_, _ = fmt.Fprintf(rc.Err, "bc: %v\n", err)
		return 1
	}
	return 0
}
