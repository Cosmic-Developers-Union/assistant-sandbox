package truecmd

import (
	"fmt"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd *toolcompat.Tool

func init() {
	cmd = &toolcompat.Tool{
		Name:     "true",
		Synopsis: "Do nothing, successfully.",
		Usage:    "true [ignored command line arguments]",
	}
	cmd.Run = func(rc *toolcompat.RunContext, args []string) int {
		if len(args) > 0 {
			switch args[0] {
			case "--help", "-h":
				_, _ = fmt.Fprintf(rc.Out, "Usage: %s\n%s\n\nOptions:\n      --help     display this help and exit\n      --version  output version information and exit\n", cmd.Usage, cmd.Synopsis)
			case "--version", "-V":
				_, _ = fmt.Fprintf(rc.Out, "%s (qiangli/coreutils) %s\n", cmd.Name, toolcompat.Version)
			}
		}
		return 0
	}
	toolcompat.Register(cmd)
}
