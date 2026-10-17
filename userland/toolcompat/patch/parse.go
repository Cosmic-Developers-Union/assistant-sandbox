package patch

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type rawLine struct {
	text  string
	noEOL bool
}

func splitRawLines(data []byte) []rawLine {
	s := string(data)
	trailingNL := strings.HasSuffix(s, "\n")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if trailingNL {
		parts = parts[:len(parts)-1]
	}
	out := make([]rawLine, len(parts))
	for i, p := range parts {
		out[i].text = strings.TrimSuffix(p, "\r")
	}
	if !trailingNL && len(out) > 0 {
		out[len(out)-1].noEOL = true
	}
	return out
}

func stripPatchIndentAt(lines []rawLine, start int) bool {
	trimmed := strings.TrimLeft(lines[start].text, " \t")
	if trimmed == lines[start].text || !isPatchControlLine(trimmed) {
		return false
	}
	prefix := lines[start].text[:len(lines[start].text)-len(trimmed)]
	for i := start; i < len(lines); i++ {
		lines[i].text = strings.TrimPrefix(lines[i].text, prefix)
	}
	return true
}

func isPatchControlLine(line string) bool {
	return strings.HasPrefix(line, "Index:") || strings.HasPrefix(line, "diff --git ") ||
		strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "*** ") || normalHunkRe.MatchString(line)
}

var (
	unifiedHunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	contextSepRe  = regexp.MustCompile(`^\*{9,}\s*$`)
	contextOldRe  = regexp.MustCompile(`^\*\*\* (\d+)(?:,(\d+))? \*\*\*\*\s*$`)
	contextNewRe  = regexp.MustCompile(`^--- (\d+)(?:,(\d+))? ----\s*$`)
	normalHunkRe  = regexp.MustCompile(`^(\d+)(?:,(\d+))?([acd])(\d+)(?:,(\d+))?$`)
	gitDiffRe     = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)
)

