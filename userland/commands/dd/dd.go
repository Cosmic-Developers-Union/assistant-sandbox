package ddcmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var cmd = &toolcompat.Tool{
	Name:     "dd",
	Synopsis: "Copy a file, converting and formatting according to operands.",
	Usage:    "dd [OPERAND]...",
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

type config struct {
	ifile, ofile string
	ibs, obs     int64
	cbs          int64
	count        int64
	skip, seek   int64
	notrunc      bool
	noerror      bool
	fullblock    bool
	sync         bool
	block        bool
	unblock      bool
	lcase        bool
	ucase        bool
	swab         bool
	ascii        bool
	ebcdic       bool
	ibm          bool
	status       string
	posix        bool
	reblock      bool
}

func envPresent(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	cfg := config{ibs: 512, obs: 512, count: -1, reblock: true, posix: envPresent(rc.Env, "POSIXLY_CORRECT")}
	var bs int64
	for _, op := range operands {
		k, v, ok := strings.Cut(op, "=")
		if !ok || k == "" {
			return toolcompat.UsageError(rc, cmd, "unrecognized operand '%s'", op)
		}
		switch k {
		case "if":
			cfg.ifile = v
		case "of":
			cfg.ofile = v
		case "bs":
			n, err := parseBytes(v)
			if err != nil || n <= 0 {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			bs = n
		case "ibs":
			n, err := parseBytes(v)
			if err != nil || n <= 0 {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			cfg.ibs = n
			cfg.reblock = true
		case "obs":
			n, err := parseBytes(v)
			if err != nil || n <= 0 {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			cfg.obs = n
			cfg.reblock = true
		case "cbs":
			n, err := parseBytes(v)
			if err != nil || n < 0 {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			cfg.cbs = n
		case "count":
			n, err := parseCount(v)
			if err != nil {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			cfg.count = n
		case "skip":
			n, err := parseCount(v)
			if err != nil {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			cfg.skip = n
		case "seek":
			n, err := parseCount(v)
			if err != nil {
				return toolcompat.UsageError(rc, cmd, "invalid number: '%s'", v)
			}
			cfg.seek = n
		case "status":
			if v != "none" && v != "noxfer" {
				return toolcompat.NotSupported(rc, cmd, "status="+v)
			}
			cfg.status = v
		case "iflag":
			for _, flag := range strings.Split(v, ",") {
				switch flag {
				case "fullblock":
					cfg.fullblock = true
				default:
					return toolcompat.NotSupported(rc, cmd, "iflag="+flag)
				}
			}
		case "conv":
			for _, conversion := range strings.Split(v, ",") {
				switch conversion {
				case "notrunc":
					cfg.notrunc = true
				case "noerror":
					cfg.noerror = true
				case "sync":
					cfg.sync = true
				case "block":
					cfg.block = true
				case "unblock":
					cfg.unblock = true
				case "lcase":
					cfg.lcase = true
				case "ucase":
					cfg.ucase = true
				case "swab":
					cfg.swab = true
				case "ascii":
					cfg.ascii = true
				case "ebcdic":
					cfg.ebcdic = true
				case "ibm":
					cfg.ibm = true
				default:
					return toolcompat.NotSupported(rc, cmd, "conv="+conversion)
				}
			}
		default:
			return toolcompat.UsageError(rc, cmd, "unrecognized operand '%s'", op)
		}
	}
	switch {
	case cfg.ascii && cfg.ebcdic:
		return toolcompat.UsageError(rc, cmd, "conv=ascii and conv=ebcdic are mutually exclusive")
	case cfg.ascii && cfg.ibm:
		return toolcompat.UsageError(rc, cmd, "conv=ascii and conv=ibm are mutually exclusive")
	case cfg.ebcdic && cfg.ibm:
		return toolcompat.UsageError(rc, cmd, "conv=ebcdic and conv=ibm are mutually exclusive")
	}

	if cfg.ascii && cfg.block {
		return toolcompat.UsageError(rc, cmd, "conv=ascii and conv=block are mutually exclusive")
	}
	if cfg.ebcdic && cfg.unblock {
		return toolcompat.UsageError(rc, cmd, "conv=ebcdic and conv=unblock are mutually exclusive")
	}
	if cfg.ibm && cfg.unblock {
		return toolcompat.UsageError(rc, cmd, "conv=ibm and conv=unblock are mutually exclusive")
	}
	if cfg.ascii {
		cfg.unblock = true
	}
	if cfg.ebcdic || cfg.ibm {
		cfg.block = true
	}
	if cfg.block && cfg.unblock {
		return toolcompat.UsageError(rc, cmd, "conv=block and conv=unblock are mutually exclusive")
	}
	if cfg.lcase && cfg.ucase {
		return toolcompat.UsageError(rc, cmd, "conv=lcase and conv=ucase are mutually exclusive")
	}
	if (cfg.block || cfg.unblock) && cfg.cbs == 0 {
		return toolcompat.UsageError(rc, cmd, "cbs= is required with conv=ascii, conv=ebcdic, conv=ibm, conv=block, or conv=unblock")
	}
	if bs > 0 {

		cfg.ibs, cfg.obs = bs, bs

		cfg.reblock = cfg.block || cfg.unblock || cfg.lcase || cfg.ucase || cfg.swab
	}
	return copyDD(rc, cfg)
}

func copyDD(rc *toolcompat.RunContext, cfg config) (closeResult0 int) {
	status := ddStatus{cfg: cfg}
	var in = rc.In
	var inf vmfs.File
	if cfg.ifile != "" {
		f, err := vfs.Open(cfg.ifile)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: failed to open '%s': %v\n", cfg.ifile, reason(err))
			return 1
		}
		defer func() {
			if err := f.Close(); err != nil && closeResult0 == 0 {
				closeResult0 = 1
			}
		}()

		inf = f
		in = f
	}
	var out = rc.Out
	stdoutSeeker, stdoutSeekable := rc.Out.(io.Seeker)
	var outf vmfs.File

	defer func() {
		if outf != nil {
			_ = outf.Close()
		}
	}()
	var seekBytes int64
	if cfg.seek > 0 {
		var ok bool
		seekBytes, ok = multiplyBlocks(cfg.seek, cfg.obs)
		if !ok {
			_, _ = fmt.Fprintln(rc.Err, "dd: seek is too large")
			return 2
		}
	}
	if cfg.ofile != "" {
		flags := int(vfs.O_WRONLY | vfs.O_CREATE)
		if !cfg.notrunc && cfg.seek == 0 {

			flags |= int(vfs.O_TRUNC)
		}
		f, err := vfs.OpenFile(cfg.ofile, flags, 0o666)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: failed to open '%s': %v\n", cfg.ofile, reason(err))
			return 1
		}
		outf = f
		out = f
		if seekBytes > 0 && !cfg.notrunc {

			if err := truncateFile(outf, seekBytes); err != nil {
				if fi, serr := outf.Stat(); serr == nil && fi.Mode().IsRegular() {
					_, _ = fmt.Fprintf(rc.Err, "dd: failed to truncate '%s': %v（客体内存文件系统没有截断端口，可改用 conv=notrunc）\n", cfg.ofile, reason(err))
					return 1
				}
			}
		}
	}
	if cfg.skip > 0 {
		n, ok := multiplyBlocks(cfg.skip, cfg.ibs)
		if !ok {
			_, _ = fmt.Fprintln(rc.Err, "dd: skip is too large")
			return 2
		}
		if inf != nil {

			if _, err := inf.Seek(n, io.SeekStart); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "dd: failed to skip '%s': %v\n", cfg.ifile, reason(err))
				return 1
			}
		} else if _, err := io.CopyN(io.Discard, in, n); err != nil && !errors.Is(err, io.EOF) {
			_, _ = fmt.Fprintf(rc.Err, "dd: error skipping input: %v\n", reason(err))
			return 1
		}
	}
	if cfg.seek > 0 {
		if outf != nil {
			if _, err := outf.Seek(seekBytes, io.SeekStart); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "dd: failed to seek '%s': %v\n", cfg.ofile, reason(err))
				return 1
			}
		} else if stdoutSeekable {
			if _, err := stdoutSeeker.Seek(seekBytes, io.SeekStart); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "dd: failed to seek standard output: %v\n", reason(err))
				return 1
			}
		} else {
			return toolcompat.NotSupported(rc, cmd, "seek= with non-seekable standard output")
		}
	}

	counter := &countWriter{w: out}
	status.counter = counter
	out = counter
	var blocker *obsWriter
	if cfg.reblock {
		blocker = &obsWriter{w: out, obs: cfg.obs}
		status.blocker = blocker
		out = blocker
	}
	var blockConv *blockWriter
	var unblockConv *unblockWriter
	if cfg.block || cfg.unblock {
		conversionBuf, err := makeBuffer(cfg.cbs)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: conversion buffer: %v\n", err)
			return 1
		}
		if cfg.block {
			var translate *[256]byte
			switch {
			case cfg.ebcdic:
				translate = &a2ePOSIX
			case cfg.ibm:
				translate = &a2ibmPOSIX
			}
			blockConv = &blockWriter{w: out, record: conversionBuf[:0], width: len(conversionBuf), translate: translate}
			status.blockConv = blockConv
			out = blockConv
		} else {
			unblockConv = &unblockWriter{w: out, record: conversionBuf[:0], width: len(conversionBuf)}
			out = unblockConv
		}
	}
	buf, err := makeBuffer(cfg.ibs)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "dd: input buffer: %v\n", err)
		return 1
	}
	var hadReadError bool
	for cfg.count < 0 || status.full+status.partial < cfg.count {
		n, rerr := readInputBlock(in, buf, cfg.fullblock)
		if n > 0 {
			if int64(n) == cfg.ibs {
				status.full++
			} else {
				status.partial++
			}
			if cfg.sync && int64(n) < cfg.ibs {

				padInputBlock(buf, n, cfg)
				n = len(buf)
			}
			data := buf[:n]
			convertBytes(data, cfg)
			if err := writeAll(out, data); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "dd: error writing output: %v\n", reason(err))
				return 1
			}
			if blocker == nil {
				if int64(n) == cfg.obs {
					status.outFull++
				} else {
					status.outPartial++
				}
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: error reading input: %v\n", reason(rerr))
			if !cfg.noerror {
				return 1
			}

			collectStatus(&status)
			if err := emitStatus(rc.Err, &status); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "dd: error writing status: %v\n", reason(err))
				return 1
			}
			hadReadError = true
			if n == 0 {
				if cfg.sync {
					padInputBlock(buf, 0, cfg)
					convertBytes(buf, cfg)
					if err := writeAll(out, buf); err != nil {
						_, _ = fmt.Fprintf(rc.Err, "dd: error writing output: %v\n", reason(err))
						return 1
					}
					if blocker == nil {
						status.outFull++
					}
				}

				if inf != nil {
					if _, err := inf.Seek(cfg.ibs, io.SeekCurrent); err != nil {
						return 1
					}
					if cfg.sync {
						status.full++
					}
					continue
				}
				break
			}

			if n > 0 {
				continue
			}
			break
		}
	}
	if blockConv != nil {
		if err := blockConv.Flush(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: error writing output: %v\n", reason(err))
			return 1
		}
	}
	if unblockConv != nil {
		if err := unblockConv.Flush(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: error writing output: %v\n", reason(err))
			return 1
		}
	}
	if blocker != nil {
		if err := blocker.Flush(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: error writing output: %v\n", reason(err))
			return 1
		}
		status.outFull, status.outPartial = blocker.full, blocker.partial
	}
	if outf != nil {
		if err := outf.Close(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "dd: error closing '%s': %v\n", cfg.ofile, reason(err))
			return 1
		}
		outf = nil
	}
	collectStatus(&status)
	if err := emitStatus(rc.Err, &status); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "dd: error writing status: %v\n", reason(err))
		return 1
	}
	if hadReadError {
		return 1
	}
	return 0
}

