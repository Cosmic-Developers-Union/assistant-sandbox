package strip

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const (
	ehdrSize = 64
	shdrSize = 64

	offShoff    = 0x28
	offShent    = 0x3A
	offShnum    = 0x3C
	offShstrndx = 0x3E

	shName   = 0
	shType   = 4
	shFlags  = 8
	shOffset = 24
	shSize   = 32
	shLink   = 40

	sectionNull   = 0
	sectionSymtab = 2
	sectionStrtab = 3

	identMagic0      = 0x7F
	classELF64       = 2
	dataLittleEndian = 1
	versionCurrent   = 1
)

const maxInputBytes = 4 << 20

var errNotELF = errors.New("不是 ELF64")

var errMalformed = errors.New("ELF 格式损坏")

type sectionHeader struct {
	raw  []byte
	typ  uint32
	link uint32
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	diag := &stickyWriter{writer: h.Stderr}
	var files []string
	terminated := false
	for _, arg := range args {
		if !terminated && len(arg) > 1 && arg[0] == '-' {
			if arg == "--" {
				terminated = true
				continue
			}
			_, _ = fmt.Fprintf(diag, "strip: 不支持的选项 %q：客体 strip 只做就地剥离，没有任何选项\n", arg)
			return interp.ExitStatus(2)
		}
		files = append(files, arg)
	}
	if len(files) == 0 {
		_, _ = fmt.Fprintln(diag, "strip: 缺少文件")
		return interp.ExitStatus(2)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "strip: %v\n", err)
		return interp.ExitStatus(1)
	}
	code := 0
	budget := maxInputBytes
	for _, name := range files {
		raw, read, err := readFile(ctx, system, commandio.Path(h.Dir, name), budget)
		budget -= read
		if err != nil {
			_, _ = fmt.Fprintf(diag, "strip: %s: %v\n", name, err)
			code = 1
			if errors.Is(err, errOverBudget) {

				return interp.ExitStatus(1)
			}
			continue
		}
		stripped, changed, err := stripImage(raw)
		if err != nil {
			_, _ = fmt.Fprintf(diag, "strip: %s: %v\n", name, err)
			code = 1
			continue
		}
		if !changed {

			continue
		}
		if err := writeViaTemp(ctx, system, commandio.Path(h.Dir, name), stripped); err != nil {
			_, _ = fmt.Fprintf(diag, "strip: %s: %v\n", name, err)
			code = 1
		}
	}
	if diag.err != nil {
		return interp.ExitStatus(1)
	}
	if code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

type stickyWriter struct {
	writer io.Writer
	err    error
}

