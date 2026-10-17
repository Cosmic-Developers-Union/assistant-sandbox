package file

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
	"mvdan.cc/sh/v3/interp"
)

const sniffLimit = 4 << 10

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "file")
	follow := flags.BoolP("follow", "L", false, "跟随符号链接（缺省不跟随）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) == 0 {
		_, _ = fmt.Fprintln(h.Stderr, "file: 需要 file 操作数")
		return interp.ExitStatus(2)
	}
	_, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	failures := 0
	for _, name := range operands {
		target := commandio.Path(h.Dir, name)
		var description string
		var ferr error
		if *follow {
			description, ferr = describe(ctx, system, target, true)
		} else {
			description, ferr = describe(ctx, system, target, false)
		}
		if ferr != nil {
			_, _ = fmt.Fprintf(h.Stderr, "file: %s: %v\n", name, ferr)
			failures++
			continue
		}
		if _, err := fmt.Fprintf(h.Stdout, "%s: %s\n", name, description); err != nil {
			return err
		}
	}
	if failures > 0 {
		return interp.ExitStatus(1)
	}
	return nil
}

func describe(ctx context.Context, system vmfs.System, target string, follow bool) (closeResult0 string, closeResult1 error) {
	info, err := system.Lstat(ctx, target)
	if err != nil {
		return "", err
	}
	if !follow && info.Mode()&fs.ModeSymlink != 0 {
		link, err := system.Readlink(ctx, target)
		if err != nil {
			return "", err
		}
		return "symbolic link to " + link, nil
	}
	if info.IsDir() {
		return "directory", nil
	}
	file, err := system.Open(ctx, target, vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		closeResult1 = errors.Join(closeResult1, file.Close())
	}()

	head := make([]byte, sniffLimit)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	return classify(head[:n]), nil
}

func classify(head []byte) string {
	if len(head) == 0 {
		return "empty"
	}
	if d := elfDescription(head); d != "" {
		return d
	}
	if len(head) > 2 && head[0] == '#' && head[1] == '!' {
		interpreter := firstWord(head[2:])
		return "a " + interpreter + " script text executable"
	}
	switch {
	case len(head) > 3 && head[0] == 0x1f && head[1] == 0x8b:
		return "gzip compressed data"
	case len(head) > 3 && head[0] == 'B' && head[1] == 'Z' && head[2] == 'h':
		return "bzip2 compressed data"
	case len(head) > 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd:
		return "zstd compressed data"
	case len(head) > 6 && head[0] == 0xfd && head[1] == '7' && head[2] == 'z' && head[3] == 'X' && head[4] == 'Z' && head[5] == 0x00:
		return "xz compressed data"
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		return "POSIX tar archive"
	case len(head) > 7 && string(head[:8]) == "!<arch>\n":
		return "current ar archive"
	}
	return textDescription(head)
}

func elfDescription(head []byte) string {
	if len(head) < 18 || head[0] != 0x7f || head[1] != 'E' || head[2] != 'L' || head[3] != 'F' {
		return ""
	}
	class := "32-bit"
	if head[4] == 2 {
		class = "64-bit"
	}
	endian := "LSB"
	if head[5] == 2 {
		endian = "MSB"
	}
	kind := "unknown type"
	if e_type := int(head[16]) | int(head[17])<<8; e_type == 2 {
		kind = "executable"
	} else if e_type == 1 {
		kind = "relocatable"
	} else if e_type == 3 {
		kind = "shared object"
	}
	return "ELF " + class + " " + endian + ", " + kind
}

func firstWord(rest []byte) string {
	line := rest
	if index := indexByte(rest, '\n'); index >= 0 {
		line = rest[:index]
	}
	text := strings.TrimSpace(string(line))
	if index := strings.IndexAny(text, " \t"); index >= 0 {
		text = text[:index]
	}
	return text
}

func textDescription(head []byte) string {
	utf8 := false
	for _, b := range head {
		switch {
		case b == 0:
			return "data"
		case b == '\n' || b == '\t' || b == '\r' || (b >= 0x20 && b <= 0x7e):

		case b >= 0x80:
			utf8 = true
		default:
			return "data"
		}
	}
	if utf8 {
		return "UTF-8 Unicode text"
	}
	return "ASCII text"
}

func indexByte(data []byte, c byte) int {
	for i, b := range data {
		if b == c {
			return i
		}
	}
	return -1
}