type ddStatus struct {
	cfg                 config
	counter             *countWriter
	full, partial       int64
	outFull, outPartial int64
	truncated           int64
	blockConv           *blockWriter
	blocker             *obsWriter
}

func collectStatus(status *ddStatus) {
	if status.blockConv != nil {
		status.truncated = status.blockConv.truncated
	}
	if status.blocker != nil {

		status.outFull, status.outPartial = status.blocker.full, status.blocker.partial
	}
}

func emitStatus(w io.Writer, status *ddStatus) error {
	if status.cfg.status == "none" {
		return nil
	}
	if _, err := fmt.Fprintf(w, "%d+%d records in\n", status.full, status.partial); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%d+%d records out\n", status.outFull, status.outPartial); err != nil {
		return err
	}
	if status.truncated > 0 {
		suffix := "s"
		if status.truncated == 1 {
			suffix = ""
		}
		if _, err := fmt.Fprintf(w, "%d truncated record%s\n", status.truncated, suffix); err != nil {
			return err
		}
	}
	if !status.cfg.posix && status.cfg.status != "noxfer" {
		var n int64
		if status.counter != nil {
			n = status.counter.n
		}
		if _, err := fmt.Fprintf(w, "%d bytes copied\n", n); err != nil {
			return err
		}
	}
	return nil
}

