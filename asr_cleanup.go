package main

import (
	"strings"
	"unicode"
)

func repRepetitionKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func collapseRepeatedTokens(text string, minRun int) string {
	if minRun < 2 {
		minRun = 2
	}
	lines := strings.Split(text, "\n")
	for li, line := range lines {
		toks := strings.Fields(line)
		out := make([]string, 0, len(toks))
		i := 0
		for i < len(toks) {
			ki := repRepetitionKey(toks[i])
			j := i + 1
			for j < len(toks) && ki != "" && repRepetitionKey(toks[j]) == ki {
				j++
			}
			if j-i >= minRun {
				out = append(out, toks[i])
			} else {
				out = append(out, toks[i:j]...)
			}
			i = j
		}
		for k, t := range out {
			if containsCJK(t) {
				out[k] = collapseRepeatedCJK(t, minRun)
			}
		}
		if joined := strings.Join(out, " "); joined != strings.Join(toks, " ") {
			lines[li] = joined
		}
	}
	return strings.Join(lines, "\n")
}

const cjkRepeatMaxUnit = 32

func collapseRepeatedCJK(s string, minRun int) string {
	if minRun < 2 {
		minRun = 2
	}
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for i := 0; i < len(r); {
		unit := 0
		maxUnit := min((len(r)-i)/minRun, cjkRepeatMaxUnit)
		for n := 2; n <= maxUnit; n++ {
			if cjkRepeatCount(r, i, n) >= minRun {
				unit = n
				break
			}
		}
		if unit == 0 {
			out = append(out, r[i])
			i++
			continue
		}
		out = append(out, r[i:i+unit]...)
		i += cjkRepeatCount(r, i, unit) * unit
	}
	return string(out)
}

func cjkRepeatCount(r []rune, i, unit int) int {
	if unit <= 0 {
		return 1
	}
	reps := 1
	for j := i + unit; j+unit <= len(r); j += unit {
		if !runesEqual(r[i:i+unit], r[j:j+unit]) {
			break
		}
		reps++
	}
	return reps
}

