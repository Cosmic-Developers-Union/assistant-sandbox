package patch

import "strings"

type ApplyOptions struct {
	Reverse          bool
	Fuzz             int
	IgnoreWhitespace bool
	Force            bool
}

type HunkOutcome int

const (
	HunkApplied HunkOutcome = iota
	HunkAppliedFuzzy
	HunkAlreadyApplied
	HunkFailed
)

type HunkReport struct {
	Index    int
	Outcome  HunkOutcome
	FuzzUsed int
	At       int
}

type Result struct {
	Lines          []string
	NoFinalNewline bool
	Reports        []HunkReport

	Rejects []Hunk
}

func (r Result) AllApplied() bool {
	for _, rep := range r.Reports {
		if rep.Outcome == HunkFailed {
			return false
		}
	}
	return true
}

func Apply(oldLines []string, oldNoFinalNewline bool, hunks []Hunk, opts ApplyOptions) Result {
	work := make([]Hunk, len(hunks))
	for i, h := range hunks {
		if opts.Reverse {
			work[i] = reverseHunk(h)
		} else {
			work[i] = h
		}
	}

	var out []string
	finalNoEOL := oldNoFinalNewline
	cur := 0
	offset := 0

	appendVerbatim := func(from, to int) {
		if to <= from {
			return
		}
		out = append(out, oldLines[from:to]...)
		finalNoEOL = oldNoFinalNewline && to == len(oldLines)
	}

	var reports []HunkReport
	var rejects []Hunk

	for idx, h := range work {
		expected := h.OldStart + offset
		matchStart, peelUsed, outcome := locateHunk(oldLines, h, expected, opts)

		switch outcome {
		case HunkFailed:
			var end int
			if matchStart < cur {

				matchStart = cur
				end = cur
			} else {
				end = min(matchStart+h.OldCount, len(oldLines))
			}
			appendVerbatim(cur, matchStart)
			appendVerbatim(matchStart, end)
			cur = end
			rejects = append(rejects, h)
			reports = append(reports, HunkReport{Index: idx + 1, Outcome: HunkFailed})

		case HunkAlreadyApplied:
			end := matchStart + h.NewCount
			if end > len(oldLines) || matchStart < cur {

				reports = append(reports, HunkReport{Index: idx + 1, Outcome: HunkFailed})
				rejects = append(rejects, h)
				continue
			}
			appendVerbatim(cur, matchStart)
			appendVerbatim(matchStart, end)
			cur = end

			offset = matchStart - h.OldStart
			reports = append(reports, HunkReport{Index: idx + 1, Outcome: HunkAlreadyApplied, At: matchStart})

		default:
			if matchStart < cur {
				end := max(min(matchStart+h.OldCount, len(oldLines)), cur)
				appendVerbatim(cur, end)
				cur = end
				rejects = append(rejects, h)
				reports = append(reports, HunkReport{Index: idx + 1, Outcome: HunkFailed})
				continue
			}
			appendVerbatim(cur, matchStart)
			repl, lastNoEOL, ok := buildReplacement(oldLines, matchStart, h.Lines, oldNoFinalNewline)
			out = append(out, repl...)
			cur = matchStart + h.OldCount
			if cur == len(oldLines) && ok {
				finalNoEOL = lastNoEOL
			} else if cur >= len(oldLines) {
				finalNoEOL = false
			}
			offset = matchStart - h.OldStart
			reports = append(reports, HunkReport{Index: idx + 1, Outcome: outcome, FuzzUsed: peelUsed, At: matchStart})
		}
	}
	appendVerbatim(cur, len(oldLines))

	return Result{Lines: out, NoFinalNewline: finalNoEOL, Reports: reports, Rejects: rejects}
}

func reverseHunk(h Hunk) Hunk {
	lines := make([]HunkLine, len(h.Lines))
	for i, l := range h.Lines {
		nl := l
		switch l.Kind {
		case LineDelete:
			nl.Kind = LineAdd
		case LineAdd:
			nl.Kind = LineDelete
		}
		lines[i] = nl
	}
	return Hunk{OldStart: h.NewStart, OldCount: h.NewCount, NewStart: h.OldStart, NewCount: h.OldCount, Lines: lines}
}

