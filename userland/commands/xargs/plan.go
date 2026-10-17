package xargs

import (
	"errors"
	"strings"
)

func plan(command []string, items []inputItem, o options) ([][]string, error) {
	if o.replaceSet || o.replace != "" {
		replacementArgs := 0
		for _, a := range command[1:] {
			if o.replace != "" && strings.Contains(a, o.replace) {
				replacementArgs++
			}
		}
		if replacementArgs > 5 {
			return nil, errors.New("-I 的替换串出现在超过五个参数里")
		}
		var batches [][]string
		for _, it := range items {
			argv := append([]string(nil), command...)
			for k, a := range command[1:] {
				k++
				if o.replace == "" {
					continue
				}
				argv[k] = strings.ReplaceAll(a, o.replace, it.value)
				if strings.Contains(a, o.replace) && len(argv[k]) > 255 {
					return nil, errors.New("替换后的参数超过 255 字节")
				}
			}
			if sizeLimitExceeded(argvSize(argv), o) {
				return nil, errors.New("构造出的命令行超过 -s 上限")
			}
			batches = append(batches, argv)
		}
		return batches, nil
	}

	baseSize := argvSize(command)
	if len(items) == 0 {
		if o.noRunEmpty {
			return nil, nil
		}
		if sizeLimitExceeded(baseSize, o) {
			return nil, errors.New("命令行超过 -s 上限")
		}
		return [][]string{append([]string(nil), command...)}, nil
	}
	if sizeLimitExceeded(baseSize, o) {
		return nil, errors.New("命令行超过 -s 上限")
	}
	var batches [][]string
	for start := 0; start < len(items); {
		argv := append([]string(nil), command...)
		size, lines, end := baseSize, 0, start
		lastLine := -1
		for end < len(items) {
			it := items[end]
			newLine := it.line != lastLine
			if newLine && o.maxLines > 0 && lines == o.maxLines {
				break
			}
			if o.maxArgs > 0 && end-start == o.maxArgs {
				break
			}
			itemSize := len(it.value) + 1
			tooLarge := size+itemSize > o.maxChars
			if o.strictSize {
				tooLarge = size+itemSize >= o.maxChars
			}
			if tooLarge {

				if o.exactSize || end == start {
					return nil, errors.New("构造出的命令行超过 -s 上限")
				}
				break
			}
			argv = append(argv, it.value)
			size += itemSize
			if newLine {
				lines++
				lastLine = it.line
			}
			end++
		}
		batches = append(batches, argv)
		start = end
	}
	return batches, nil
}

func argvSize(argv []string) int {
	n := 0
	for _, arg := range argv {
		n += len(arg) + 1
	}
	return n
}

func environmentSize(environment map[string]string) int {
	n := 0
	for name, value := range environment {
		n += len(name) + len(value) + 2
	}
	return n
}

func sizeLimitExceeded(size int, o options) bool {
	if o.maxChars <= 0 {
		return false
	}
	if o.strictSize {
		return size >= o.maxChars
	}
	return size > o.maxChars
}
