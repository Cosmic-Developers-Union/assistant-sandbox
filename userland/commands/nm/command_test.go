package nm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

type secEntry struct {
	name  string
	typ   uint32
	flags uint64
	data  []byte
}

type symEntry struct {
	name  string
	value uint64
	info  byte
	shndx uint16
}

func buildELF64(secs []secEntry, syms []symEntry) []byte {
	all := append([]secEntry{}, secs...)
	if syms != nil {

		strtab := []byte{0}
		table := make([]byte, 0, len(syms)*symEntrySize)
		for _, s := range syms {
			offset := len(strtab)
			strtab = append(strtab, s.name...)
			strtab = append(strtab, 0)
			entry := make([]byte, symEntrySize)
			binary.LittleEndian.PutUint32(entry[symName:], safeUint32(offset))
			entry[symInfo] = s.info
			binary.LittleEndian.PutUint16(entry[symShndx:], s.shndx)
			binary.LittleEndian.PutUint64(entry[symValue:], s.value)
			table = append(table, entry...)
		}
		all = append(all,
			secEntry{name: ".symtab", typ: sectionSymtab, data: table},
			secEntry{name: ".strtab", typ: sectionStrtab, data: strtab},
		)
	}

	shstrtab := []byte{0}
	all = append(all, secEntry{name: ".shstrtab", typ: sectionStrtab, data: shstrtab})
	shstrndx := len(all) - 1
	nameOffsets := make([]uint32, len(all))
	for i, sec := range all {
		nameOffsets[i] = safeUint32(len(shstrtab))
		shstrtab = append(shstrtab, sec.name...)
		shstrtab = append(shstrtab, 0)
	}
	all[shstrndx].data = shstrtab

	offset := uint64(ehdrSize)
	offsets := make([]uint64, len(all))
	for i, sec := range all {
		offset = (offset + 7) &^ 7
		offsets[i] = offset
		offset += uint64(len(sec.data))
	}
	shoff := (offset + 7) &^ 7

	image := make([]byte, shoff+uint64(len(all))*shdrSize)
	ident := image[:ehdrSize]
	ident[0], ident[1], ident[2], ident[3] = identMagic0, identMagicE, identMagicL, identMagicF
	ident[4], ident[5], ident[6] = classELF64, dataLittleEndian, versionCurrent
	binary.LittleEndian.PutUint16(ident[16:], 1)
	binary.LittleEndian.PutUint16(ident[18:], 62)
	binary.LittleEndian.PutUint32(ident[20:], 1)
	binary.LittleEndian.PutUint16(ident[52:], ehdrSize)
	binary.LittleEndian.PutUint16(ident[58:], shdrSize)
	binary.LittleEndian.PutUint64(ident[offShoff:], shoff)
	binary.LittleEndian.PutUint16(ident[offShnum:], safeUint16(len(all)))
	binary.LittleEndian.PutUint16(ident[offShstrndx:], safeUint16(shstrndx))

	strtabIndex := -1
	for i, sec := range all {
		if sec.name == ".strtab" {
			strtabIndex = i
		}
	}
	for i, sec := range all {
		head := image[shoff+uint64(i)*shdrSize:][:shdrSize]
		binary.LittleEndian.PutUint32(head[shName:], nameOffsets[i])
		binary.LittleEndian.PutUint32(head[shType:], sec.typ)
		binary.LittleEndian.PutUint64(head[shFlags:], sec.flags)
		binary.LittleEndian.PutUint64(head[shOffset:], offsets[i])
		binary.LittleEndian.PutUint64(head[shSize:], uint64(len(sec.data)))
		if sec.typ == sectionSymtab {
			binary.LittleEndian.PutUint32(head[shLink:], uint32(strtabIndex))
			binary.LittleEndian.PutUint64(head[shEntsize:], symEntrySize)
		}
		if sec.typ == sectionStrtab {
			binary.LittleEndian.PutUint64(head[shEntsize:], 1)
		}
		copy(image[offsets[i]:], sec.data)
	}
	return image
}

func safeUint32(n int) uint32 {
	if n < 0 || uint64(n) > uint64(^uint32(0)) {
		panic("test ELF uint32 field overflow")
	}
	return uint32(n)
}