func buildReplacement(oldLines []string, matchStart int, lines []HunkLine, oldNoFinalNewline bool) (repl []string, lastNoEOL bool, ok bool) {
	ti := matchStart
	lastNoEOL = oldNoFinalNewline
	sawAny := false
	for _, l := range lines {
		switch l.Kind {
		case LineContext:
			repl = append(repl, oldLines[ti])
			lastNoEOL = l.NoEOL
			sawAny = true
			ti++
		case LineDelete:
			ti++
		case LineAdd:
			repl = append(repl, l.Text)
			lastNoEOL = l.NoEOL
			sawAny = true
		}
	}
	return repl, lastNoEOL, sawAny
}

func locateHunk(oldLines []string, h Hunk, expected int, opts ApplyOptions) (matchStart, fuzzUsed int, outcome HunkOutcome) {
	leadCtx, trailCtx := edgeContextCounts(h.Lines)
	maxFuzz := max(opts.Fuzz, 0)
	for f := 0; f <= maxFuzz; f++ {
		peelLead := min(f, leadCtx)
		peelTrail := min(f, trailCtx)
		core := h.Lines[peelLead : len(h.Lines)-peelTrail]
		subOld := oldSeq(core)

		minCoreStart := peelLead
		maxCoreStart := len(oldLines) - h.OldCount + peelLead
		pos, found := searchWithin(oldLines, subOld, expected+peelLead, minCoreStart, maxCoreStart, opts.IgnoreWhitespace)
		if found {
			outcome = HunkApplied
			if f > 0 {
				outcome = HunkAppliedFuzzy
			}
			return pos - peelLead, f, outcome
		}
		if peelLead == leadCtx && peelTrail == trailCtx && f < maxFuzz {

			break
		}
	}
	if !opts.Force {
		wantNew := newSeq(h.Lines)

		if len(wantNew) > 0 {
			if pos, found := search(oldLines, wantNew, expected, opts.IgnoreWhitespace); found {
				return pos, 0, HunkAlreadyApplied
			}
		}
	}
	return clampInt(expected, 0, len(oldLines)), 0, HunkFailed
}

func edgeContextCounts(lines []HunkLine) (lead, trail int) {
	for _, l := range lines {
		if l.Kind != LineContext {
			break
		}
		lead++
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Kind != LineContext {
			break
		}
		trail++
	}
	if lead+trail > len(lines) {
		trail = len(lines) - lead
	}
	return lead, trail
}

func oldSeq(lines []HunkLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.Kind != LineAdd {
			out = append(out, l.Text)
		}
	}
	return out
}

func newSeq(lines []HunkLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.Kind != LineDelete {
			out = append(out, l.Text)
		}
	}
	return out
}

func search(target, want []string, hint int, ignoreWS bool) (int, bool) {
	return searchWithin(target, want, hint, 0, len(target)-len(want), ignoreWS)
}

func searchWithin(target, want []string, hint, minStart, maxStart int, ignoreWS bool) (int, bool) {
	l := len(want)
	sequenceMax := len(target) - l
	if minStart < 0 {
		minStart = 0
	}
	if maxStart > sequenceMax {
		maxStart = sequenceMax
	}
	if minStart > maxStart {
		return 0, false
	}
	if l == 0 {
		return clampInt(hint, minStart, maxStart), true
	}
	start := clampInt(hint, minStart, maxStart)
	for _, cand := range searchOrderWithin(start, minStart, maxStart) {
		if matchAt(target, want, cand, ignoreWS) {
			return cand, true
		}
	}
	return 0, false
}

func searchOrderWithin(start, minStart, maxStart int) []int {
	order := []int{start}
	for d := 1; start-d >= minStart || start+d <= maxStart; d++ {
		if start-d >= minStart {
			order = append(order, start-d)
		}
		if start+d <= maxStart {
			order = append(order, start+d)
		}
	}
	return order
}

func matchAt(target, want []string, pos int, ignoreWS bool) bool {
	for i, w := range want {
		t := target[pos+i]
		if ignoreWS {
			t, w = normalizeWS(t), normalizeWS(w)
		}
		if t != w {
			return false
		}
	}
	return true
}

func normalizeWS(s string) string {
	var out strings.Builder
	inBlank := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if !inBlank {
				out.WriteByte(' ')
				inBlank = true
			}
			continue
		}
		inBlank = false
		out.WriteByte(s[i])
	}
	return out.String()
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
