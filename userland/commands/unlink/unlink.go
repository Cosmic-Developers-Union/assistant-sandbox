package unlinkcmd

import (
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "unlink",
	Synopsis: "Call the unlink function to remove the specified FILE.",
	Usage:    "unlink FILE",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	args = toolcompat.AliasHelpVersion(args)
	fs := toolcompat.NewFlags(cmd.Name)
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	switch len(operands) {
	case 0:
		return toolcompat.UsageError(rc, cmd, "missing operand")
	case 1:

	default:
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[1])
	}
	name := operands[0]
	path := rc.Path(name)

	if fi, err := vfs.Lstat(path); err == nil && fi.IsDir() {
		_, _ = fmt.Fprintf(rc.Err, "unlink: cannot unlink '%s': Is a directory\n", name)
		return 1
	}
	if err := vfs.Remove(path); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "unlink: cannot unlink '%s': %v\n", name, reason(err))
		return 1
	}
	return 0
}

func reason(err error) error {
	return toolcompat.SysErr(err)
}
