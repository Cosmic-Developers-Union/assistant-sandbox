package strip

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

type secEntry struct {
	name   string
	typ    uint32
	flags  uint64
	data   []byte
	linkTo string
}

func buildELF64(secs []secEntry) []byte {
	all := append([]secEntry{}, secs...)
	shstrtab := []byte{0}
	all = append(all, secEntry{name: ".shstrtab", typ: sectionStrtab, data: shstrtab})
	shstrndx := len(all) - 1
	nameOffsets := make([]uint32, len(all))
	nameIndex := map[string]int{}
	for i, sec := range all {
		nameIndex[sec.name] = i
		nameOffsets[i] = testUint32(len(shstrtab))
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
	ident[0], ident[1], ident[2], ident[3] = identMagic0, 'E', 'L', 'F'
	ident[4], ident[5], ident[6] = classELF64, 1, versionCurrent
	binary.LittleEndian.PutUint16(ident[52:], ehdrSize)
	binary.LittleEndian.PutUint16(ident[58:], shdrSize)
	binary.LittleEndian.PutUint64(ident[offShoff:], shoff)
	binary.LittleEndian.PutUint16(ident[offShnum:], testUint16(len(all)))
	binary.LittleEndian.PutUint16(ident[offShstrndx:], testUint16(shstrndx))
	for i, sec := range all {
		head := image[shoff+uint64(i)*shdrSize:][:shdrSize]
		binary.LittleEndian.PutUint32(head[shName:], nameOffsets[i])
		binary.LittleEndian.PutUint32(head[shType:], sec.typ)
		binary.LittleEndian.PutUint64(head[shFlags:], sec.flags)
		binary.LittleEndian.PutUint64(head[shOffset:], offsets[i])
		binary.LittleEndian.PutUint64(head[shSize:], uint64(len(sec.data)))
		if sec.linkTo != "" {
			binary.LittleEndian.PutUint32(head[shLink:], testUint32(nameIndex[sec.linkTo]))
		}
		copy(image[offsets[i]:], sec.data)
	}
	return image
}

func testUint32(n int) uint32 {
	if n < 0 || uint64(n) > uint64(^uint32(0)) {
		panic("test ELF uint32 field overflow")
	}
	return uint32(n)
}

func testUint16(n int) uint16 {
	if n < 0 || n > int(^uint16(0)) {
		panic("test ELF uint16 field overflow")
	}
	return uint16(n)
}

func baseSections() []secEntry {
	return []secEntry{
		{name: "", typ: sectionNull},
		{name: ".text", typ: 1, flags: 0x6, data: []byte("code bytes")},
		{name: ".symtab", typ: sectionSymtab, data: make([]byte, symEntrySizeForTest), linkTo: ".strtab"},
		{name: ".strtab", typ: sectionStrtab, data: []byte{0, 'm', 0}},
		{name: ".data", typ: 1, flags: 0x3, data: []byte("data bytes"), linkTo: ".data2"},
		{name: ".data2", typ: 1, flags: 0x3, data: []byte("more")},
	}
}

const symEntrySizeForTest = 24

func TestStripRewritesSectionTable(t *testing.T) {
	dir := t.TempDir()
	image := buildELF64(baseSections())
	name := "obj.o"
	if err := os.WriteFile(filepath.Join(dir, name), image, 0o644); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{name}, "")
	if result.Code != 0 || result.Stdout != "" || result.Stderr != "" {
		t.Fatalf("strip 应静默成功: %+v", result)
	}
	stripped, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}

	shoff, shnum, shstrndx, sections, err := parseSections(stripped)
	if err != nil {
		t.Fatalf("剥离产物不可解析: %v", err)
	}

	if shnum != 5 || shstrndx != 4 {
		t.Fatalf("e_shnum=%d e_shstrndx=%d", shnum, shstrndx)
	}
	for _, sec := range sections {
		if sec.typ == sectionSymtab {
			t.Fatal("剥离后仍有符号表")
		}
	}

	var dataLink uint32
	for i, sec := range sections {
		if sec.typ == 1 && i == 2 {
			dataLink = sec.link
		}
	}
	if dataLink != 3 {
		t.Fatalf(".data 的 sh_link 应平移到 3，得到 %d", dataLink)
	}

	if binary.LittleEndian.Uint64(stripped[offShoff:]) != shoff {
		t.Fatal("e_shoff 不应改变")
	}
	textOffset := binary.LittleEndian.Uint64(stripped[shoff+shdrSize+shOffset:])
	if string(stripped[textOffset:textOffset+10]) != "code bytes" {
		t.Fatalf(".text 数据应原样保留: %q", stripped[textOffset:textOffset+10])
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".strip-") {
			t.Fatalf("临时文件残留: %s", entry.Name())
		}
	}
}

func TestStripIdempotent(t *testing.T) {
	dir := t.TempDir()
	image := buildELF64([]secEntry{
		{name: "", typ: sectionNull},
		{name: ".text", typ: 1, flags: 0x6, data: []byte("code")},
	})
	path := filepath.Join(dir, "clean.o")
	if err := os.WriteFile(path, image, 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"clean.o"}, "")
	if result.Code != 0 || result.Stderr != "" {
		t.Fatalf("无符号表应幂等成功: %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(image) {
		t.Fatal("幂等剥离不应改写文件")
	}
}

