package uname

import (
	"context"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
)

const (
	system  = "Linux"
	machine = "riscv64"
	release = "sandbox"
	version = "assistant-sandbox"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "uname")
	sys := flags.BoolP("kernel-name", "s", false, "内核名")
	node := flags.BoolP("nodename", "n", false, "主机名")
	rel := flags.BoolP("kernel-release", "r", false, "内核版本")
	ver := flags.BoolP("kernel-version", "v", false, "内核构建信息")
	mach := flags.BoolP("machine", "m", false, "机器架构")
	operating := flags.BoolP("operating-system", "o", false, "操作系统")
	all := flags.BoolP("all", "a", false, "全部")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("uname 不接受操作数")
	}
	fields := make([]string, 0, 6)
	if *all || (!*sys && !*node && !*rel && !*ver && !*mach && !*operating) || *sys {
		fields = append(fields, system)
	}
	if *all || *node {
		fields = append(fields, "sandbox")
	}
	if *all || *rel {
		fields = append(fields, release)
	}
	if *all || *ver {
		fields = append(fields, version)
	}
	if *all || *mach {
		fields = append(fields, machine)
	}
	if *all || *operating {
		fields = append(fields, "assistant")
	}
	_, err := fmt.Fprintln(h.Stdout, strings.Join(fields, " "))
	return err
}
