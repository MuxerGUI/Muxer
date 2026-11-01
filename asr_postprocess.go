package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type wxWord struct {
	Word  string   `json:"word"`
	Start *float64 `json:"start"`
	End   *float64 `json:"end"`
}

func isCJKWord(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

func joinWords(parts []string) string {
	var b strings.Builder
	for i, p := range parts {
		if i > 0 && !isCJKWord(p) && !isCJKWord(parts[i-1]) {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return strings.TrimSpace(b.String())
}

var asrAbbrevs = map[string]bool{
	"mr.": true, "mrs.": true, "ms.": true, "dr.": true, "prof.": true,
	"st.": true, "mt.": true, "vs.": true, "no.": true, "jr.": true, "sr.": true,
}

func isAbbrevWord(s string) bool {
	return asrAbbrevs[strings.ToLower(strings.TrimSpace(s))]
}

func isClauseBreakWord(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	r := []rune(s)
	switch r[len(r)-1] {
	case ',', ';', ':', '、', '，', '；', '：':
		return true
	}
	return false
}

func resegmentWhisperxJSON(data []byte) []SubtitleEntry {
	var doc struct {
		WordSegments []wxWord `json:"word_segments"`
		Segments     []struct {
			Words []wxWord `json:"words"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	src := doc.WordSegments
	if len(src) < 2 {
		for _, s := range doc.Segments {
			src = append(src, s.Words...)
		}
	}
	if len(src) < 2 {
		return nil
	}

	type tw struct {
		text       string
		start, end float64
	}
	var ws []tw
	var lastEnd float64
	for _, x := range src {
		t := strings.TrimSpace(x.Word)
		if t == "" {
			continue
		}
		s := lastEnd
		if x.Start != nil {
			s = *x.Start
		}
		e := s
		if x.End != nil {
			e = *x.End
		}
		if e < s {
			e = s
		}
		lastEnd = e
		ws = append(ws, tw{t, s, e})
	}
	if len(ws) < 2 {
		return nil
	}

	const maxChars = 84

	chunkChars := func(idxs []int) int {
		n := 0
		for k, i := range idxs {
			if k > 0 && !isCJKWord(ws[i].text) {
				n++
			}
			n += runeLenNoSpace(ws[i].text)
		}
		return n
	}

	var sentences [][]int
	var cur []int
	for i := range ws {
		cur = append(cur, i)
		if asrEndsSentence(ws[i].text) && !isAbbrevWord(ws[i].text) {
			sentences = append(sentences, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		sentences = append(sentences, cur)
	}

	var entries []SubtitleEntry
	emit := func(idxs []int) {
		if len(idxs) == 0 {
			return
		}
		parts := make([]string, len(idxs))
		for k, i := range idxs {
			parts[k] = ws[i].text
		}
		entries = append(entries, SubtitleEntry{
			Start:  msToSubTime(int(ws[idxs[0]].start*1000), "srt"),
			End:    msToSubTime(int(ws[idxs[len(idxs)-1]].end*1000), "srt"),
			Text:   joinWords(parts),
			Format: "srt",
		})
	}

	for _, sent := range sentences {
		if chunkChars(sent) <= maxChars {
			emit(sent)
			continue
		}
		var chunk []int
		lastClause := -1
		for _, i := range sent {
			if len(chunk) > 0 && chunkChars(append(append([]int{}, chunk...), i)) > maxChars {
				if lastClause >= 0 && lastClause < len(chunk)-1 {
					emit(chunk[:lastClause+1])
					chunk = append([]int{}, chunk[lastClause+1:]...)
				} else {
					emit(chunk)
					chunk = nil
				}
				lastClause = -1
			}
			chunk = append(chunk, i)
			if isClauseBreakWord(ws[i].text) {
				lastClause = len(chunk) - 1
			}
		}
		emit(chunk)
	}
	if len(entries) == 0 {
		return nil
	}
	return entries
}

func buildSRTFromEntries(entries []SubtitleEntry) string {
	var b strings.Builder
	for i, e := range entries {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
	}
	return b.String()
}

func (a *App) asrPostCorrect(srtPath string) {
	raw, err := os.ReadFile(srtPath)
	if err != nil {
		a.log("warn", "读取字幕文件失败，跳过 AI 修正: "+err.Error())
		return
	}

	entries := parseSRTEntries(string(raw))
	if len(entries) == 0 {
		a.log("warn", "字幕为空，跳过 AI 修正")
		return
	}

	before := len(entries)
	entries = collapseASRRepetitions(entries)
	if removed := before - len(entries); removed > 0 {
		a.log("info", fmt.Sprintf("折叠重复 %d 条", removed))
	}
	before = len(entries)
	entries = dropEdgeHallucinations(entries)
	if removed := before - len(entries); removed > 0 {
		a.log("info", fmt.Sprintf("清理片头片尾幻觉 %d 条", removed))
	}
	before = len(entries)
	entries = dropASRHallucinations(entries)
	if removed := before - len(entries); removed > 0 {
		a.log("info", fmt.Sprintf("剔除超语速幻觉 %d 条", removed))
	}
	before = len(entries)
	entries = mergeShortASREntries(entries)
	if merged := before - len(entries); merged > 0 {
		a.log("info", fmt.Sprintf("合并碎句 %d 条", merged))
	}
	if len(entries) == 0 {
		a.log("warn", "规整后字幕为空，跳过 AI 修正")
		return
	}
	if adjusted := enforceASRTiming(entries); adjusted > 0 {
		a.log("info", fmt.Sprintf("规整时长 %d 条", adjusted))
	}

	texts := make([]string, len(entries))
	for i, e := range entries {
		t := strings.TrimSpace(e.Text)
		t = strings.ReplaceAll(t, "\n", " ")
		if t == "" {
			t = "…"
		}
		texts[i] = t
	}

	ext := filepath.Ext(srtPath)
	stem := strings.TrimSuffix(srtPath, ext)
	outFile := stem + ".AI修正.srt"

	apiKey := chatAPIKey()
	if apiKey == "" {
		a.log("warn", "未配置 AI Key，仅输出规整字幕")
		if werr := os.WriteFile(outFile, []byte(buildSRTFromEntries(entries)), 0644); werr != nil {
			a.log("warn", "写入失败: "+werr.Error())
			return
		}
		a.log("ok", "字幕 → "+filepath.Base(outFile))
		a.asrSelfCheckReport(outFile)
		return
	}

	a.log("info", fmt.Sprintf("%s 语义修正 %d 条", aiProviderLabel(), len(entries)))

	const maxSplitDepth = 4
	const contextLines = 5
	var prevCtxSrc, prevCtxDst []string
	var correctWithSplit func(batch []string, depth int) ([]string, error)
	correctWithSplit = func(batch []string, depth int) ([]string, error) {
		const maxRetry = 3
		var results []string
		var err error
		for attempt := 1; attempt <= maxRetry; attempt++ {
			if a.stopped.Load() {
				return nil, errCancelled
			}
			results, err = asrCorrectChinese(apiKey, batch, prevCtxSrc, prevCtxDst)
			if err == nil {
				return results, nil
			}
			msg := err.Error()
			if strings.Contains(msg, "余额不足") || strings.Contains(msg, "认证失败") {
				return nil, err
			}
			if attempt < maxRetry {
				if !a.sleepOrStop(time.Duration(attempt) * 2 * time.Second) {
					return nil, errCancelled
				}
			}
		}
		if depth >= maxSplitDepth || len(batch) <= 5 {
			if depth == 0 {
				a.log("warn", fmt.Sprintf("%s 修正失败 %d 条，逐条重试", aiProviderLabel(), len(batch)))
			}
			fallback := make([]string, len(batch))
			kept := 0
			for i, t := range batch {
				if a.stopped.Load() {
					return nil, errCancelled
				}
				sr, se := asrCorrectChinese(apiKey, []string{t}, prevCtxSrc, prevCtxDst)
				switch {
				case se != nil:
					fallback[i] = t
					kept++
				case len(sr) == 0:
					fallback[i] = t
					kept++
				default:
					fallback[i] = sr[0]
				}
			}
			if kept > 0 {
				a.log("warn", fmt.Sprintf("保留原文 %d 条", kept))
			}
			return fallback, nil
		}
		mid := len(batch) / 2
		if depth == 0 {
			a.log("warn", fmt.Sprintf("%s 修正失败 %d 条，拆分重试", aiProviderLabel(), len(batch)))
		}
		left, errL := correctWithSplit(batch[:mid], depth+1)
		if errL != nil {
			return nil, errL
		}
		right, errR := correctWithSplit(batch[mid:], depth+1)
		if errR != nil {
			return nil, errR
		}
		return append(left, right...), nil
	}

	srtFile, ferr := os.Create(outFile)
	if ferr != nil {
		a.log("warn", "创建修正文件失败: "+ferr.Error())
		return
	}

	const batchSize = 50
	globalIdx := 1
	writeOK := true
	for start := 0; start < len(texts); start += batchSize {
		if a.stopped.Load() {
			a.log("warn", aiProviderLabel()+" 修正已取消")
			writeOK = false
			break
		}

		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[start:end]

		results, batchErr := correctWithSplit(batch, 0)
		if batchErr != nil {
			if isCancelled(batchErr) {
				a.log("warn", aiProviderLabel()+" 修正已取消")
			} else {
				a.log("warn", "修正失败，保留原始 ASR: "+batchErr.Error())
			}
			writeOK = false
			break
		}

		ctxStart := 0
		if len(batch) > contextLines {
			ctxStart = len(batch) - contextLines
		}
		prevCtxSrc = append([]string{}, batch[ctxStart:]...)
		prevCtxDst = append([]string{}, results[ctxStart:]...)

		cleanResults := make([]string, len(results))
		for i, res := range results {
			c := asrCleanSubtitleText(res)
			if c == "" {
				c = strings.TrimSpace(res)
			}
			cleanResults[i] = c
		}
		biLines := asrBuildBilingualLines(apiKey, batch, cleanResults)
		for i := range results {
			if strings.TrimSpace(biLines[i]) == "" {
				continue
			}
			e := entries[start+i]
			fmt.Fprintf(srtFile, "%d\n%s --> %s\n%s\n\n", globalIdx, e.Start, e.End, biLines[i])
			globalIdx++
		}
		a.log("info", fmt.Sprintf("修正进度 %d / %d", end, len(entries)))
	}

	srtFile.Close()
	if !writeOK {
		os.Remove(outFile)
		return
	}

	a.log("ok", fmt.Sprintf("%s 修正 → %s", chatProviderUsed(), filepath.Base(outFile)))
	a.asrSelfCheckReport(outFile)
}

func (a *App) asrSelfCheckReport(subPath string) {
	raw, err := os.ReadFile(subPath)
	if err != nil {
		return
	}
	entries := parseSRTEntries(string(raw))
	if len(entries) == 0 {
		return
	}

	type issue struct {
		idx    int
		start  string
		end    string
		text   string
		labels []string
	}
	const (
		minDurMs  = 833
		maxDurMs  = 7000
		zhMaxLine = 20
		zhMaxCPS  = 12
		enMaxLine = 42
		enMaxCPS  = 17
	)
	var issues []issue
	prevEnd := -1
	var prevText string

	for i, e := range entries {
		start := subTimeToMs(e.Start, "srt")
		end := subTimeToMs(e.End, "srt")
		dur := end - start
		text := strings.TrimSpace(e.Text)
		var labels []string

		if end <= start {
			labels = append(labels, "时间码异常(结束≤开始)")
		} else {
			if dur < minDurMs {
				labels = append(labels, fmt.Sprintf("过短(%.1fs)", float64(dur)/1000))
			}
			if dur > maxDurMs {
				labels = append(labels, fmt.Sprintf("过长(%.1fs)", float64(dur)/1000))
			}
		}
		if prevEnd >= 0 && start < prevEnd {
			labels = append(labels, fmt.Sprintf("与上一条重叠%dms", prevEnd-start))
		}
		if text == "" {
			labels = append(labels, "空文本")
		} else {
			lines := strings.Split(text, "\n")
			if len(lines) > 2 {
				labels = append(labels, fmt.Sprintf("行数过多(%d行)", len(lines)))
			}
			durSec := float64(dur) / 1000
			for _, ln := range lines {
				ln = strings.TrimSpace(ln)
				if ln == "" {
					continue
				}
				n := runeLenNoSpace(ln)
				if containsCJK(ln) {
					if n > zhMaxLine {
						labels = append(labels, fmt.Sprintf("中文行过长(%d字)", n))
					}
					if durSec > 0 && float64(n)/durSec > zhMaxCPS {
						labels = append(labels, fmt.Sprintf("中文阅读过快(%.0f字/秒)", float64(n)/durSec))
					}
				} else {
					if n > enMaxLine {
						labels = append(labels, fmt.Sprintf("英文行过长(%d字符)", n))
					}
					if durSec > 0 && float64(n)/durSec > enMaxCPS {
						labels = append(labels, fmt.Sprintf("英文阅读过快(%.0f字符/秒)", float64(n)/durSec))
					}
				}
			}
			if text == prevText {
				labels = append(labels, "与上一条完全相同(疑似重复)")
			}
		}

		if len(labels) > 0 {
			issues = append(issues, issue{idx: i + 1, start: e.Start, end: e.End, text: text, labels: labels})
		}
		prevEnd = end
		prevText = text
	}

	var b strings.Builder
	b.WriteString("\ufeff")
	b.WriteString("Muxer 字幕自检报告\n")
	b.WriteString("文件: " + filepath.Base(subPath) + "\n")
	b.WriteString("生成: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
	b.WriteString(fmt.Sprintf("条目总数: %d    可疑条目: %d\n", len(entries), len(issues)))
	b.WriteString(fmt.Sprintf("判定线: 中文≤%d字/行 且≤%d字/秒 · 英文≤%d字符/行 且≤%d字符/秒 · 时长%.2f–%.1f秒\n",
		zhMaxLine, zhMaxCPS, enMaxLine, enMaxCPS, float64(minDurMs)/1000, float64(maxDurMs)/1000))
	b.WriteString("说明: 以下为机器自检出的可疑项，建议用播放器跳到对应时间码核对人名/术语/轴是否准确。\n")
	b.WriteString(strings.Repeat("-", 60) + "\n\n")

	if len(issues) == 0 {
		b.WriteString("未发现明显问题，可直接使用。\n")
	} else {
		for _, it := range issues {
			b.WriteString(fmt.Sprintf("#%d  [%s]\n", it.idx, strings.Join(it.labels, " / ")))
			b.WriteString(fmt.Sprintf("    %s --> %s\n", it.start, it.end))
			for _, ln := range strings.Split(it.text, "\n") {
				b.WriteString("    " + ln + "\n")
			}
			b.WriteString("\n")
		}
	}

	reportPath := strings.TrimSuffix(subPath, filepath.Ext(subPath)) + ".自检报告.txt"
	if err := os.WriteFile(reportPath, []byte(b.String()), 0644); err != nil {
		a.log("warn", "写自检报告失败: "+err.Error())
		return
	}
	if len(issues) == 0 {
		a.log("ok", "自检通过")
	} else {
		a.log("info", fmt.Sprintf("自检 %d 条可疑 · %s", len(issues), filepath.Base(reportPath)))
	}
}
