package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	tradOnlyChars = "們這個沒說會來時對麼還過樣點發現實國東後覺開關問間長見學車話語讓認應該進遠邊運連選錢錯頭題願風飛馬驗體魚鳥麗黃龍歡愛親歲兒無為數種經給結統繼續線終組細紀約級紅純納織聽讀寫買賣產業動區醫務稱職權術檢證據斷處備確議論爭"
	simpOnlyChars = "们这个没说会来时对么还过样点发现实国东后觉开关问间长见学车话语让认应该进远边运连选钱错头题愿风飞马验体鱼鸟丽黄龙欢爱亲岁儿无为数种经给结统继续线终组细纪约级红纯纳织听读写买卖产业动区医务称职权术检证据断处备确议论争"
)

var (
	tradCharSet = buildRuneSet(tradOnlyChars)
	simpCharSet = buildRuneSet(simpOnlyChars)
)

func buildRuneSet(s string) map[rune]bool {
	m := make(map[rune]bool, len([]rune(s)))
	for _, r := range s {
		m[r] = true
	}
	return m
}

func chineseVariant(text string) string {
	trad, simp := 0, 0
	for _, r := range text {
		if tradCharSet[r] {
			trad++
		} else if simpCharSet[r] {
			simp++
		}
	}
	switch {
	case trad == 0 && simp == 0:
		return ""
	case trad > simp:
		return "繁"
	default:
		return "简"
	}
}

type subLangKind struct {
	Variant    string
	HasCJK     bool
	HasForeign bool
	Bilingual  bool
}

func subtitleBodyText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n")
	var entries []SubtitleEntry
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ass", ".ssa":
		entries = parseASSEntries(content)
	case ".vtt":
		entries = parseVTTEntries(content)
	case ".sub":
		entries = parseSubEntries(content)
	default:
		entries = parseSRTEntries(content)
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(stripAllTags(e.Text))
		sb.WriteByte('\n')
	}
	return sb.String()
}

const bilingualLineRatio = 0.25

func detectSubtitleLang(path string) subLangKind {
	var k subLangKind
	body := subtitleBodyText(path)
	if strings.TrimSpace(body) == "" {
		return k
	}

	total, cjkLines, foreignLines := 0, 0, 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		switch {
		case containsCJK(line):
			cjkLines++
		case looksForeignSubtitle(line):
			foreignLines++
		}
	}
	if total == 0 {
		return k
	}

	k.HasCJK = cjkLines > 0
	k.HasForeign = foreignLines > 0
	k.Bilingual = float64(cjkLines)/float64(total) >= bilingualLineRatio &&
		float64(foreignLines)/float64(total) >= bilingualLineRatio
	if k.HasCJK {
		k.Variant = chineseVariant(body)
	}
	return k
}

func subtitleFilenameTag(paths []string) string {
	merged := false
	var hasSimp, hasTrad, hasUnknownCN, hasEng bool

	for _, p := range paths {
		if isBitmapSubPath(p) {
			continue
		}
		k := detectSubtitleLang(p)
		if k.Bilingual {
			merged = true
		}
		if k.HasCJK {
			switch k.Variant {
			case "简":
				hasSimp = true
			case "繁":
				hasTrad = true
			default:
				hasUnknownCN = true
			}
		}
		if k.HasForeign {
			hasEng = true
		}
	}

	if merged {
		return "中英字幕"
	}

	cn := ""
	switch {
	case hasSimp && hasTrad:
		cn = "简繁"
	case hasSimp:
		cn = "简"
	case hasTrad:
		cn = "繁"
	case hasUnknownCN:
		cn = "中"
	}

	switch {
	case cn == "" && !hasEng:
		return ""
	case cn == "":
		return "英字"
	case !hasEng:
		if cn == "中" {
			return "中字"
		}
		return cn + "中"
	case cn == "简繁":
		return "简繁英"
	default:
		return cn + "英双字幕"
	}
}

func subtitleTagsToStrip() []string {
	tags := []string{
		"中英字幕", "中英双语", "简繁英",
		"简英双字幕", "繁英双字幕", "中英双字幕",
		"简中", "繁中", "简繁中", "中字", "英字",
	}
	sort.SliceStable(tags, func(i, j int) bool {
		return len([]rune(tags[i])) > len([]rune(tags[j]))
	})
	return tags
}