func TestFailurePaths(t *testing.T) {
	dir := t.TempDir()

	dangling := buildELF64([]secEntry{
		{name: "", typ: sectionNull},
		{name: ".text", typ: 1, flags: 0x6, data: []byte("code")},
		{name: ".rel.text", typ: 9, data: []byte("rel"), linkTo: ".symtab"},
		{name: ".symtab", typ: sectionSymtab, data: make([]byte, symEntrySizeForTest), linkTo: ".strtab"},
		{name: ".strtab", typ: sectionStrtab, data: []byte{0}},
	})
	if err := os.WriteFile(filepath.Join(dir, "dangling.o"), dangling, 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "text.o"), []byte("plain"), 0o644); err != nil {
		t.Error(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "short.o"), []byte("\x7FELF"), 0o644); err != nil {
		t.Error(err)
	}

	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"text.o"}, "不是 ELF64"},
		{[]string{"short.o"}, "不是 ELF64"},
		{[]string{"missing.o"}, "missing.o"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 1 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"dangling.o"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "悬挂引用") {
		t.Fatalf("悬挂引用: %+v", result)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "dangling.o"))
	if string(after) != string(dangling) {
		t.Fatal("拒绝剥离时文件应保持原样")
	}

	if err := os.WriteFile(filepath.Join(dir, "ok.o"), buildELF64(baseSections()), 0o644); err != nil {
		t.Error(err)
	}
	result = commandtest.Run(t, Run, dir, []string{"text.o", "ok.o"}, "")
	if result.Code != 1 {
		t.Fatalf("多文件部分失败: %+v", result)
	}
	if _, _, _, sections, err := parseSections(mustRead(t, filepath.Join(dir, "ok.o"))); err != nil || len(sections) != 5 {
		t.Fatalf("成功文件应被剥离: %v %d", err, len(sections))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{}, "缺少文件"},
		{[]string{"-s", "a.o"}, "不支持的选项"},
		{[]string{"-e", "a.o"}, "不支持的选项"},
		{[]string{"--strip-all", "a.o"}, "不支持的选项"},
	} {
		result := commandtest.Run(t, Run, dir, test.args, "")
		if result.Code != 2 || !strings.Contains(result.Stderr, test.want) {
			t.Fatalf("%v: code=%d stderr=%q", test.args, result.Code, result.Stderr)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "-weird"), buildELF64(baseSections()), 0o644); err != nil {
		t.Error(err)
	}
	if result := commandtest.Run(t, Run, dir, []string{"--", "-weird"}, ""); result.Code != 0 {
		t.Fatalf("-- 应终结选项: %+v", result)
	}
}

func TestInputBudget(t *testing.T) {
	dir := t.TempDir()
	image := append(append([]byte{}, buildELF64(baseSections())...), make([]byte, maxInputBytes)...)
	if err := os.WriteFile(filepath.Join(dir, "big.o"), image, 0o644); err != nil {
		t.Error(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"big.o"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "累计输入超过 4 MiB") {
		t.Fatalf("预算: %+v", result)
	}
}

func TestTempCreationExhausted(t *testing.T) {
	dir := t.TempDir()
	image := buildELF64(baseSections())
	if err := os.WriteFile(filepath.Join(dir, "obj.o"), image, 0o644); err != nil {
		t.Error(err)
	}
	for i := 0; i < 64; i++ {
		name := filepath.Join(dir, ".strip-"+itoa(i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result := commandtest.Run(t, Run, dir, []string{"obj.o"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "无法创建临时文件") {
		t.Fatalf("临时文件耗尽: %+v", result)
	}
}

func TestParseSectionsCorners(t *testing.T) {
	image := buildELF64(baseSections())

	if _, _, _, _, err := parseSections([]byte("\x7F")); !errors.Is(err, errNotELF) {
		t.Fatalf("短输入: %v", err)
	}
	if _, _, _, _, err := parseSections([]byte("\x7FELF")); !errors.Is(err, errNotELF) {
		t.Fatalf("截断头: %v", err)
	}

	bad := append([]byte(nil), image...)
	bad[4] = 1
	if _, _, _, _, err := parseSections(bad); !errors.Is(err, errNotELF) {
		t.Fatalf("ELF32: %v", err)
	}
	bad = append([]byte(nil), image...)
	bad[5] = 2
	if _, _, _, _, err := parseSections(bad); !errors.Is(err, errNotELF) {
		t.Fatalf("大端: %v", err)
	}

	bad = append([]byte(nil), image...)
	bad[6] = 9
	if _, _, _, _, err := parseSections(bad); !errors.Is(err, errMalformed) {
		t.Fatalf("版本: %v", err)
	}

	bad = append([]byte(nil), image...)
	binary.LittleEndian.PutUint16(bad[offShent:], 32)
	if _, _, _, _, err := parseSections(bad); !errors.Is(err, errMalformed) {
		t.Fatalf("shentsize: %v", err)
	}

	bad = append([]byte(nil), image...)
	binary.LittleEndian.PutUint64(bad[offShoff:], 1<<40)
	if _, _, _, _, err := parseSections(bad); !errors.Is(err, errMalformed) {
		t.Fatalf("shoff 越界: %v", err)
	}

	bare := buildELF64(nil)
	binary.LittleEndian.PutUint16(bare[offShnum:], 0)
	binary.LittleEndian.PutUint16(bare[offShstrndx:], 0)
	shoff, shnum, shstrndx, sections, err := parseSections(bare)
	if err != nil || shnum != 0 || len(sections) != 0 || shstrndx != 0 || shoff == 0 {
		t.Fatalf("无节表: %v %d %d", err, shnum, shstrndx)
	}
}