func padInputBlock(buf []byte, n int, cfg config) {
	switch {
	case cfg.block || cfg.unblock:

		for i := n; i < len(buf); i++ {
			buf[i] = ' '
		}
	default:
		clear(buf[n:])
	}
}

func convertBytes(p []byte, cfg config) {
	if cfg.swab {
		swabBytes(p)
	}
	if cfg.ascii {
		translateBytes(p, &e2aPOSIX)
	}
	if cfg.lcase {
		lowercaseBytes(p)
	} else if cfg.ucase {
		uppercaseBytes(p)
	}
}

func swabBytes(p []byte) {
	for i := 0; i+1 < len(p); i += 2 {
		p[i], p[i+1] = p[i+1], p[i]
	}
}

func lowercaseBytes(p []byte) {
	for i, c := range p {
		if c >= 'A' && c <= 'Z' {
			p[i] = c + ('a' - 'A')
		}
	}
}

func uppercaseBytes(p []byte) {
	for i, c := range p {
		if c >= 'a' && c <= 'z' {
			p[i] = c - ('a' - 'A')
		}
	}
}

func readInputBlock(in io.Reader, buf []byte, fullblock bool) (int, error) {
	if !fullblock {
		return in.Read(buf)
	}
	n, err := io.ReadFull(in, buf)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return n, io.EOF
	}
	return n, err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type blockWriter struct {
	w         io.Writer
	record    []byte
	width     int
	overflow  bool
	truncated int64
	translate *[256]byte
}

