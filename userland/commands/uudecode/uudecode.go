package uudecodecmd

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var cmd = &toolcompat.Tool{
	Name:     "uudecode",
	Synopsis: "Decode a uuencoded file.",
	Usage: "uudecode [OPTION]... [FILE]...\n\n" +
		"Decode traditional or begin-base64 data from FILEs, or standard input.\n\n" +
		"  -o, --output-file=FILE  write to FILE instead of the header name\n\n" +
		"Header output names use ordinary pathname semantics.",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type header struct {
	name   string
	mode   fs.FileMode
	base64 bool
}

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	output := fs.StringP("output-file", "o", "", "write to FILE instead of the header name")
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	if envPresent(rc.Env, "POSIXLY_CORRECT") && len(operands) > 1 {
		return toolcompat.UsageError(rc, cmd, "extra operand %q", operands[1])
	}
	if *output != "" && len(operands) > 1 {
		return toolcompat.UsageError(rc, cmd, "--output-file cannot be used with multiple input files")
	}
	if len(operands) == 0 {
		if decodeInput(rc, "standard input", rc.In, *output) {
			return 0
		}
		return 1
	}

	failed := false
	for _, operand := range operands {
		f, err := vfs.Open(operand)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uudecode: %s: %v\n", operand, err)
			failed = true
			continue
		}
		ok := decodeInput(rc, operand, f, *output)
		if err := f.Close(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uudecode: %s: %v\n", operand, err)
			ok = false
		}
		if !ok {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func envPresent(env []string, key string) bool {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return true
		}
	}
	return false
}

func decodeInput(rc *toolcompat.RunContext, source string, input io.Reader, override string) bool {
	r := bufio.NewReader(input)
	found := false
	for {
		line, err := readLine(r)
		if err != nil && !errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintf(rc.Err, "uudecode: %s: read error: %v\n", source, err)
			return false
		}
		if h, ok, parseErr := parseHeader(line); ok || parseErr != nil {
			if parseErr != nil {
				_, _ = fmt.Fprintf(rc.Err, "uudecode: %s\n", parseErr)
				return false
			}
			found = true
			if !decodePart(rc, r, h, override) {
				return false
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if !found {
		_, _ = fmt.Fprintf(rc.Err, "uudecode: %s: no 'begin' line\n", source)
	}
	return found
}

func parseHeader(line string) (header, bool, error) {
	base64Header := strings.HasPrefix(line, "begin-base64 ")
	classicHeader := strings.HasPrefix(line, "begin ")
	if !base64Header && !classicHeader {
		if strings.HasPrefix(line, "begin-") {
			return header{}, false, fmt.Errorf("unsupported begin header")
		}
		return header{}, false, nil
	}
	prefix := "begin "
	if base64Header {
		prefix = "begin-base64 "
	}
	parts := strings.SplitN(strings.TrimPrefix(line, prefix), " ", 2)
	if len(parts) != 2 || parts[1] == "" {
		return header{}, false, fmt.Errorf("malformed 'begin' line")
	}
	mode, err := parseHeaderMode(parts[0])
	if err != nil {
		return header{}, false, fmt.Errorf("malformed 'begin' line")
	}
	return header{name: parts[1], mode: mode, base64: base64Header}, true, nil
}

func decodePart(rc *toolcompat.RunContext, r *bufio.Reader, h header, override string) bool {
	name := h.name
	toStdout := h.name == "-" || h.name == "/dev/stdout"
	if override != "" {
		name = override

		toStdout = override == "/dev/stdout"
	}

	decode := func(w io.Writer) error {
		if h.base64 {
			return decodeBase64(r, w)
		}
		return decodeClassic(r, w)
	}
	if toStdout {
		if err := decode(rc.Out); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "uudecode: %v\n", err)
			return false
		}
		return true
	}
	chmodErr, err := decodeOutput(rc.Path(name), h.mode, decode)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "uudecode: %s: %v\n", name, err)
		return false
	}
	if chmodErr != nil && !errors.Is(chmodErr, errno.NotSupported) {

		if !envPresent(rc.Env, "POSIXLY_CORRECT") {
			_, _ = fmt.Fprintf(rc.Err, "uudecode: %s: warning: cannot set permissions: %v\n", name, chmodErr)
		}
	}
	return true
}

var chmodDecodedFile = func(path string, mode fs.FileMode) error {
	return vfs.Chmod(path, mode.Perm())
}

func decodeOutput(target string, mode fs.FileMode, decode func(io.Writer) error) (chmodErr, err error) {
	target = path.Clean(target)
	info, statErr := vfs.Stat(target)
	switch {
	case statErr == nil && info.Mode().IsRegular():
		return decodeExistingRegular(target, mode, decode)
	case statErr == nil:

		return nil, fmt.Errorf("output path exists and is not a regular file")
	case vfs.NotExist(statErr):
		return decodeNewAtomically(target, mode, decode)
	default:
		return nil, statErr
	}
}

