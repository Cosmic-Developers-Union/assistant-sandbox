package commcmd

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
)

var cmd = &toolcompat.Tool{
	Name:     "comm",
	Synopsis: "Compare two sorted files line by line.",
	Usage: "comm [OPTION]... FILE1 FILE2\n\n" +
		"When FILE1 or FILE2 (not both) is -, read standard input.\n\n" +
		"With no options, produce three-column output. Column one contains\n" +
		"lines unique to FILE1, column two lines unique to FILE2, and column\n" +
		"three lines common to both files.\n\n" +
		"  -1   suppress column 1 (lines unique to FILE1)\n" +
		"  -2   suppress column 2 (lines unique to FILE2)\n" +
		"  -3   suppress column 3 (lines that appear in both files)",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type stringCollator interface {
	Compare(a, b string) (int, error)
	Close() error
}

type collatorOpener func(string) (stringCollator, error)

func run(rc *toolcompat.RunContext, args []string) int {
	return runWithCollator(rc, args, func(name string) (stringCollator, error) {
		return locale.OpenEnv(locale.StoreEnvAt(rc.Env, rc.Path), name)
	})
}

func runWithCollator(rc *toolcompat.RunContext, args []string, openCollator collatorOpener) (status int) {

	var suppress [4]bool
	rest := make([]string, 0, len(args))
	valuePending := false
	for idx, a := range args {
		if valuePending {
			rest = append(rest, a)
			valuePending = false
			continue
		}
		if a == "--" {
			rest = append(rest, args[idx:]...)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			rest = append(rest, args[idx:]...)
			break
		}
		if strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			name, _, hasValue := strings.Cut(a[2:], "=")
			if !hasValue && strings.HasPrefix("output-delimiter", name) {
				valuePending = true
			}
			continue
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			hasDigits := false
			for i := 1; i < len(a); i++ {
				if a[i] >= '1' && a[i] <= '3' {
					hasDigits = true
					break
				}
			}
			if hasDigits {
				var rebuilt strings.Builder
				rebuilt.WriteByte('-')
				for i := 1; i < len(a); i++ {
					if a[i] >= '1' && a[i] <= '3' {
						suppress[a[i]-'0'] = true
					} else {
						rebuilt.WriteByte(a[i])
					}
				}
				if rebuilt.Len() > 1 {
					rest = append(rest, rebuilt.String())
				}
				continue
			}
		}
		rest = append(rest, a)
	}

	fs := toolcompat.NewFlags(cmd.Name)
	checkOrder := fs.Bool("check-order", false, "check that the input is correctly sorted")
	nocheckOrder := fs.Bool("nocheck-order", false, "do not check that the input is correctly sorted")
	outputDelimiter := fs.String("output-delimiter", "", "separate columns with STR")
	total := fs.Bool("total", false, "output a summary")
	zeroTerminated := fs.BoolP("zero-terminated", "z", false, "line delimiter is NUL, not newline")

	operands, code := toolcompat.ParseRequireOrder(rc, cmd, fs, rest)
	if code >= 0 {
		return code
	}
	switch {
	case len(operands) == 0:
		return toolcompat.UsageError(rc, cmd, "missing operand")
	case len(operands) == 1:
		return toolcompat.UsageError(rc, cmd, "missing operand after '%s'", operands[0])
	case len(operands) > 2:
		return toolcompat.UsageError(rc, cmd, "extra operand '%s'", operands[2])
	}
	if operands[0] == "-" && operands[1] == "-" {
		return toolcompat.UsageError(rc, cmd, "both files cannot be standard input")
	}

	compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
	if name := locale.ResolveCarried(locale.StoreEnvAt(rc.Env, rc.Path), locale.Collate); name != "C" && name != "POSIX" {
		provider, err := openCollator(name)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "comm: LC_COLLATE=%s: %v\n", name, err)
			return 2
		}
		defer func() {
			if err := provider.Close(); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "comm: LC_COLLATE=%s: %v\n", name, err)
				if status == 0 {
					status = 1
				}
			}
		}()
		compare = provider.Compare
	}

	var inputs [2]*recordReader
	for i, op := range operands {
		input, err := openRecordReader(rc, op, *zeroTerminated)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "comm: %s: %v\n", op, pathErr(err))
			if inputs[0] != nil {
				inputs[0].close()
			}
			return 1
		}
		inputs[i] = input
	}
	defer inputs[0].close()
	defer inputs[1].close()

	delim := "\t"
	if fs.Changed("output-delimiter") {
		delim = *outputDelimiter
	}

	var colCounts [4]int
	bw := bufio.NewWriter(rc.Out)
	emit := func(col int, line string) {
		colCounts[col]++
		if suppress[col] {
			return
		}
		tabs := 0
		for c := 1; c < col; c++ {
			if !suppress[c] {
				tabs++
			}
		}
		_, _ = bw.WriteString(strings.Repeat(delim, tabs))
		_, _ = bw.WriteString(line)
		if *zeroTerminated {
			_ = bw.WriteByte('\x00')
		} else {
			_ = bw.WriteByte('\n')
		}
	}

	seenUnpairable := false
	var warned [2]bool
	var current [2]string
	var have [2]bool
	advance := func(file int) bool {
		idx := file - 1
		previous, hadPrevious := current[idx], have[idx]
		next, ok, err := inputs[idx].next()
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "comm: %s: %v\n", operands[idx], pathErr(err))
			return false
		}
		current[idx], have[idx] = next, ok
		order := 0
		if hadPrevious && ok {
			order, err = compare(previous, next)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "comm: comparison failed: %v\n", err)
				return false
			}
		}
		if hadPrevious && ok && order > 0 {
			if !*nocheckOrder {
				if *checkOrder {
					_, _ = fmt.Fprintf(rc.Err, "comm: file %d is not in sorted order\n", file)
					return false
				}
				if seenUnpairable && !warned[file-1] {
					_, _ = fmt.Fprintf(rc.Err, "comm: file %d is not in sorted order\n", file)
					warned[file-1] = true
				}
			}
		}
		return true
	}
	if !advance(1) || !advance(2) {
		return 1
	}
	for have[0] || have[1] {
		var d int
		switch {
		case !have[0]:
			d = 1
		case !have[1]:
			d = -1
		default:
			var err error
			d, err = compare(current[0], current[1])
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "comm: comparison failed: %v\n", err)
				return 1
			}
		}
		switch {
		case d < 0:
			seenUnpairable = true
			emit(1, current[0])
			if !advance(1) {
				return 1
			}
		case d > 0:
			seenUnpairable = true
			emit(2, current[1])
			if !advance(2) {
				return 1
			}
		default:
			emit(3, current[0])
			if !advance(1) || !advance(2) {
				return 1
			}
		}
	}
	if *total {
		c1 := colCounts[1]
		if suppress[1] {
			c1 = 0
		}
		c2 := colCounts[2]
		if suppress[2] {
			c2 = 0
		}
		c3 := colCounts[3]
		if suppress[3] {
			c3 = 0
		}
		sum := c1 + c2 + c3
		_, _ = fmt.Fprintf(bw, "%d%s%d%s%d%s%d total", c1, delim, c2, delim, c3, delim, sum)
		if *zeroTerminated {
			_ = bw.WriteByte('\x00')
		} else {
			_ = bw.WriteByte('\n')
		}
	}
	if err := bw.Flush(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "comm: write failed: %v\n", err)
		return 1
	}
	if warned[0] || warned[1] {
		_, _ = fmt.Fprintln(rc.Err, "comm: input is not in sorted order")
		return 1
	}
	return 0
}

type recordReader struct {
	reader    *bufio.Reader
	closer    io.Closer
	delimiter byte
}

func openRecordReader(rc *toolcompat.RunContext, operand string, zeroTerminated bool) (*recordReader, error) {
	var input io.Reader
	var closer io.Closer
	if operand == "-" {
		input = rc.In
	} else {
		file, err := vfs.Open(rc.Path(operand))
		if err != nil {
			return nil, err
		}
		input, closer = file, file
	}
	delimiter := byte('\n')
	if zeroTerminated {
		delimiter = 0
	}
	return &recordReader{reader: bufio.NewReader(input), closer: closer, delimiter: delimiter}, nil
}

func (r *recordReader) next() (string, bool, error) {
	record, err := r.reader.ReadString(r.delimiter)
	if len(record) > 0 {
		if record[len(record)-1] == r.delimiter {
			record = record[:len(record)-1]
		}
		if err == io.EOF {
			err = nil
		}
		return record, true, err
	}
	if err == io.EOF {
		return "", false, nil
	}
	return "", false, err
}

func (r *recordReader) close() {
	if r.closer != nil {
		_ = r.closer.Close()
	}
}

func pathErr(err error) error {
	return toolcompat.SysErr(err)
}