func (b *blockWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		if c == '\n' {
			if err := b.emit(); err != nil {
				return 0, err
			}
			continue
		}
		if len(b.record) < b.width {
			b.record = append(b.record, c)
		} else {
			b.overflow = true
		}
	}
	return len(p), nil
}

func (b *blockWriter) emit() error {
	for len(b.record) < b.width {
		b.record = append(b.record, ' ')
	}
	if b.translate != nil {
		translateBytes(b.record, b.translate)
	}
	if err := writeAll(b.w, b.record); err != nil {
		return err
	}
	if b.overflow {
		b.truncated++
	}
	b.record = b.record[:0]
	b.overflow = false
	return nil
}

func (b *blockWriter) Flush() error {
	if len(b.record) == 0 && !b.overflow {
		return nil
	}
	return b.emit()
}

type unblockWriter struct {
	w      io.Writer
	record []byte
	width  int
}

func (u *unblockWriter) Write(p []byte) (int, error) {
	for _, c := range p {
		u.record = append(u.record, c)
		if len(u.record) == u.width {
			if err := u.emit(); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

func (u *unblockWriter) emit() error {
	end := len(u.record)
	for end > 0 && u.record[end-1] == ' ' {
		end--
	}
	if err := writeAll(u.w, u.record[:end]); err != nil {
		return err
	}
	if err := writeAll(u.w, []byte{'\n'}); err != nil {
		return err
	}
	u.record = u.record[:0]
	return nil
}

func (u *unblockWriter) Flush() error {
	if len(u.record) == 0 {
		return nil
	}
	return u.emit()
}

type obsWriter struct {
	w             io.Writer
	obs           int64
	buf           []byte
	full, partial int64
}

func (o *obsWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		if len(o.buf) == 0 && int64(len(p)) >= o.obs {
			if err := writeAll(o.w, p[:o.obs]); err != nil {
				return 0, err
			}
			o.full++
			p = p[o.obs:]
			continue
		}
		n := min(int(o.obs)-len(o.buf), len(p))
		o.buf = append(o.buf, p[:n]...)
		p = p[n:]
		if int64(len(o.buf)) == o.obs {
			if err := writeAll(o.w, o.buf); err != nil {
				return 0, err
			}
			o.full++
			o.buf = o.buf[:0]
		}
	}
	return total, nil
}

func (o *obsWriter) Flush() error {
	if len(o.buf) == 0 {
		return nil
	}
	if err := writeAll(o.w, o.buf); err != nil {
		return err
	}
	o.partial++
	o.buf = o.buf[:0]
	return nil
}

var byteMultipliers = map[string]int64{
	"":    1,
	"c":   1,
	"w":   2,
	"b":   512,
	"k":   1024,
	"kB":  1000,
	"K":   1024,
	"KB":  1000,
	"M":   1024 * 1024,
	"MB":  1000 * 1000,
	"G":   1024 * 1024 * 1024,
	"GB":  1000 * 1000 * 1000,
	"KiB": 1024,
	"MiB": 1024 * 1024,
	"GiB": 1024 * 1024 * 1024,
}

func parseBytes(s string) (int64, error) {
	parts := strings.Split(s, "x")
	if len(parts) == 0 {
		return 0, strconv.ErrSyntax
	}
	total := int64(1)
	for _, part := range parts {
		n, err := parseByteFactor(part)
		if err != nil {
			return 0, err
		}
		var ok bool
		total, ok = checkedMul(total, n)
		if !ok {
			return 0, strconv.ErrRange
		}
	}
	return total, nil
}

func parseByteFactor(s string) (int64, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, err
	}
	m, ok := byteMultipliers[s[i:]]
	if !ok {
		return 0, strconv.ErrSyntax
	}
	prod, ok := checkedMul(n, m)
	if !ok {
		return 0, strconv.ErrRange
	}
	return prod, nil
}

func checkedMul(a, b int64) (int64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if a != 0 && b > maxInt64/a {
		return 0, false
	}
	return a * b, true
}

func parseCount(s string) (int64, error) {
	n, err := parseBytes(s)
	if err == nil {
		return n, nil
	}
	n, err = strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		if err == nil {
			err = strconv.ErrRange
		}
		return 0, err
	}
	return n, nil
}

func multiplyBlocks(blocks, size int64) (int64, bool) {
	if blocks < 0 || size < 0 || (size != 0 && blocks > int64(^uint64(0)>>1)/size) {
		return 0, false
	}
	return blocks * size, true
}

func makeBuffer(size int64) (buf []byte, err error) {
	if size <= 0 || uint64(size) > uint64(^uint(0)>>1) {
		return nil, strconv.ErrRange
	}
	defer func() {
		if recover() != nil {
			buf = nil
			err = errors.New("cannot allocate memory")
		}
	}()
	return make([]byte, int(size)), nil
}

func truncateFile(f vmfs.File, size int64) error {
	if truncater, ok := f.(interface{ Truncate(int64) error }); ok {
		return truncater.Truncate(size)
	}
	return errno.Wrap(errno.NotSupported, "客体文件不支持截断")
}

func writeAll(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err != nil {
		return err
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}

func reason(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