func decodeExistingRegular(target string, mode fs.FileMode, decode func(io.Writer) error) (chmodErr, err error) {
	staged, stagedName, err := createTemp(path.Dir(target), ".uudecode-")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = staged.Close()
		_ = vfs.Remove(stagedName)
	}()
	if err := decode(staged); err != nil {
		return nil, err
	}
	if _, err := staged.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	file, err := vfs.OpenFile(target, int(vfs.O_WRONLY|vfs.O_TRUNC), mode.Perm())
	if err != nil {
		return nil, err
	}

	buffer := make([]byte, 32*1024)
	for {
		read, readErr := staged.Read(buffer)
		if read > 0 {
			if _, writeErr := file.Write(buffer[:read]); writeErr != nil {
				_ = file.Close()
				return nil, writeErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return nil, readErr
		}
	}
	chmodErr = chmodDecodedFile(target, mode.Perm())
	if err := file.Close(); err != nil {
		return chmodErr, err
	}
	return chmodErr, nil
}

func decodeNewAtomically(target string, mode fs.FileMode, decode func(io.Writer) error) (chmodErr, err error) {

	tmp, tmpName, err := createTempMode(path.Dir(target), ".uudecode-", mode.Perm())
	if err != nil {
		return nil, err
	}
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
		_ = vfs.Remove(tmpName)
	}()
	if err := decode(tmp); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	tmp = nil
	return nil, vfs.Rename(tmpName, target)
}

func createTemp(dir, prefix string) (vmfs.File, string, error) {
	return createTempMode(dir, prefix, 0o600)
}

func createTempMode(dir, prefix string, perm fs.FileMode) (vmfs.File, string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		name := path.Join(dir, fmt.Sprintf("%s%d", prefix, attempt))

		file, err := vfs.OpenFile(name, int(vfs.O_RDWR|vfs.O_CREATE|vfs.O_EXCL), perm)
		if err == nil {
			return file, name, nil
		}
		if !vfs.Exist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("无法创建临时文件")
}

func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), err
}

func decodeClassic(r *bufio.Reader, out io.Writer) error {
	for {
		line, err := readLine(r)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read error: %w", err)
		}
		if line == "end" {
			return fmt.Errorf("malformed data: missing zero-length line before 'end'")
		}
		if line == "" && errors.Is(err, io.EOF) {
			return fmt.Errorf("short file")
		}
		if line == "" {
			return fmt.Errorf("malformed empty data line")
		}
		n := int(dec(line[0]))
		if n == 0 {
			next, nextErr := readLine(r)
			if nextErr != nil && !errors.Is(nextErr, io.EOF) {
				return fmt.Errorf("read error: %w", nextErr)
			}
			if next != "end" {
				return fmt.Errorf("malformed data: expected 'end'")
			}
			return nil
		}
		if n > 45 {
			return fmt.Errorf("invalid data line length %d", n)
		}
		need := ((n + 2) / 3) * 4
		if len(line)-1 < need {
			return fmt.Errorf("short data line")
		}
		decoded := make([]byte, 0, n)
		for i := 1; i <= need; i += 4 {
			for j := 0; j < 4; j++ {
				c := line[i+j]
				if c < 0x20 || c > 0x60 {
					return fmt.Errorf("invalid encoded character 0x%02x", c)
				}
			}
			v := uint32(dec(line[i]))<<18 | uint32(dec(line[i+1]))<<12 | uint32(dec(line[i+2]))<<6 | uint32(dec(line[i+3]))
			decoded = append(decoded, byte(v>>16), byte(v>>8), byte(v))
		}
		if _, err := out.Write(decoded[:n]); err != nil {
			return fmt.Errorf("write error: %w", err)
		}
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("short file")
		}
	}
}

func decodeBase64(r *bufio.Reader, out io.Writer) error {
	var quantum [4]byte
	n := 0
	padded := false
	for {
		line, err := readLine(r)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read error: %w", err)
		}
		if line == "====" {
			if n != 0 {
				return fmt.Errorf("malformed base64 data")
			}
			return nil
		}
		for i := 0; i < len(line); i++ {
			c := line[i]
			if !base64Char(c) {
				continue
			}
			if padded {
				return fmt.Errorf("malformed base64 data")
			}
			quantum[n] = c
			n++
			if n != len(quantum) {
				continue
			}
			var decoded [3]byte
			count, decErr := base64.StdEncoding.Decode(decoded[:], quantum[:])
			if decErr != nil {
				return fmt.Errorf("malformed base64 data")
			}
			if _, writeErr := out.Write(decoded[:count]); writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}
			padded = quantum[2] == '=' || quantum[3] == '='
			n = 0
		}
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("short base64 data")
		}
	}
}

func base64Char(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
}

func dec(b byte) byte { return (b - 0x20) & 0x3f }
