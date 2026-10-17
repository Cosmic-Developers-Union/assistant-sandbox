package mkfifo

import (
	"context"
	"fmt"
	stdfs "io/fs"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const defaultMode = 0o666

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "mkfifo")

	flags.SetInterspersed(false)
	mode := flags.StringP("mode", "m", "", "新建管道的权限位（八进制；符号形式尚未支持）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) == 0 {
		return fmt.Errorf("缺少文件操作数")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	fifos, ok := system.(vmfs.Fifos)
	if !ok {

		return fmt.Errorf("客体文件系统不能创建命名管道")
	}
	var wanted *uint32
	if flags.Changed("mode") {
		parsed, err := parseMode(*mode)
		if err != nil {
			return err
		}
		wanted = &parsed
		if _, ok := system.(vmfs.Modes); !ok {

			return fmt.Errorf("客体文件系统没有模式位能力，无法把 -m 落到位")
		}
	}
	failures := 0
	for _, name := range operands {
		if err := create(ctx, fifos, system, name, wanted); err != nil {
			_, _ = fmt.Fprintf(h.Stderr, "mkfifo: %s: %v\n", name, err)
			failures++
		}
	}
	if failures > 0 {

		return interp.ExitStatus(1)
	}
	return nil
}

func create(ctx context.Context, fifos vmfs.Fifos, system vmfs.System, name string, wanted *uint32) error {
	if err := fifos.Mkfifo(ctx, name, defaultMode); err != nil {
		return err
	}
	if wanted == nil {
		return nil
	}
	modes, _ := system.(vmfs.Modes)

	if err := modes.Chmod(ctx, name, stdfs.FileMode(*wanted)); err != nil {
		return fmt.Errorf("设置权限位: %w", err)
	}
	return nil
}

func parseMode(text string) (uint32, error) {
	value, err := strconv.ParseUint(text, 8, 32)
	if err != nil || value > 0o7777 {
		return 0, fmt.Errorf("-m 只支持八进制权限位（如 644）：%q", text)
	}
	return uint32(value), nil
}
