package df

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "df")

	flags.SetInterspersed(false)
	kilobytes := flags.BoolP("kibibytes", "k", false, "以 1024 字节为单位（默认 512）")
	portable := flags.BoolP("portability", "P", false, "输出 POSIX 规定的格式")
	total := flags.BoolP("total", "t", false, "包含总分配空间（默认格式本来就包含）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if *portable && *total {

		return fmt.Errorf("-P 与 -t 互斥")
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	capacity, ok := system.(vmfs.Capacity)
	if !ok {

		return fmt.Errorf("客体文件系统没有容量能力")
	}
	unit := uint64(512)
	if *kilobytes {
		unit = 1024
	}
	spaces, failures := collect(ctx, capacity, flags.Args())
	if len(spaces) == 0 && len(failures) > 0 {

		return failures[0]
	}
	if err := write(h.Stdout, spaces, unit, *portable); err != nil {
		return err
	}
	for _, failure := range failures {
		_, _ = fmt.Fprintf(h.Stderr, "df: %v\n", failure)
	}
	if len(failures) > 0 {

		return interp.ExitStatus(1)
	}
	return nil
}

func collect(ctx context.Context, capacity vmfs.Capacity, operands []string) ([]vmfs.Space, []error) {
	if len(operands) == 0 {
		spaces, err := capacity.Filesystems(ctx)
		if err != nil {
			return nil, []error{err}
		}
		return spaces, nil
	}
	var (
		spaces  []vmfs.Space
		reasons []error
		seen    = map[string]bool{}
	)
	for _, operand := range operands {
		space, err := capacity.Space(ctx, operand)
		if err != nil {
			reasons = append(reasons, fmt.Errorf("%s: %w", operand, err))
			continue
		}
		if seen[space.Root] {
			continue
		}
		seen[space.Root] = true
		spaces = append(spaces, space)
	}
	return spaces, reasons
}

func write(out interface{ Write([]byte) (int, error) }, spaces []vmfs.Space, unit uint64, portable bool) error {
	header := fmt.Sprintf("Filesystem %d-blocks Used Available Capacity Mounted on", unit)
	if !portable {

		header += " Inodes IFree"
	}
	if _, err := fmt.Fprintln(out, header); err != nil {
		return err
	}
	for _, space := range spaces {
		line := fmt.Sprintf("%s %s %s %s %s %s",
			space.Name, cell(space.Bytes, space.Bytes.Total, unit),
			units(space.Bytes.Used, unit), cell(space.Bytes, space.Bytes.Free, unit),
			percentage(space.Bytes), space.Root)
		if !portable {
			line += fmt.Sprintf(" %s %s", cell(space.Inodes, space.Inodes.Total, 1), cell(space.Inodes, space.Inodes.Free, 1))
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func cell(quota vmfs.Quota, value, unit uint64) string {
	if !quota.Declared {
		return "-"
	}
	return units(value, unit)
}

func units(value, unit uint64) string {
	if unit <= 1 {
		return strconv.FormatUint(value, 10)
	}
	return strconv.FormatUint((value+unit-1)/unit, 10)
}

func percentage(quota vmfs.Quota) string {
	if !quota.Declared {
		return "-"
	}
	denominator := quota.Used + quota.Free
	if denominator == 0 {
		return "0%"
	}
	return strconv.FormatUint((quota.Used*100+denominator-1)/denominator, 10) + "%"
}
