package linkcmd

import (
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "link",
	Synopsis: "Call the link function to create a link named FILE2 to an existing FILE1.",
	Usage:    "link FILE1 FILE2",
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
		return toolcompat.UsageError(rc, cmd, "missing operand after '%s'", operands[0])
	case 2:

	default:
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[2])
	}
	if err := vfs.Link(rc.Path(operands[0]), rc.Path(operands[1])); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "link: cannot create link '%s' to '%s': %v\n", operands[1], operands[0], reason(err))
		return 1
	}
	return 0
}

func reason(err error) error {
	return toolcompat.SysErr(err)
}
