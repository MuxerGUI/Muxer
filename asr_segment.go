package main

import (
	"fmt"
	"math"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

type asrWordTime struct {
	Text  string
	Punc  string
	Start int
	End   int
}

type asrSubtitleSeg struct {
	Start int
	End   int
	Text  string
}

const (
	asrSubtitleMaxDurationMs = 6500
	asrSubtitleMinDurationMs = 800
	asrSubtitleMaxRunes      = 34
	asrSubtitleSoftRunes     = 22
)

func asrNormalizeText(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func asrVisibleRuneCount(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

func asrIsSentencePunc(r rune) bool {
	switch r {
	case '.', '?', '!', ';', ':', ',', '。', '？', '！', '；', '：', '，', '、':
		return true
	default:
		return false
	}
}

func asrEndsWithPunc(s string) bool {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) == 0 {
		return false
	}
	return asrIsSentencePunc(runes[len(runes)-1])
}

func asrIsHardSentenceEnd(r rune) bool {
	switch r {
	case '.', '?', '!', '。', '？', '！':
		return true
	default:
		return false
	}
}

func asrEndsWithHardPunc(s string) bool {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) == 0 {
		return false
	}
	return asrIsHardSentenceEnd(runes[len(runes)-1])
}

func noSpaceScript(r rune) bool {
	switch {
	case r >= 0x3000 && r <= 0x303F:
		return true
	case r >= 0xFF00 && r <= 0xFFEF:
		return true
	}
	return isCJKRune(r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func asrShouldInsertSpace(prev, next string) bool {
	prev = strings.TrimSpace(prev)
	next = strings.TrimSpace(next)
	if prev == "" || next == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(prev)
	first, _ := utf8.DecodeRuneInString(next)
	if asrIsSentencePunc(first) {
		return false
	}
	return !noSpaceScript(last) && !noSpaceScript(first)
}

func joinASRText(prev, next string) string {
	prev, next = strings.TrimSpace(prev), strings.TrimSpace(next)
	if prev == "" {
		return next
	}
	if next == "" {
		return prev
	}
	if asrShouldInsertSpace(prev, next) {
		return prev + " " + next
	}
	return prev + next
}

func asrSplitTextChunks(text string, maxRunes int) []string {
	text = asrNormalizeText(text)
	if text == "" {
		return nil
	}
	var chunks []string
	var cur []rune
	flush := func() {
		s := strings.TrimSpace(string(cur))
		if s != "" {
			chunks = append(chunks, s)
		}
		cur = nil
	}
	for _, r := range []rune(text) {
		cur = append(cur, r)
		if asrIsSentencePunc(r) && asrVisibleRuneCount(string(cur)) >= 6 {
			flush()
			continue
		}
		if asrVisibleRuneCount(string(cur)) >= maxRunes {
			flush()
		}
	}
	flush()
	return chunks
}

func asrSplitSegmentByText(start, end int, text string) []asrSubtitleSeg {
	text = asrNormalizeText(text)
	if text == "" || end <= start {
		return nil
	}
	duration := end - start
	if duration <= asrSubtitleMaxDurationMs && asrVisibleRuneCount(text) <= asrSubtitleMaxRunes {
		return []asrSubtitleSeg{{Start: start, End: end, Text: text}}
	}
	chunks := asrSplitTextChunks(text, asrSubtitleSoftRunes)
	if len(chunks) <= 1 {
		return []asrSubtitleSeg{{Start: start, End: end, Text: text}}
	}
	totalRunes := 0
	for _, c := range chunks {
		totalRunes += max(1, asrVisibleRuneCount(c))
	}
	var out []asrSubtitleSeg
	cursor := start
	for i, c := range chunks {
		if i == len(chunks)-1 {
			if end > cursor {
				out = append(out, asrSubtitleSeg{Start: cursor, End: end, Text: c})
			}
			break
		}
		part := int(math.Round(float64(duration) * float64(max(1, asrVisibleRuneCount(c))) / float64(totalRunes)))
		if part < asrSubtitleMinDurationMs {
			part = asrSubtitleMinDurationMs
		}
		next := cursor + part
		remaining := len(chunks) - i - 1
		if end-next < remaining*asrSubtitleMinDurationMs {
			next = end - remaining*asrSubtitleMinDurationMs
		}
		if next <= cursor {
			next = cursor + 1
		}
		out = append(out, asrSubtitleSeg{Start: cursor, End: next, Text: c})
		cursor = next
	}
	return out
}

func asrSplitByWords(sentenceStart, sentenceEnd int, text string, words []asrWordTime) []asrSubtitleSeg {
	if len(words) == 0 {
		return asrSplitSegmentByText(sentenceStart, sentenceEnd, text)
	}
	var out []asrSubtitleSeg
	var curText strings.Builder
	curStart := -1
	curEnd := sentenceStart
	flush := func(forceEnd int) {
		s := asrNormalizeText(curText.String())
		if s != "" && curStart >= 0 {
			if forceEnd <= curStart {
				forceEnd = curEnd
			}
			out = append(out, asrSubtitleSeg{Start: curStart, End: forceEnd, Text: s})
		}
		curText.Reset()
		curStart = -1
	}
	for _, w := range words {
		part := strings.TrimSpace(w.Text + w.Punc)
		if part == "" {
			continue
		}
		if curStart < 0 {
			curStart = w.Start
		}
		if curText.Len() > 0 && asrShouldInsertSpace(curText.String(), part) {
			curText.WriteByte(' ')
		}
		curText.WriteString(part)
		curEnd = w.End
		curRunes := asrVisibleRuneCount(curText.String())
		hardBreak := asrEndsWithHardPunc(part) && curRunes >= 6
		softBreak := !asrEndsWithHardPunc(part) && asrEndsWithPunc(part) && curRunes >= asrSubtitleSoftRunes
		tooLong := curEnd-curStart >= asrSubtitleMaxDurationMs || curRunes >= asrSubtitleMaxRunes
		if hardBreak || softBreak || tooLong {
			flush(curEnd)
		}
	}
	if curText.Len() > 0 {
		flush(sentenceEnd)
	}
	if len(out) == 0 {
		return asrSplitSegmentByText(sentenceStart, sentenceEnd, text)
	}
	return asrMergeShortSegs(out)
}

func asrMergeShortSegs(segs []asrSubtitleSeg) []asrSubtitleSeg {
	if len(segs) <= 1 {
		return segs
	}
	out := make([]asrSubtitleSeg, 0, len(segs))
	for _, s := range segs {
		if len(out) == 0 {
			out = append(out, s)
			continue
		}
		prev := &out[len(out)-1]
		prevShort := prev.End-prev.Start < asrSubtitleMinDurationMs
		curShort := s.End-s.Start < asrSubtitleMinDurationMs
		if prevShort || curShort {
			mergedText := joinASRText(prev.Text, s.Text)
			if asrVisibleRuneCount(mergedText) <= asrSubtitleMaxRunes &&
				s.End-prev.Start <= asrSubtitleMaxDurationMs {
				prev.End = s.End
				prev.Text = mergedText
				continue
			}
		}
		out = append(out, s)
	}
	return out
}

func asrCleanSubtitleText(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	i := 0
	for i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
		i++
	}
	if i > 0 && i < len(runes) && (runes[i] == '.' || runes[i] == '。') {
		next := i + 1
		if next >= len(runes) || runes[next] < '0' || runes[next] > '9' {
			s = strings.TrimSpace(string(runes[next:]))
		}
	}
	runes = []rune(s)
	for len(runes) > 0 && runes[len(runes)-1] == '。' {
		runes = runes[:len(runes)-1]
	}
	dotCount := 0
	for j := len(runes) - 1; j >= 0 && runes[j] == '.'; j-- {
		dotCount++
	}
	if dotCount > 0 && dotCount < 3 {
		runes = runes[:len(runes)-dotCount]
	}
	return strings.TrimSpace(string(runes))
}

func writeASRSRT(path string, segments []asrSubtitleSeg) (int, error) {
	srtFile, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer srtFile.Close()
	idx := 1
	for _, s := range segments {
		text := asrCleanSubtitleText(s.Text)
		if text == "" || s.End <= s.Start {
			continue
		}
		fmt.Fprintf(srtFile, "%d\n%s --> %s\n%s\n\n",
			idx,
			msToSubTime(s.Start, "srt"),
			msToSubTime(s.End, "srt"),
			text)
		idx++
	}
	return idx - 1, nil
}
