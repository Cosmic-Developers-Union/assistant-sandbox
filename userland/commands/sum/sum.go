package sumcmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "sum",
	Synopsis: "Print checksum and block count for each FILE. With no FILE, or when FILE is -, read standard input.",
	Usage: "sum [OPTION]... [FILE]...\n\n" +
		"  -r          use BSD sum algorithm (the default), use 1K blocks\n" +
		"  -s, --sysv  use System V sum algorithm, use 512 bytes blocks",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {

	sysv := false
	rest := make([]string, 0, len(args))
preparse:
	for idx, a := range args {
		switch {
		case a == "--":
			rest = append(rest, args[idx:]...)
			break preparse
		case a == "--sysv":
			sysv = true
			continue
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			cluster := a[1:]
			kept := make([]byte, 0, len(cluster))
			for i := 0; i < len(cluster); i++ {
				switch cluster[i] {
				case 'r':
					sysv = false
				case 's':
					sysv = true
				default:
					kept = append(kept, cluster[i:]...)
					i = len(cluster)
				}
			}
			if len(kept) > 0 {
				rest = append(rest, "-"+string(kept))
			}
			continue
		}
		rest = append(rest, a)
	}
	fs := toolcompat.NewFlags(cmd.Name)
	operands, code := toolcompat.Parse(rc, cmd, fs, rest)
	if code >= 0 {
		return code
	}
	withName := len(operands) > 0
	if !withName {
		operands = []string{"-"}
	}
	exit := 0
	for _, name := range operands {
		result, err := sumOperand(rc, name, sysv)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "sum: %s: %s\n", name, errMsg(err))
			exit = 1
			continue
		}
		suffix := ""
		if withName {
			suffix = " " + name
		}
		if sysv {
			_, _ = fmt.Fprintf(rc.Out, "%d %d%s\n", result.checksum, result.blocks, suffix)
		} else {
			_, _ = fmt.Fprintf(rc.Out, "%05d %5d%s\n", result.checksum, result.blocks, suffix)
		}
	}
	return exit
}

type sumResult struct {
	checksum uint16
	blocks   uint64
}

func sumOperand(rc *toolcompat.RunContext, name string, sysv bool) (closeResult0 sumResult, closeResult1 error) {
	var r io.Reader
	if name == "-" {
		if rc.In == nil {
			r = strings.NewReader("")
		} else {
			r = rc.In
		}
	} else {
		f, err := vfs.Open(rc.Path(name))
		if err != nil {
			return sumResult{}, err
		}
		defer func() {
			closeResult1 = errors.Join(closeResult1, f.Close())
		}()

		if fi, err := f.Stat(); err == nil && fi.IsDir() {
			return sumResult{}, errIsDirectory
		}
		r = f
	}
	if sysv {
		return sysvSum(r)
	}
	return bsdSum(r)
}

func bsdSum(r io.Reader) (sumResult, error) {
	var checksum uint16
	var size uint64
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			checksum = (checksum >> 1) + ((checksum & 1) << 15) + uint16(b)
		}
		size += uint64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return sumResult{}, err
		}
	}
	return sumResult{checksum: checksum, blocks: blocks(size, 1024)}, nil
}

func sysvSum(r io.Reader) (sumResult, error) {
	var checksum uint32
	var size uint64
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			checksum += uint32(b)
		}
		size += uint64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return sumResult{}, err
		}
	}
	checksum = (checksum & 0xffff) + (checksum >> 16)
	checksum = (checksum & 0xffff) + (checksum >> 16)
	return sumResult{checksum: uint16(checksum), blocks: blocks(size, 512)}, nil
}

func blocks(size, block uint64) uint64 {
	if size == 0 {
		return 0
	}
	return (size + block - 1) / block
}

var errIsDirectory = fmt.Errorf("Is a directory")

func errMsg(err error) string {
	if errors.Is(err, errIsDirectory) {
		return err.Error()
	}
	if errors.Is(err, vfs.ErrNotExist) {
		return "No such file or directory"
	}
	if errors.Is(err, vfs.ErrPermission) {
		return "Permission denied"
	}
	return err.Error()
}