func runesEqual(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func collapseASRRepetitions(entries []SubtitleEntry) []SubtitleEntry {
	for i := range entries {
		entries[i].Text = collapseRepeatedTokens(entries[i].Text, 4)
	}
	if len(entries) < 3 {
		return entries
	}
	var out []SubtitleEntry
	i := 0
	for i < len(entries) {
		ki := repRepetitionKey(entries[i].Text)
		j := i + 1
		for j < len(entries) && ki != "" && repRepetitionKey(entries[j].Text) == ki {
			j++
		}
		if j-i >= 3 {
			out = append(out, entries[i])
		} else {
			out = append(out, entries[i:j]...)
		}
		i = j
	}
	return out
}

func runeLenNoSpace(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

func asrEndsSentence(s string) bool {
	s = strings.TrimRight(strings.TrimSpace(s), `"'”’)）」』`)
	if s == "" {
		return false
	}
	r := []rune(s)
	switch r[len(r)-1] {
	case '.', '!', '?', '…', '。', '！', '？':
		return true
	}
	return false
}

func dropASRHallucinations(entries []SubtitleEntry) []SubtitleEntry {
	const (
		maxWordsPerSec = 14
		minWords       = 4
		maxCJKPerSec   = 20
		minCJKChars    = 8
	)
	out := entries[:0:0]
	for _, e := range entries {
		dur := subTimeToMs(e.End, e.Format) - subTimeToMs(e.Start, e.Format)
		if dur <= 0 {
			out = append(out, e)
			continue
		}
		sec := float64(dur) / 1000.0
		if n := cjkRuneCount(e.Text); n >= minCJKChars && float64(n)/sec > maxCJKPerSec {
			continue
		}
		if n := len(strings.Fields(e.Text)); n >= minWords && float64(n)/sec > maxWordsPerSec {
			continue
		}
		out = append(out, e)
	}
	return out
}

var edgeHallucinationCanned = []string{
	"thank you", "thanks for watching", "thank you for watching",
	"please subscribe", "subscribe to", "like and subscribe",
	"subtitles by", "subtitled by", "transcript", "transcribed by",
	"transcription by", "captioning by", "captions by",
	"amara.org", "all rights reserved",
	"感谢观看", "谢谢观看", "谢谢大家", "请订阅", "字幕由", "字幕组",
}

const edgeHallSymbols = "♪♫♬♩¶¦©®"

func edgeHallSignal(e SubtitleEntry) (weak, strong bool) {
	t := strings.TrimSpace(e.Text)
	if t == "" {
		return true, false
	}
	low := strings.ToLower(t)

	if strings.ContainsAny(t, edgeHallSymbols) {
		strong = true
	}
	letters, total := 0, 0
	for _, r := range t {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}
	if total >= 4 && float64(letters)/float64(total) < 0.5 {
		strong = true
	}
	if dur := subTimeToMs(e.End, e.Format) - subTimeToMs(e.Start, e.Format); dur > 15000 {
		strong = true
	}
	if collapseRepeatedTokens(t, 3) != t {
		strong = true
	}
	for _, p := range edgeHallucinationCanned {
		if strings.Contains(low, p) {
			weak = true
			break
		}
	}
	if strong {
		weak = true
	}
	return weak, strong
}

func dropEdgeHallucinations(entries []SubtitleEntry) []SubtitleEntry {
	const (
		headWindow = 5
		tailWindow = 8
	)
	if len(entries) < 2 {
		return entries
	}
	remove := make([]bool, len(entries))

	{
		i, strong := 0, false
		for i < len(entries) && i < headWindow {
			w, s := edgeHallSignal(entries[i])
			if !w {
				break
			}
			strong = strong || s
			i++
		}
		if strong {
			for k := 0; k < i; k++ {
				remove[k] = true
			}
		}
	}

	{
		n := len(entries)
		i, strong := n-1, false
		for i >= 0 && n-1-i < tailWindow {
			w, s := edgeHallSignal(entries[i])
			if !w {
				break
			}
			strong = strong || s
			i--
		}
		if strong {
			for k := i + 1; k < n; k++ {
				remove[k] = true
			}
		}
	}

	out := entries[:0:0]
	for i, e := range entries {
		if !remove[i] {
			out = append(out, e)
		}
	}
	return out
}

func mergeShortASREntries(entries []SubtitleEntry) []SubtitleEntry {
	if len(entries) < 2 {
		return entries
	}
	const (
		maxLineChars = 42
		maxMergedDur = 5000
		maxGapMs     = 500
	)
	out := []SubtitleEntry{entries[0]}
	for i := 1; i < len(entries); i++ {
		prev := &out[len(out)-1]
		cur := entries[i]
		pStart := subTimeToMs(prev.Start, prev.Format)
		pEnd := subTimeToMs(prev.End, prev.Format)
		cStart := subTimeToMs(cur.Start, cur.Format)
		cEnd := subTimeToMs(cur.End, cur.Format)
		gap := cStart - pEnd
		pLen := runeLenNoSpace(prev.Text)
		cLen := runeLenNoSpace(cur.Text)
		if gap >= 0 && gap <= maxGapMs &&
			pLen > 0 && cLen > 0 && pLen+cLen <= maxLineChars &&
			cEnd-pStart <= maxMergedDur && !asrEndsSentence(prev.Text) {
			prev.Text = joinASRText(prev.Text, cur.Text)
			prev.End = cur.End
			continue
		}
		out = append(out, cur)
	}
	return out
}

func enforceASRTiming(entries []SubtitleEntry) int {
	const (
		baseMs   = 500
		perChar  = 66
		minDurMs = 833
		maxDurMs = 7000
		minGapMs = 83
		hardMin  = 250
	)
	starts := make([]int, len(entries))
	ends := make([]int, len(entries))
	for i := range entries {
		starts[i] = subTimeToMs(entries[i].Start, entries[i].Format)
		ends[i] = subTimeToMs(entries[i].End, entries[i].Format)
	}
	changed := 0
	for i := range entries {
		s := starts[i]
		dur := baseMs + runeLenNoSpace(entries[i].Text)*perChar
		if dur < minDurMs {
			dur = minDurMs
		}
		if dur > maxDurMs {
			dur = maxDurMs
		}
		end := s + dur
		if i+1 < len(entries) {
			if limit := starts[i+1] - minGapMs; end > limit {
				end = limit
			}
		}
		if end < s+hardMin {
			end = s + hardMin
		}
		if end != ends[i] {
			changed++
		}
		ends[i] = end
	}
	for i := range entries {
		entries[i].Start = msToSubTime(starts[i], entries[i].Format)
		entries[i].End = msToSubTime(ends[i], entries[i].Format)
	}
	return changed
}