func Parse(data []byte) (*Patch, error) {
	lines := splitRawLines(data)
	n := len(lines)
	var files []FilePatch
	var indexName string
	for i := 0; i < n; {
		if stripPatchIndentAt(lines, i) {
			continue
		}
		switch {
		case strings.HasPrefix(lines[i].text, "Index:"):
			indexName = strings.TrimSpace(strings.TrimPrefix(lines[i].text, "Index:"))
			i++
		case strings.HasPrefix(lines[i].text, "diff --git "):
			fp, ni, err := parseGitWrappedFile(lines, i)
			if err != nil {
				return nil, err
			}
			fp.IndexName = indexName
			files = append(files, fp)
			indexName = ""
			i = ni
		case strings.HasPrefix(lines[i].text, "--- ") && i+1 < n && strings.HasPrefix(lines[i+1].text, "+++ "):
			fp, ni, err := parseUnifiedFile(lines, i)
			if err != nil {
				return nil, err
			}
			fp.IndexName = indexName
			files = append(files, fp)
			indexName = ""
			i = ni
		case strings.HasPrefix(lines[i].text, "*** ") && i+1 < n && strings.HasPrefix(lines[i+1].text, "--- "):
			fp, ni, err := parseContextFile(lines, i)
			if err != nil {
				return nil, err
			}
			fp.IndexName = indexName
			files = append(files, fp)
			indexName = ""
			i = ni
		case normalHunkRe.MatchString(lines[i].text):
			fp, ni, err := parseNormalFile(lines, i)
			if err != nil {
				return nil, err
			}
			fp.IndexName = indexName
			files = append(files, fp)
			indexName = ""
			i = ni
		default:
			i++
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no recognizable patch hunks found in input")
	}
	return &Patch{Files: files}, nil
}

func extractName(rest string) string {
	if name, _, ok := strings.Cut(rest, "\t"); ok {
		return name
	}
	return strings.TrimRight(rest, " \t")
}

func unifiedRangeVal(num int, hasCount bool, count int) (start0, n int) {
	if !hasCount {
		return num - 1, 1
	}
	if count == 0 {
		return num, 0
	}
	return num - 1, count
}

func parseUnifiedFile(lines []rawLine, i int) (FilePatch, int, error) {
	oldName := extractName(lines[i].text[len("--- "):])
	newName := extractName(lines[i+1].text[len("+++ "):])
	i += 2
	n := len(lines)
	var hunks []Hunk
	for i < n && strings.HasPrefix(lines[i].text, "@@ ") {
		m := unifiedHunkRe.FindStringSubmatch(lines[i].text)
		if m == nil {
			return FilePatch{}, 0, fmt.Errorf("patch: malformed unified hunk header %q", lines[i].text)
		}
		oldNum, _ := strconv.Atoi(m[1])
		oldStart0, oldCount := unifiedRangeVal(oldNum, m[2] != "", atoiDefault(m[2], 0))
		newNum, _ := strconv.Atoi(m[3])
		newStart0, newCount := unifiedRangeVal(newNum, m[4] != "", atoiDefault(m[4], 0))
		i++

		var hl []HunkLine
		oldSeen, newSeen := 0, 0
		for oldSeen < oldCount || newSeen < newCount {
			if i >= n {
				return FilePatch{}, 0, fmt.Errorf("patch: unexpected end of input inside a hunk for %q", newName)
			}
			text := lines[i].text
			switch {
			case len(text) > 0 && text[0] == '\\':
				if len(hl) > 0 {
					hl[len(hl)-1].NoEOL = true
				}
				i++
				continue
			case text == "" || text[0] == ' ':
				body := ""
				if len(text) > 0 {
					body = text[1:]
				}
				hl = append(hl, HunkLine{Kind: LineContext, Text: body, NoEOL: lines[i].noEOL})
				oldSeen++
				newSeen++
			case text[0] == '-':
				hl = append(hl, HunkLine{Kind: LineDelete, Text: text[1:], NoEOL: lines[i].noEOL})
				oldSeen++
			case text[0] == '+':
				hl = append(hl, HunkLine{Kind: LineAdd, Text: text[1:], NoEOL: lines[i].noEOL})
				newSeen++
			default:
				return FilePatch{}, 0, fmt.Errorf("patch: unexpected line %q inside a unified hunk", text)
			}
			i++
		}

		for i < n && len(lines[i].text) > 0 && lines[i].text[0] == '\\' {
			if len(hl) > 0 {
				hl[len(hl)-1].NoEOL = true
			}
			i++
		}
		hunks = append(hunks, Hunk{OldStart: oldStart0, OldCount: oldCount, NewStart: newStart0, NewCount: newCount, Lines: hl})
	}
	return FilePatch{OldName: oldName, NewName: newName, Format: FormatUnified, Hunks: hunks}, i, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func contextRangeVal(hasSecond bool, first, second int) (start0Explicit bool, start0 int, countFromHeader int) {
	if hasSecond {
		return true, first - 1, second - first + 1
	}
	return false, 0, first
}

type markedLine struct {
	mark  string
	text  string
	noEOL bool
}

func consumeMarkedBlock(lines []rawLine, i int, marks map[string]bool) ([]markedLine, int) {
	var out []markedLine
	for i < len(lines) {
		text := lines[i].text
		if len(text) > 0 && text[0] == '\\' {
			if len(out) > 0 {
				out[len(out)-1].noEOL = true
			}
			i++
			continue
		}
		if len(text) < 2 || !marks[text[:2]] {
			break
		}
		out = append(out, markedLine{mark: text[:2], text: text[2:], noEOL: lines[i].noEOL})
		i++
	}
	return out, i
}

var oldBlockMarks = map[string]bool{"  ": true, "- ": true, "! ": true}
var newBlockMarks = map[string]bool{"  ": true, "+ ": true, "! ": true}

func parseContextFile(lines []rawLine, i int) (FilePatch, int, error) {
	oldName := extractName(lines[i].text[len("*** "):])
	newName := extractName(lines[i+1].text[len("--- "):])
	i += 2
	n := len(lines)
	var hunks []Hunk
	for i < n && contextSepRe.MatchString(lines[i].text) {
		i++
		if i >= n {
			return FilePatch{}, 0, errors.New("patch: unexpected end of input after a context hunk separator")
		}
		om := contextOldRe.FindStringSubmatch(lines[i].text)
		if om == nil {
			return FilePatch{}, 0, fmt.Errorf("patch: expected a context old-range header, got %q", lines[i].text)
		}
		oldFirst, _ := strconv.Atoi(om[1])
		oldHasSecond, oldSecondStr := om[2] != "", om[2]
		oldSecond := atoiDefault(oldSecondStr, 0)
		i++

		var oldBlock []markedLine
		if i < n && !contextNewRe.MatchString(lines[i].text) {
			oldBlock, i = consumeMarkedBlock(lines, i, oldBlockMarks)
		}
		if i >= n {
			return FilePatch{}, 0, errors.New("patch: expected a context new-range header, got end of input")
		}
		nm := contextNewRe.FindStringSubmatch(lines[i].text)
		if nm == nil {
			return FilePatch{}, 0, fmt.Errorf("patch: expected a context new-range header, got %q", lines[i].text)
		}
		newFirst, _ := strconv.Atoi(nm[1])
		newHasSecond, newSecondStr := nm[2] != "", nm[2]
		newSecond := atoiDefault(newSecondStr, 0)
		i++

		var newBlock []markedLine
		if i < n && !contextSepRe.MatchString(lines[i].text) {
			newBlock, i = consumeMarkedBlock(lines, i, newBlockMarks)
		}

		oldStart0, oldCount := resolveContextStart(oldHasSecond, oldFirst, oldSecond, len(oldBlock))
		newStart0, newCount := resolveContextStart(newHasSecond, newFirst, newSecond, len(newBlock))

		hl := zipContextBlocks(oldBlock, newBlock)
		hunks = append(hunks, Hunk{OldStart: oldStart0, OldCount: oldCount, NewStart: newStart0, NewCount: newCount, Lines: hl})
	}
	return FilePatch{OldName: oldName, NewName: newName, Format: FormatContext, Hunks: hunks}, i, nil
}

func resolveContextStart(hasSecond bool, first, second, actualCount int) (start0, count int) {
	explicit, s0, headerCount := contextRangeVal(hasSecond, first, second)
	if explicit {

		return s0, headerCount
	}

	return first - actualCount, actualCount
}

func zipContextBlocks(oldBlock, newBlock []markedLine) []HunkLine {
	var hl []HunkLine
	oi, ni := 0, 0
	for oi < len(oldBlock) || ni < len(newBlock) {
		if oi < len(oldBlock) && oldBlock[oi].mark == "  " {
			hl = append(hl, HunkLine{Kind: LineContext, Text: oldBlock[oi].text, NoEOL: oldBlock[oi].noEOL})
			oi++
			if ni < len(newBlock) && newBlock[ni].mark == "  " {
				ni++
			}
			continue
		}
		for oi < len(oldBlock) && oldBlock[oi].mark != "  " {
			hl = append(hl, HunkLine{Kind: LineDelete, Text: oldBlock[oi].text, NoEOL: oldBlock[oi].noEOL})
			oi++
		}
		for ni < len(newBlock) && newBlock[ni].mark != "  " {
			hl = append(hl, HunkLine{Kind: LineAdd, Text: newBlock[ni].text, NoEOL: newBlock[ni].noEOL})
			ni++
		}
		if oi >= len(oldBlock) && ni < len(newBlock) && newBlock[ni].mark == "  " {

			hl = append(hl, HunkLine{Kind: LineContext, Text: newBlock[ni].text, NoEOL: newBlock[ni].noEOL})
			ni++
		}
	}
	return hl
}

func parseNormalFile(lines []rawLine, i int) (FilePatch, int, error) {
	n := len(lines)
	var hunks []Hunk
	for i < n {
		m := normalHunkRe.FindStringSubmatch(lines[i].text)
		if m == nil {
			break
		}
		oldFirst, _ := strconv.Atoi(m[1])
		oldHasSecond := m[2] != ""
		oldSecond := atoiDefault(m[2], 0)
		op := m[3]
		newFirst, _ := strconv.Atoi(m[4])
		newHasSecond := m[5] != ""
		newSecond := atoiDefault(m[5], 0)
		i++

		oldCount := 1
		if oldHasSecond {
			oldCount = oldSecond - oldFirst + 1
		}
		newCount := 1
		if newHasSecond {
			newCount = newSecond - newFirst + 1
		}

		var hl []HunkLine
		var oldStart0, newStart0 int
		var err error
		switch op {
		case "a":
			oldStart0, oldCount = oldFirst, 0
			newStart0 = newFirst - 1
			var adds []markedLine
			adds, i, err = consumeFixedMarked(lines, i, "> ", newCount)
			if err != nil {
				return FilePatch{}, 0, err
			}
			for _, l := range adds {
				hl = append(hl, HunkLine{Kind: LineAdd, Text: l.text, NoEOL: l.noEOL})
			}
		case "d":
			oldStart0 = oldFirst - 1
			newStart0, newCount = newFirst, 0
			var dels []markedLine
			dels, i, err = consumeFixedMarked(lines, i, "< ", oldCount)
			if err != nil {
				return FilePatch{}, 0, err
			}
			for _, l := range dels {
				hl = append(hl, HunkLine{Kind: LineDelete, Text: l.text, NoEOL: l.noEOL})
			}
		case "c":
			oldStart0 = oldFirst - 1
			newStart0 = newFirst - 1
			var dels []markedLine
			dels, i, err = consumeFixedMarked(lines, i, "< ", oldCount)
			if err != nil {
				return FilePatch{}, 0, err
			}
			for _, l := range dels {
				hl = append(hl, HunkLine{Kind: LineDelete, Text: l.text, NoEOL: l.noEOL})
			}
			if i >= n || lines[i].text != "---" {
				return FilePatch{}, 0, fmt.Errorf("patch: expected '---' separator in a change hunk, got %q", peekText(lines, i))
			}
			i++
			var adds []markedLine
			adds, i, err = consumeFixedMarked(lines, i, "> ", newCount)
			if err != nil {
				return FilePatch{}, 0, err
			}
			for _, l := range adds {
				hl = append(hl, HunkLine{Kind: LineAdd, Text: l.text, NoEOL: l.noEOL})
			}
		}
		hunks = append(hunks, Hunk{OldStart: oldStart0, OldCount: oldCount, NewStart: newStart0, NewCount: newCount, Lines: hl})
	}
	if len(hunks) == 0 {
		return FilePatch{}, 0, errors.New("patch: no hunks found in normal-format diff")
	}
	return FilePatch{Format: FormatNormal, Hunks: hunks}, i, nil
}

func peekText(lines []rawLine, i int) string {
	if i >= len(lines) {
		return "<end of input>"
	}
	return lines[i].text
}

func consumeFixedMarked(lines []rawLine, i int, prefix string, want int) ([]markedLine, int, error) {
	var out []markedLine
	for len(out) < want {
		if i >= len(lines) {
			return nil, 0, fmt.Errorf("patch: expected %d %q line(s), ran out of input", want, prefix)
		}
		text := lines[i].text
		if len(text) > 0 && text[0] == '\\' {
			if len(out) > 0 {
				out[len(out)-1].noEOL = true
			}
			i++
			continue
		}
		if !strings.HasPrefix(text, prefix) {
			return nil, 0, fmt.Errorf("patch: expected a %q line, got %q", prefix, text)
		}
		out = append(out, markedLine{text: text[len(prefix):], noEOL: lines[i].noEOL})
		i++
	}
	return out, i, nil
}

func parseGitWrappedFile(lines []rawLine, i int) (FilePatch, int, error) {
	gm := gitDiffRe.FindStringSubmatch(lines[i].text)
	var gitOld, gitNew string
	if gm != nil {
		gitOld, gitNew = "a/"+gm[1], "b/"+gm[2]
	}
	i++
	n := len(lines)
	var renameFrom, renameTo string
	binary := false
	for i < n {
		text := lines[i].text
		switch {
		case strings.HasPrefix(text, "--- "), strings.HasPrefix(text, "diff --git "):
			goto doneHeaders
		case strings.HasPrefix(text, "rename from "):
			renameFrom = text[len("rename from "):]
		case strings.HasPrefix(text, "rename to "):
			renameTo = text[len("rename to "):]
		case strings.HasPrefix(text, "GIT binary patch"), strings.HasPrefix(text, "Binary files "):
			binary = true
		}
		i++
	}
doneHeaders:
	if i < n && strings.HasPrefix(lines[i].text, "--- ") && i+1 < n && strings.HasPrefix(lines[i+1].text, "+++ ") {
		fp, ni, err := parseUnifiedFile(lines, i)
		if err != nil {
			return FilePatch{}, 0, err
		}
		fp.RenameFrom, fp.RenameTo = renameFrom, renameTo
		return fp, ni, nil
	}
	reason := "git diff section has no textual hunk to apply (metadata-only or unrecognized)"
	if binary {
		reason = "binary patch content is not supported by pure-Go coreutils"
	} else if renameFrom != "" || renameTo != "" {
		reason = "a git rename with no content hunk is not supported by pure-Go coreutils"
	}
	return FilePatch{
		OldName:     gitOld,
		NewName:     gitNew,
		RenameFrom:  renameFrom,
		RenameTo:    renameTo,
		Unsupported: reason,
	}, i, nil
}