func (w *stickyWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func stripImage(raw []byte) ([]byte, bool, error) {
	shoff, shnum, shstrndx, sections, err := parseSections(raw)
	if err != nil {
		return nil, false, err
	}

	removed := map[int]bool{}
	for i, sec := range sections {
		if sec.typ != sectionSymtab {
			continue
		}
		removed[i] = true
		if int(sec.link) < shnum && sections[sec.link].typ == sectionStrtab {
			removed[int(sec.link)] = true
		}
	}
	if len(removed) == 0 {
		return raw, false, nil
	}
	var kept []int
	var keptCount uint16
	for i := range sections {
		if !removed[i] {
			kept = append(kept, i)
			keptCount++
		}
	}

	type linkFix struct {
		pos     uint64
		newLink uint32
	}
	var fixes []linkFix
	for pos, index := range kept {
		sec := sections[index]
		if sec.link == sectionNull {
			continue
		}
		link := int(sec.link)
		if removed[link] {
			return nil, false, fmt.Errorf("段 #%d 的 sh_link 指向被移除的段 #%d，剥离会留下悬挂引用", index, link)
		}
		shift := 0
		for r := range removed {
			if r < link {
				shift++
			}
		}
		if shift > 0 {
			if shift > 65535 {
				return nil, false, fmt.Errorf("重定位偏移超出 ELF64 节索引范围")
			}
			if pos < 0 {
				return nil, false, fmt.Errorf("无效的节索引 %d", pos)
			}
			fixes = append(fixes, linkFix{pos: uint64(pos), newLink: sec.link - uint32(shift)})
		}
	}

	result := append([]byte(nil), raw...)
	for newPos, index := range kept {
		dst := result[shoff+uint64(newPos)*shdrSize:][:shdrSize]
		copy(dst, sections[index].raw)
	}
	for _, fix := range fixes {
		entry := result[shoff+fix.pos*shdrSize:][:shdrSize]
		binary.LittleEndian.PutUint32(entry[shLink:], fix.newLink)
	}
	head := result[:ehdrSize]
	binary.LittleEndian.PutUint64(head[offShoff:], shoff)
	binary.LittleEndian.PutUint16(head[offShnum:], keptCount)
	if shstrndx != sectionNull {
		if removed[shstrndx] {

			shstrndx = sectionNull
		} else {
			shift := 0
			for r := range removed {
				if r < shstrndx {
					shift++
				}
			}
			shstrndx -= shift
		}
	}
	binary.LittleEndian.PutUint16(head[offShstrndx:], uint16(shstrndx))

	_, _, _, after, err := parseSections(result)
	if err != nil {
		return nil, false, fmt.Errorf("剥离后的映像无法解析: %w", err)
	}
	for _, sec := range after {
		if sec.typ == sectionSymtab {
			return nil, false, errors.New("剥离后的映像仍有符号表")
		}
	}
	return result, true, nil
}

func parseSections(raw []byte) (shoff uint64, shnum int, shstrndx int, sections []sectionHeader, err error) {
	if len(raw) < 4 || raw[0] != identMagic0 || raw[1] != 'E' || raw[2] != 'L' || raw[3] != 'F' {
		return 0, 0, 0, nil, errNotELF
	}
	if len(raw) < ehdrSize {
		return 0, 0, 0, nil, fmt.Errorf("%w：%s", errNotELF, "文件头被截断")
	}
	if class := raw[4]; class != classELF64 {
		return 0, 0, 0, nil, fmt.Errorf("%w：映像不是 64 位（class %d）", errNotELF, class)
	}
	if data := raw[5]; data != dataLittleEndian {
		return 0, 0, 0, nil, fmt.Errorf("%w：映像不是小端（data %d）", errNotELF, data)
	}
	if raw[6] != versionCurrent {
		return 0, 0, 0, nil, fmt.Errorf("%w：e_ident 版本 %d 不是 %d", errMalformed, raw[6], versionCurrent)
	}
	shoff = binary.LittleEndian.Uint64(raw[offShoff:])
	shentsize := binary.LittleEndian.Uint16(raw[offShent:])
	shnum = int(binary.LittleEndian.Uint16(raw[offShnum:]))
	shstrndx = int(binary.LittleEndian.Uint16(raw[offShstrndx:]))
	if shnum < 0 {
		return 0, 0, 0, nil, fmt.Errorf("%w：节数量无效", errMalformed)
	}
	if shnum == 0 {
		return shoff, 0, shstrndx, nil, nil
	}
	if shentsize != shdrSize {
		return 0, 0, 0, nil, fmt.Errorf("%w：e_shentsize %d 不是 %d", errMalformed, shentsize, shdrSize)
	}
	if shoff+uint64(shnum)*shdrSize > uint64(len(raw)) || shoff+uint64(shnum)*shdrSize < shoff {
		return 0, 0, 0, nil, fmt.Errorf("%w：节头表超出映像", errMalformed)
	}
	sections = make([]sectionHeader, shnum)
	for i := range sections {
		entry := raw[shoff+uint64(i)*shdrSize:][:shdrSize]
		sections[i] = sectionHeader{
			raw:  entry,
			typ:  binary.LittleEndian.Uint32(entry[shType:]),
			link: binary.LittleEndian.Uint32(entry[shLink:]),
		}
	}
	return shoff, shnum, shstrndx, sections, nil
}

var errOverBudget = errors.New("累计输入超过 4 MiB")

func readFile(ctx context.Context, system vmfs.System, name string, budget int) ([]byte, int, error) {
	if budget < 0 {
		return nil, 0, errOverBudget
	}
	file, err := system.Open(ctx, name, vmfs.ReadOnly, 0)
	if err != nil {
		return nil, 0, err
	}

	raw, err := io.ReadAll(io.LimitReader(file, int64(budget+1)))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, 0, err
	}
	if len(raw) > budget {
		return nil, len(raw), errOverBudget
	}
	return raw, len(raw), nil
}

func writeViaTemp(ctx context.Context, system vmfs.System, target string, content []byte) error {
	perm := stdfs.FileMode(0o666)
	if info, err := system.Stat(ctx, target); err == nil {
		perm = info.Mode().Perm()
	}
	file, tempName, err := createTemp(ctx, system, path.Dir(target))
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = system.Remove(ctx, tempName)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := system.Rename(ctx, tempName, target); err != nil {
		return err
	}
	committed = true

	if modes, ok := system.(vmfs.Modes); ok {
		if err := modes.Chmod(ctx, target, perm); err != nil {
			return err
		}
	}
	return nil
}

func createTemp(ctx context.Context, system vmfs.System, dir string) (vmfs.File, string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		name := path.Join(dir, fmt.Sprintf(".strip-%d", attempt))
		file, err := system.Open(ctx, name, vmfs.WriteOnly|vmfs.Create|vmfs.Exclusive, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !isExist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("无法创建临时文件")
}

func isExist(err error) bool {
	return errors.Is(err, errno.Exist) || errors.Is(err, stdfs.ErrExist)
}