func safeUint16(n int) uint16 {
	if n < 0 || n > int(^uint16(0)) {
		panic("test ELF uint16 field overflow")
	}
	return uint16(n)
}

func objectSections() []secEntry {
	return []secEntry{
		{name: "", typ: sectionNull},
		{name: ".text", typ: 1, flags: flagAlloc | flagExec, data: []byte("code")},
		{name: ".data", typ: 1, flags: flagAlloc | flagWrite, data: []byte("data")},
		{name: ".rodata", typ: 1, flags: flagAlloc, data: []byte("ro")},
		{name: ".bss", typ: sectionNoBits, flags: flagAlloc | flagWrite},
		{name: ".debug_info", typ: 1, data: []byte("dbg")},
	}
}

func objectSymbols() []symEntry {
	return []symEntry{
		{name: "local_fn", info: 0x00, shndx: 1},
		{name: "main", value: 0x1000, info: 0x12, shndx: 1},
		{name: "counter", value: 0x2000, info: 0x11, shndx: 2},
		{name: "local_ro", info: 0x01, shndx: 3},
		{name: "buffer", value: 0x3000, info: 0x11, shndx: 4},
		{name: "debug_var", info: 0x01, shndx: 5},
		{name: "extern_printf", info: 0x12, shndx: 0},
		{name: "abs_label", value: 0x40, info: 0x00, shndx: 0xfff1},
		{name: "common_blk", info: 0x11, shndx: 0xfff2},
		{name: "weak_data", value: 0x2000, info: 0x21, shndx: 2},
		{name: "weak_fn", value: 0x1000, info: 0x22, shndx: 1},
		{name: "readonly", value: 0x3000, info: 0x11, shndx: 3},
		{name: "orphan", info: 0x11, shndx: 99},
	}
}

func line(value uint64, undef bool, letter byte, name string) string {
	if undef {
		return fmt.Sprintf("%16s %c %s\n", "", letter, name)
	}
	return fmt.Sprintf("%16x %c %s\n", value, letter, name)
}

func writeObject(t *testing.T, dir, name string, image []byte) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), image, 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestContract(t *testing.T) {
	dir := t.TempDir()
	name := writeObject(t, dir, "obj.o", buildELF64(objectSections(), objectSymbols()))

	want := line(0x3000, false, 'B', "buffer") +
		line(0, false, 'C', "common_blk") +
		line(0x2000, false, 'D', "counter") +
		line(0, true, 'U', "extern_printf") +
		line(0x1000, false, 'T', "main") +
		line(0, false, '?', "orphan") +
		line(0x3000, false, 'R', "readonly") +
		line(0x2000, false, 'V', "weak_data") +
		line(0x1000, false, 'W', "weak_fn")
	result := commandtest.Run(t, Run, dir, []string{name}, "")
	if result.Code != 0 || result.Stdout != want {
		t.Fatalf("缺省输出:\n got\n%s\nwant\n%s\nstderr=%q", result.Stdout, want, result.Stderr)
	}
}

func TestAllAndUndefinedAndExternal(t *testing.T) {
	dir := t.TempDir()
	name := writeObject(t, dir, "obj.o", buildELF64(objectSections(), objectSymbols()))

	result := commandtest.Run(t, Run, dir, []string{"-a", name}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, line(0, false, 't', "local_fn")) ||
		!strings.Contains(result.Stdout, line(0, false, 'n', "debug_var")) ||
		!strings.Contains(result.Stdout, line(0x40, false, 'a', "abs_label")) ||
		!strings.Contains(result.Stdout, line(0, false, 'r', "local_ro")) {
		t.Fatalf("-a 输出:\n%s", result.Stdout)
	}

	got := commandtest.Run(t, Run, dir, []string{"-g", name}, "")
	def := commandtest.Run(t, Run, dir, []string{name}, "")
	if got.Code != 0 || got.Stdout != def.Stdout {
		t.Fatalf("-g 应与缺省一致:\n%s", got.Stdout)
	}

	got = commandtest.Run(t, Run, dir, []string{"-u", name}, "")
	if got.Code != 0 || got.Stdout != line(0, true, 'U', "extern_printf") {
		t.Fatalf("-u 输出:\n%q", got.Stdout)
	}
}

