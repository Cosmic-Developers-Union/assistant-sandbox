package localedef

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

var categories = []string{
	"LC_CTYPE", "LC_COLLATE", "LC_MESSAGES", "LC_MONETARY", "LC_NUMERIC", "LC_TIME",
}

func isCategory(name string) bool {
	for _, category := range categories {
		if category == name {
			return true
		}
	}
	return false
}

type entry struct {
	key, value string
}

type parsed struct {
	entries []entry
}

func parse(input string) (parsed, []string, error) {
	var out parsed
	var warnings []string
	section := ""
	comment := "#"
	for number, line := range strings.Split(input, "\n") {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, comment) {
			continue
		}
		fields := strings.Fields(text)
		switch {
		case fields[0] == "comment_char" || fields[0] == "escape_char":
			if len(fields) != 2 || utf8.RuneCountInString(fields[1]) != 1 {
				return out, warnings, fmt.Errorf("第 %d 行: %s 需要单个字符操作数", number+1, fields[0])
			}
			if fields[0] == "comment_char" {
				comment = fields[1]
			}

		case fields[0] == "END":
			if section == "" {
				return out, warnings, fmt.Errorf("第 %d 行: END 没有对应的类别节", number+1)
			}
			if len(fields) != 2 || fields[1] != section {
				return out, warnings, fmt.Errorf("第 %d 行: END 与打开的类别节 %s 不匹配", number+1, section)
			}
			section = ""
		case isCategory(fields[0]):
			if section != "" {
				return out, warnings, fmt.Errorf("第 %d 行: 类别节 %s 尚未以 END 关闭", number+1, section)
			}
			section = fields[0]
		case section != "":

			value := unquote(strings.TrimSpace(text[len(fields[0]):]))
			out.entries = append(out.entries, entry{key: section + "." + fields[0], value: value})
		default:

			warnings = append(warnings, fmt.Sprintf("第 %d 行: %s: 不认识的指令", number+1, text))
		}
	}
	if section != "" {
		return out, warnings, fmt.Errorf("类别节 %s 没有以 END 关闭", section)
	}
	return out, warnings, nil
}

func unquote(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	return value
}
