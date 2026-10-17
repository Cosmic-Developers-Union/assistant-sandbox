package grep

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/locale"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

var errMatched = errors.New("匹配已找到")

var errFileMatched = errors.New("本文件已有匹配")

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "grep")
	var number, invert, quiet, fixed, ignoreCase, recursive, count, only bool
	var extended, filesWithMatches, noMessages, wholeLine bool
	flags.BoolVarP(&number, "line-number", "n", false, "行号")
	flags.BoolVarP(&invert, "invert-match", "v", false, "反向")
	flags.BoolVarP(&quiet, "quiet", "q", false, "仅状态")
	flags.BoolVarP(&fixed, "fixed-strings", "F", false, "固定字符串")
	flags.BoolVarP(&ignoreCase, "ignore-case", "i", false, "忽略大小写")
	flags.BoolVarP(&recursive, "recursive", "r", false, "递归")
	flags.BoolVarP(&count, "count", "c", false, "只输出匹配行数")
	flags.BoolVarP(&only, "only-matching", "o", false, "只输出匹配部分")
	flags.BoolVarP(&extended, "extended-regexp", "E", false, "扩展正则")
	flags.BoolVarP(&filesWithMatches, "files-with-matches", "l", false, "只输出含匹配的文件名")
	flags.BoolVarP(&noMessages, "no-messages", "s", false, "不报告文件读取错误")
	flags.BoolVarP(&wholeLine, "line-regexp", "x", false, "整行匹配")
	var explicit []string
	flags.StringArrayVarP(&explicit, "regexp", "e", nil, "模式（可重复）")
	var patternFiles []string
	flags.StringArrayVarP(&patternFiles, "file", "f", nil, "从客体文件读取模式（可重复）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	files := flags.Args()
	patterns := slices.Clone(explicit)

	if len(patternFiles) > 0 {
		for _, name := range patternFiles {
			content, err := readPatternFile(ctx, name)
			if err != nil {
				if !noMessages {
					_, _ = fmt.Fprintf(h.Stderr, "grep: %s: %v\n", name, err)
				}
				return interp.ExitStatus(2)
			}
			patterns = append(patterns, splitPatterns(content)...)
		}
	}
	if len(explicit) == 0 && len(patternFiles) == 0 {

		if len(files) == 0 {
			return fmt.Errorf("grep 缺少模式")
		}
		patterns = []string{files[0]}
		files = files[1:]
	}
	if len(patterns) == 0 {

		return interp.ExitStatus(1)
	}
	matcher, err := compile(patterns, fixed, extended, ignoreCase, wholeLine)
	if err != nil {
		return fmt.Errorf("grep 正则无效: %w", err)
	}
	if len(files) == 0 {
		files = []string{"-"}
	}
	if recursive {
		files, err = expandRecursive(ctx, h.Dir, files)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return interp.ExitStatus(1)
		}
	}
	matched := false
	status := 0
	prefixFor := func(file string) string {
		if len(files) > 1 || recursive {
			return file + ":"
		}
		return ""
	}
	for _, file := range files {

		err := commandio.Inputs(ctx, []string{file}, func(name, input string) error {
			rows := commandio.Rows(input)
			hits := 0
			for index, row := range rows {
				if matcher.MatchString(row) == !invert {
					matched = true
					hits++

					if quiet {
						return errMatched
					}
					if filesWithMatches {

						if _, err := fmt.Fprintln(h.Stdout, file); err != nil {
							return err
						}
						return errFileMatched
					}
					if count {
						continue
					}
					prefix := prefixFor(file)
					if number {
						prefix += strconv.Itoa(index+1) + ":"
					}
					if only {
						for _, found := range matcher.FindAllString(row, -1) {
							if _, err := fmt.Fprintln(h.Stdout, prefix+found); err != nil {
								return err
							}
						}
						continue
					}
					if _, err := fmt.Fprintln(h.Stdout, prefix+row); err != nil {
						return err
					}
				}
			}
			if count {
				if _, err := fmt.Fprintf(h.Stdout, "%s%d\n", prefixFor(file), hits); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, errMatched) {
			return nil
		}
		if errors.Is(err, errFileMatched) {
			continue
		}
		if err != nil {
			status = 2
			if !noMessages {
				_, _ = fmt.Fprintf(h.Stderr, "grep: %s: %v\n", file, err)
			}
		}
	}
	if status != 0 {
		return interp.ExitStatus(status)
	}
	if !matched {
		return interp.ExitStatus(1)
	}
	return nil
}

func compile(patterns []string, fixed, extended, ignoreCase, wholeLine bool) (*regexp.Regexp, error) {
	translated := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		switch {
		case fixed:
			pattern = regexp.QuoteMeta(pattern)
		case !extended:
			pattern = locale.BREToERE(pattern)
		}
		if wholeLine {
			pattern = "^(?:" + pattern + ")$"
		}
		translated = append(translated, "(?:"+pattern+")")
	}
	joined := strings.Join(translated, "|")
	if ignoreCase {
		joined = "(?i)" + joined
	}
	return regexp.Compile(joined)
}

func readPatternFile(ctx context.Context, name string) (closeResult0 string, closeResult1 error) {
	file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), name), vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		closeResult1 = errors.Join(closeResult1, file.Close())
	}()

	return commandio.ReadText(ctx, file)
}

func splitPatterns(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func expandRecursive(ctx context.Context, dir string, files []string) ([]string, error) {
	expanded := make([]string, 0, len(files))
	for _, file := range files {
		if file == "-" {
			expanded = append(expanded, file)
			continue
		}
		err := commandio.Walk(ctx, commandio.Path(dir, file), func(path string, d fs.DirEntry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !d.IsDir() {
				expanded = append(expanded, path)
			}
			if len(expanded) > 4096 {
				return fmt.Errorf("grep 递归文件超过 4096 个")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return expanded, nil
}