func TestSortOrders(t *testing.T) {
	dir := t.TempDir()
	syms := []symEntry{
		{name: "zeta", value: 0x30, info: 0x11, shndx: 1},
		{name: "alpha", value: 0x20, info: 0x11, shndx: 1},
		{name: "mid", value: 0x30, info: 0x11, shndx: 2},
	}
	image := buildELF64([]secEntry{
		{name: "", typ: sectionNull},
		{name: ".text", typ: 1, flags: flagAlloc | flagExec, data: []byte("c")},
		{name: ".data", typ: 1, flags: flagAlloc | flagWrite, data: []byte("d")},
	}, syms)
	name := writeObject(t, dir, "s.o", image)

	got := commandtest.Run(t, Run, dir, []string{name}, "")
	want := line(0x20, false, 'T', "alpha") + line(0x30, false, 'D', "mid") + line(0x30, false, 'T', "zeta")
	if got.Stdout != want {
		t.Fatalf("名字序:\n got %q\nwant %q", got.Stdout, want)
	}

	got = commandtest.Run(t, Run, dir, []string{"-n", name}, "")
	if got.Stdout != want {
		t.Fatalf("值序:\n%q", got.Stdout)
	}
	got = commandtest.Run(t, Run, dir, []string{"-nr", name}, "")
	if got.Stdout != line(0x30, false, 'T', "zeta")+line(0x30, false, 'D', "mid")+line(0x20, false, 'T', "alpha") {
		t.Fatalf("值序反转:\n%q", got.Stdout)
	}

	got = commandtest.Run(t, Run, dir, []string{"-p", name}, "")
	if got.Stdout != line(0x30, false, 'T', "zeta")+line(0x20, false, 'T', "alpha")+line(0x30, false, 'D', "mid") {
		t.Fatalf("原序:\n%q", got.Stdout)
	}
	got = commandtest.Run(t, Run, dir, []string{"-pr", name}, "")
	if got.Stdout != line(0x30, false, 'D', "mid")+line(0x20, false, 'T', "alpha")+line(0x30, false, 'T', "zeta") {
		t.Fatalf("原序倒排:\n%q", got.Stdout)
	}

	got = commandtest.Run(t, Run, dir, []string{"-v", name}, "")
	if got.Stdout != want {
		t.Fatalf("-v:\n%q", got.Stdout)
	}
}

func TestMultiFileAndPrefix(t *testing.T) {
	dir := t.TempDir()
	syms := []symEntry{{name: "one", value: 1, info: 0x11, shndx: 1}, {name: "two", info: 0x12, shndx: 0}}
	image := buildELF64([]secEntry{{name: "", typ: sectionNull}, {name: ".text", typ: 1, flags: flagAlloc | flagExec, data: []byte("c")}}, syms)
	first := writeObject(t, dir, "a.o", image)
	second := writeObject(t, dir, "b.o", image)

	got := commandtest.Run(t, Run, dir, []string{first, second}, "")
	body := line(1, false, 'T', "one") + line(0, true, 'U', "two")
	want := "\na.o:\n" + body + "\nb.o:\n" + body
	if got.Code != 0 || got.Stdout != want {
		t.Fatalf("多文件:\n got %q\nwant %q", got.Stdout, want)
	}
	got = commandtest.Run(t, Run, dir, []string{"-o", first, second}, "")
	want = "a.o:" + line(1, false, 'T', "one") + "a.o:" + line(0, true, 'U', "two") +
		"b.o:" + line(1, false, 'T', "one") + "b.o:" + line(0, true, 'U', "two")
	if got.Code != 0 || got.Stdout != want {
		t.Fatalf("-o 前缀:\n got %q\nwant %q", got.Stdout, want)
	}

	got = commandtest.Run(t, Run, dir, []string{first}, "")
	if strings.Contains(got.Stdout, "a.o:") {
		t.Fatalf("单文件不应有头:\n%s", got.Stdout)
	}
}

