package uuencodecmd

import (
	"encoding/base64"
	"fmt"
	"io"
	stdfs "io/fs"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "uuencode",
	Synopsis: "Encode a binary file in printable ASCII.",
	Usage: "uuencode [-m] [FILE] REMOTE-FILE\n\n" +
		"Read FILE, or standard input when FILE is omitted, and write an encoded\n" +
		"representation using REMOTE-FILE in its header. -m uses base64 framing.",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) (closeResult0 int) {
	fs := toolcompat.NewFlags(cmd.Name)
	base64Mode := fs.BoolP("base64", "m", false, "encode using base64")
	var operands []string
	var code int
	if posixMode(rc.Env) {
		operands, code = toolcompat.ParseRequireOrder(rc, cmd, fs, args)
	} else {
		operands, code = toolcompat.Parse(rc, cmd, fs, args)
	}
	if code >= 0 {
		return code
	}
	if len(operands) == 0 {
		return toolcompat.UsageError(rc, cmd, "missing operand")
	}
	if len(operands) > 2 {
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[2])
	}

	input := rc.In
	var mode stdfs.FileMode
	var err error
	remote := operands[0]
	var file vmfs.File
	if len(operands) == 2 {
		remote = operands[1]

		file, err = vfs.Open(rc.Path(operands[0]))
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: %s: %v\n", operands[0], err)
			return 1
		}
		defer func() {
			if err := file.Close(); err != nil && closeResult0 == 0 {
				closeResult0 = 1
			}
		}()

		input = file
		mode, err = inputMode(file)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: %s: %v\n", operands[0], err)
			return 1
		}
	} else {

		mode, err = inputMode(input)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: standard input: %v\n", err)
			return 1
		}
	}

	prefix := "begin"
	if *base64Mode {
		prefix = "begin-base64"
	}
	if _, err := fmt.Fprintf(rc.Out, "%s %03o %s\n", prefix, mode.Perm(), remote); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "uuencode: write error: %v\n", err)
		return 1
	}
	if *base64Mode {
		return encodeBase64(rc, input)
	}
	return encodeClassic(rc, input)
}

func posixMode(env []string) bool {
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], "POSIXLY_CORRECT=") {
			return true
		}
	}
	return false
}

func inputMode(input io.Reader) (stdfs.FileMode, error) {
	if file, ok := input.(vmfs.File); ok {
		info, err := file.Stat()
		if err != nil {
			return 0, err
		}
		return info.Mode().Perm(), nil
	}
	return 0o666, nil
}

func encodeClassic(rc *toolcompat.RunContext, input io.Reader) int {
	buf := make([]byte, 45)
	for {
		n, err := io.ReadFull(input, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: read error: %v\n", err)
			return 1
		}
		if n == 0 {
			break
		}
		line := make([]byte, 1, 1+((n+2)/3)*4+1)
		line[0] = enc(byte(n))
		for i := 0; i < n; i += 3 {
			var v uint32
			for j := 0; j < 3; j++ {
				v <<= 8
				if i+j < n {
					v |= uint32(buf[i+j])
				}
			}
			line = append(line, enc(byte(v>>18)), enc(byte(v>>12)), enc(byte(v>>6)), enc(byte(v)))
		}
		line = append(line, '\n')
		if _, err := rc.Out.Write(line); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: write error: %v\n", err)
			return 1
		}
		if err == io.ErrUnexpectedEOF {
			break
		}
	}
	if _, err := io.WriteString(rc.Out, " \nend\n"); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "uuencode: write error: %v\n", err)
		return 1
	}
	return 0
}

func enc(b byte) byte {
	return (b & 0x3f) + 0x20
}

func encodeBase64(rc *toolcompat.RunContext, input io.Reader) int {
	buf := make([]byte, 57)
	for {
		n, err := io.ReadFull(input, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: read error: %v\n", err)
			return 1
		}
		if n == 0 {
			break
		}
		line := make([]byte, base64.StdEncoding.EncodedLen(n))
		base64.StdEncoding.Encode(line, buf[:n])
		if _, err := fmt.Fprintf(rc.Out, "%s\n", line); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uuencode: write error: %v\n", err)
			return 1
		}
		if err == io.ErrUnexpectedEOF {
			break
		}
	}
	if _, err := io.WriteString(rc.Out, "====\n"); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "uuencode: write error: %v\n", err)
		return 1
	}
	return 0
}
