package ar

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	archiveMagic = "!<arch>\n"
	headerLength = 60
	nameField    = 16

	maxPlainName = nameField - 1
)

var errBadArchive = errors.New("档案格式不正确")

type member struct {
	name  string
	mtime int64
	mode  uint32
	data  []byte
}

type unsupportedMemberError struct{ name string }

func (e *unsupportedMemberError) Error() string {
	return fmt.Sprintf("包含不支持的成员段 %q：客体档案不做扩展名段（//）与对象符号表（/）", e.name)
}

func parseArchive(data []byte) ([]member, error) {
	if len(data) < len(archiveMagic) || string(data[:len(archiveMagic)]) != archiveMagic {
		return nil, errBadArchive
	}
	var members []member
	offset := len(archiveMagic)
	for offset < len(data) {
		if len(data)-offset < headerLength {
			return nil, fmt.Errorf("%w：尾部剩 %d 字节，不足一个成员头", errBadArchive, len(data)-offset)
		}
		head := data[offset : offset+headerLength]
		offset += headerLength
		parsed, size, err := parseHeader(head)
		if err != nil {
			return nil, err
		}
		if uint64(len(data)-offset) < size {
			return nil, fmt.Errorf("%w：成员 %q 的数据超出档案末尾", errBadArchive, parsed.name)
		}

		parsed.data = data[offset : offset+int(size)]
		members = append(members, parsed)
		offset += int(size)

		if size%2 == 1 {
			offset++
		}
	}
	return members, nil
}

func parseHeader(head []byte) (parsed member, size uint64, err error) {
	if string(head[58:60]) != "`\n" {
		return member{}, 0, fmt.Errorf("%w：成员头结束符不是 \"`\\n\"", errBadArchive)
	}
	rawName := strings.TrimRight(string(head[:nameField]), " ")
	name, err := parseMemberName(rawName)
	if err != nil {
		return member{}, 0, err
	}
	size, err = strconv.ParseUint(strings.TrimSpace(string(head[48:58])), 10, 63)
	if err != nil {
		return member{}, 0, fmt.Errorf("%w：成员 %q 的大小字段不是数字", errBadArchive, name)
	}

	mtime, _ := strconv.ParseInt(strings.TrimSpace(string(head[16:28])), 10, 64)

	mode, _ := strconv.ParseUint(strings.TrimSpace(string(head[40:48])), 8, 32)
	return member{name: name, mtime: mtime, mode: uint32(mode)}, size, nil
}

func parseMemberName(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("%w：成员头没有名字", errBadArchive)
	}
	if strings.HasPrefix(raw, "#1/") {
		return "", &unsupportedMemberError{name: raw}
	}
	if strings.HasPrefix(raw, "/") {
		return "", &unsupportedMemberError{name: raw}
	}
	name, _, found := strings.Cut(raw, "/")
	if !found || name == "" {
		return "", fmt.Errorf("%w：成员名 %q 没有 '/' 终止符", errBadArchive, raw)
	}

	return name, nil
}

func buildArchive(members []member) []byte {
	var out strings.Builder
	out.WriteString(archiveMagic)
	for _, m := range members {
		out.WriteString(memberHeader(m.name, m.mtime, m.mode, len(m.data)))
		out.Write(m.data)
		if len(m.data)%2 == 1 {
			out.WriteByte('\n')
		}
	}
	return []byte(out.String())
}

func memberHeader(name string, mtime int64, mode uint32, size int) string {
	var b strings.Builder
	writeField(&b, name+"/", nameField)
	writeField(&b, strconv.FormatInt(mtime, 10), 12)
	writeField(&b, "0", 6)
	writeField(&b, "0", 6)
	writeField(&b, strconv.FormatUint(uint64(mode), 8), 8)
	writeField(&b, strconv.Itoa(size), 10)
	b.WriteString("`\n")
	return b.String()
}

func writeField(b *strings.Builder, text string, width int) {
	b.WriteString(text)
	for i := len(text); i < width; i++ {
		b.WriteByte(' ')
	}
}