func TestFailurePaths(t *testing.T) {
	dir := t.TempDir()
	writeObject(t, dir, "text.o", []byte("plain text"))
	writeObject(t, dir, "nosym.o", buildELF64(objectSections(), nil))
	writeObject(t, dir, "cut.o", buildELF64(objectSections(), objectSymbols())[:40])

	bad := buildELF64(objectSections(), objectSymbols())
	binary.LittleEndian.PutUint64(bad[offShoff:], 1<<40)
	writeObject(t, dir, "bad.o", bad)

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"text.o"}, "不是 ELF64"},
		{[]string{"nosym.o"}, "没有符号表"},
		{[]string{"cut.o"}, "不是 ELF64"},
		{[]string{"bad.o"}, "ELF 格式损坏"},
		{[]string{"missing.o"}, "missing.o"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 1 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}

	goodName := writeObject(t, dir, "good.o", buildELF64(objectSections(), []symEntry{{name: "ok", info: 0x11, shndx: 1}}))
	result := commandtest.Run(t, Run, dir, []string{"text.o", goodName}, "")
	if result.Code != 1 || !strings.Contains(result.Stdout, "0 T ok") {
		t.Fatalf("多文件部分失败: %+v", result)
	}
}

func TestInputBudget(t *testing.T) {
	dir := t.TempDir()
	image := buildELF64(objectSections(), objectSymbols())
	big := append(append([]byte{}, image...), make([]byte, maxInputBytes)...)
	writeObject(t, dir, "big.o", big)
	result := commandtest.Run(t, Run, dir, []string{"big.o"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "累计输入超过 4 MiB") {
		t.Fatalf("预算: %+v", result)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{}, "需要至少一个文件操作数"},
		{[]string{"-g", "-u", "x.o"}, "-g 与 -u 不能同时使用"},
		{[]string{"-p", "-n", "x.o"}, "-p 与 -n/-v 不能同时使用"},
		{[]string{"--bogus", "x.o"}, "参数错误"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 2 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}
}

func TestParserCorners(t *testing.T) {

	image := buildELF64(objectSections(), objectSymbols())
	image[4] = 1
	if _, err := parseImage(image); !errors.Is(err, errNotELF) {
		t.Fatalf("ELF32: %v", err)
	}

	image = buildELF64(objectSections(), objectSymbols())
	image[5] = 2
	if _, err := parseImage(image); !errors.Is(err, errNotELF) {
		t.Fatalf("大端: %v", err)
	}

	bare := buildELF64(nil, nil)
	binary.LittleEndian.PutUint16(bare[offShnum:], 0)
	binary.LittleEndian.PutUint16(bare[offShstrndx:], 0)
	img, err := parseImage(bare)
	if !errors.Is(err, errNoSymtab) || img == nil {
		t.Fatalf("无节表: %v %v", err, img)
	}

	bad := buildELF64(objectSections(), objectSymbols())
	binary.LittleEndian.PutUint16(bad[offShent:], 32)
	if _, err := parseImage(bad); err == nil || !strings.Contains(err.Error(), "e_shentsize") {
		t.Fatalf("shentsize: %v", err)
	}

	small := buildELF64(objectSections(), objectSymbols())
	patchSymtab(small, func(head []byte) {
		binary.LittleEndian.PutUint64(head[shEntsize:], 8)
	})
	if _, err := parseImage(small); err == nil || !strings.Contains(err.Error(), "符号项大小") {
		t.Fatalf("entsize 过小: %v", err)
	}

	bad = buildELF64(objectSections(), objectSymbols())
	patchSymtab(bad, func(head []byte) {
		binary.LittleEndian.PutUint32(head[shLink:], 999)
	})
	if _, err := parseImage(bad); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("shlink 越界: %v", err)
	}
}

func patchSymtab(image []byte, patch func(head []byte)) {
	shoff := binary.LittleEndian.Uint64(image[offShoff:])
	shnum := int(binary.LittleEndian.Uint16(image[offShnum:]))
	for i := 0; i < shnum; i++ {
		head := image[shoff+uint64(i)*shdrSize:][:shdrSize]
		if binary.LittleEndian.Uint32(head[shType:]) == sectionSymtab {
			patch(head)
		}
	}
}

func TestCString(t *testing.T) {
	table := []byte{0, 'a', 'b', 0, 'c'}
	if got := cString(table, 1); got != "ab" {
		t.Fatalf("cString 正常: %q", got)
	}
	if got := cString(table, 0); got != "" {
		t.Fatalf("偏移 0 应为空名: %q", got)
	}
	if got := cString(table, 99); got != "" {
		t.Fatalf("越界应为空名: %q", got)
	}
	if got := cString([]byte{'x'}, 0); got != "" {
		t.Fatalf("无 NUL 尾: %q", got)
	}
}
