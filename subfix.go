package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type lineLang uint8

const (
	lineLangEmpty lineLang = iota
	lineLangChinese
	lineLangLatin
	lineLangJapanese
	lineLangKorean
	lineLangMixed
	lineLangOther
)

type fixUnitKind uint8

const (
	fixKindEmpty fixUnitKind = iota
	fixKindChinese
	fixKindLatin
	fixKindOtherForeign
	fixKindBilingual
	fixKindPairedZH
	fixKindPairedForeign
)

type styledFrag struct {
	prefix string
	text   string
	suffix string
	raw    string
	lang   lineLang
}

type subtitleFixUnit struct {
	index    int
	kind     fixUnitKind
	frags    []styledFrag
	joiner   string
	origText string

	drop        bool
	foreignEN   bool
	foreignZH   bool
	foreignDrop bool
	addEN       bool
	addZH       bool
	foreignSrc  string
}

type fixJob struct {
	ui, fi int
	text   string
}

var (
	reLeadingASSTags   = regexp.MustCompile(`^(\{[^}]*\})+`)
	reLeadingHTMLTags  = regexp.MustCompile(`(?i)^(</?[^>]+>)+`)
	reTrailingCloseTag = regexp.MustCompile(`(?i)(</[^>]+>|\{\\[^}]*\})+$`)
	reLatinToken       = regexp.MustCompile(`[a-z]{2,}`)
)

func isHiraganaKatakana(r rune) bool {
	return (r >= 0x3040 && r <= 0x30FF) || (r >= 0x31F0 && r <= 0x31FF)
}

func isHangulRune(r rune) bool {
	return (r >= 0xAC00 && r <= 0xD7AF) || (r >= 0x1100 && r <= 0x11FF)
}

func isLatinLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func classifyLineLang(s string) lineLang {
	s = strings.TrimSpace(s)
	if s == "" {
		return lineLangEmpty
	}
	var han, latin, hira, hang int
	for _, r := range s {
		switch {
		case isHiraganaKatakana(r):
			hira++
		case isHangulRune(r):
			hang++
		case isCJKRune(r):
			han++
		case isLatinLetter(r):
			latin++
		}
	}
	switch {
	case hira > 0:
		return lineLangJapanese
	case hang > 0:
		return lineLangKorean
	case han > 0 && latin > 0:
		return lineLangMixed
	case han > 0:
		return lineLangChinese
	case latin > 0:
		return lineLangLatin
	case hasExtendedLetter(s):
		return lineLangOther
	default:
		return lineLangOther
	}
}

func hasExtendedLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && !isLatinLetter(r) && !isCJKRune(r) && !isHiraganaKatakana(r) && !isHangulRune(r) {
			return true
		}
	}
	return false
}

func looksEnglish(s string) bool {
	hasLatin := false
	for _, r := range s {
		if isLatinLetter(r) {
			hasLatin = true
		}
		if r >= 0x00C0 && r <= 0x024F {
			return false
		}
	}
	return hasLatin
}

func langFamily(l lineLang) lineLang {
	switch l {
	case lineLangChinese, lineLangMixed:
		return lineLangChinese
	case lineLangLatin, lineLangJapanese, lineLangKorean, lineLangOther:
		if l == lineLangOther {
			return 0
		}
		return l
	default:
		return 0
	}
}

func fragsHaveMultipleLangs(frags []styledFrag) bool {
	var seen lineLang
	for _, f := range frags {
		fam := langFamily(f.lang)
		if fam == 0 {
			continue
		}
		if seen == 0 {
			seen = fam
			continue
		}
		if fam != seen {
			return true
		}
	}
	return false
}

func isForeignLineLang(l lineLang) bool {
	return l == lineLangLatin || l == lineLangJapanese || l == lineLangKorean
}

func expandSubtitleBreaks(s string) string {
	s = strings.ReplaceAll(s, `\N`, "\n")
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

func peelStyledTags(line string) (prefix, text, suffix, raw string) {
	rest := line
	for {
		m := reLeadingASSTags.FindString(rest)
		if m == "" {
			m = reLeadingHTMLTags.FindString(rest)
		}
		if m == "" {
			break
		}
		prefix += m
		rest = rest[len(m):]
	}
	rest, suffix = peelTrailingTags(rest)
	return prefix, strings.TrimSpace(stripAllTags(rest)), suffix, strings.TrimSpace(rest)
}

func peelTrailingTags(text string) (body, suffix string) {
	for {
		trimmed := strings.TrimRight(text, " \t")
		loc := reTrailingCloseTag.FindStringIndex(trimmed)
		if loc == nil {
			break
		}
		suffix = trimmed[loc[0]:] + suffix
		text = strings.TrimRight(trimmed[:loc[0]], " \t")
	}
	return text, suffix
}

func splitMixedScript(s string) (zh, foreign string) {
	var zhB, foB strings.Builder
	last := 0
	for _, r := range s {
		switch {
		case isCJKRune(r):
			zhB.WriteRune(r)
			last = 1
		case isLatinLetter(r) || isHiraganaKatakana(r) || isHangulRune(r):
			foB.WriteRune(r)
			last = 2
		case unicode.IsSpace(r):
			if last == 2 && foB.Len() > 0 {
				foB.WriteRune(r)
			}
		default:
			if last == 1 {
				zhB.WriteRune(r)
			} else if last == 2 {
				foB.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(zhB.String()), strings.TrimSpace(foB.String())
}

func splitByASSTags(line string) []styledFrag {
	idxs := reASSTag.FindAllStringIndex(line, -1)
	if len(idxs) == 0 {
		return nil
	}
	var frags []styledFrag
	cursor := 0
	pending := ""
	appendText := func(raw, tags string) {
		text := strings.TrimSpace(stripAllTags(raw))
		if text == "" {
			return
		}
		frags = append(frags, styledFrag{prefix: tags, text: text, lang: classifyLineLang(text)})
	}
	for _, loc := range idxs {
		before := line[cursor:loc[0]]
		tag := line[loc[0]:loc[1]]
		if strings.TrimSpace(stripAllTags(before)) != "" {
			appendText(before, pending)
			pending = tag
		} else {
			pending += tag
		}
		cursor = loc[1]
	}
	appendText(line[cursor:], pending)
	return explodeMixedFrags(frags)
}

func explodeMixedFrags(frags []styledFrag) []styledFrag {
	if len(frags) == 0 {
		return frags
	}
	out := make([]styledFrag, 0, len(frags)+1)
	for _, f := range frags {
		if f.lang != lineLangMixed {
			out = append(out, f)
			continue
		}
		if !mixedSplittable(f.text) {
			out = append(out, f)
			continue
		}
		zh, foreign := splitMixedScript(f.text)
		out = append(out, styledFrag{prefix: f.prefix, text: zh, lang: lineLangChinese})
		out = append(out, styledFrag{text: foreign, suffix: f.suffix, lang: classifyLineLang(foreign)})
	}
	return out
}

func mixedSplittable(s string) bool {
	zh, foreign := splitMixedScript(s)
	if zh == "" || foreign == "" {
		return false
	}
	if len(strings.Fields(foreign)) < 2 {
		return false
	}
	runes := []rune(s)
	first, last := -1, -1
	for i, r := range runes {
		if isLatinLetter(r) || isHiraganaKatakana(r) || isHangulRune(r) {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return false
	}
	if first > 0 && isCJKRune(runes[first-1]) {
		return false
	}
	if last+1 < len(runes) && isCJKRune(runes[last+1]) {
		return false
	}
	return true
}

func lostLatinToken(orig, fixed string) bool {
	lo := strings.ToLower(stripAllTags(orig))
	if lo == "" {
		return false
	}
	lf := strings.ToLower(stripAllTags(fixed))
	for _, tok := range reLatinToken.FindAllString(lo, -1) {
		if !strings.Contains(lf, tok) {
			return true
		}
	}
	return false
}

func splitLineToFrags(line string) []styledFrag {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	plain := strings.TrimSpace(stripAllTags(line))
	lang := classifyLineLang(plain)
	if strings.Contains(line, "{") && reASSTag.MatchString(line) {
		if frags := splitByASSTags(line); fragsHaveMultipleLangs(frags) {
			return frags
		}
		if lang == lineLangMixed {
			if frags := explodeMixedFrags(splitByASSTags(line)); fragsHaveMultipleLangs(frags) {
				return frags
			}
		}
	}
	if lang == lineLangMixed && mixedSplittable(plain) {
		zh, foreign := splitMixedScript(plain)
		if zh != "" && foreign != "" {
			prefix, _, suffix, _ := peelStyledTags(line)
			return []styledFrag{
				{prefix: prefix, text: zh, lang: lineLangChinese},
				{text: foreign, suffix: suffix, lang: classifyLineLang(foreign)},
			}
		}
	}
	prefix, text, suffix, raw := peelStyledTags(line)
	if text == "" {
		text = plain
		suffix = ""
		raw = ""
	}
	return []styledFrag{{prefix: prefix, text: text, suffix: suffix, raw: raw, lang: classifyLineLang(text)}}
}

func parseStyledFrags(raw string) []styledFrag {
	raw = expandSubtitleBreaks(raw)
	var out []styledFrag
	for _, line := range strings.Split(raw, "\n") {
		out = append(out, splitLineToFrags(line)...)
	}
	return out
}

func classifyFrags(frags []styledFrag) fixUnitKind {
	hasZH, hasLatin, hasOtherForeign := false, false, false
	for _, f := range frags {
		switch f.lang {
		case lineLangChinese, lineLangMixed:
			hasZH = true
			if f.lang == lineLangMixed {
				hasLatin = true
			}
		case lineLangLatin:
			hasLatin = true
		case lineLangJapanese, lineLangKorean:
			hasOtherForeign = true
		case lineLangOther:
			if hasExtendedLetter(f.text) {
				hasOtherForeign = true
			}
		}
	}
	hasForeign := hasLatin || hasOtherForeign
	switch {
	case hasZH && hasForeign:
		return fixKindBilingual
	case hasZH:
		return fixKindChinese
	case hasLatin && !hasOtherForeign:
		return fixKindLatin
	case hasForeign:
		return fixKindOtherForeign
	default:
		return fixKindEmpty
	}
}

func subtitleJoiner(format string) string {
	if format == "ass" || format == "ssa" {
		return `\N`
	}
	return "\n"
}

func planSubtitleFix(entries []SubtitleEntry) []subtitleFixUnit {
	units := make([]subtitleFixUnit, len(entries))
	for i, e := range entries {
		frags := dedupeFragLines(parseStyledFrags(e.Text))
		units[i] = subtitleFixUnit{
			index:    i,
			kind:     classifyFrags(frags),
			frags:    frags,
			joiner:   subtitleJoiner(e.Format),
			origText: e.Text,
		}
	}
	groups := markOverlappingLangPairs(entries, units)
	planFixLanguages(units, groups)
	return units
}

func entriesOverlapHalf(a, b SubtitleEntry) bool {
	as, ae := subTimeToMs(a.Start, a.Format), subTimeToMs(a.End, a.Format)
	bs, be := subTimeToMs(b.Start, b.Format), subTimeToMs(b.End, b.Format)
	if ae < as {
		as, ae = ae, as
	}
	if be < bs {
		bs, be = be, bs
	}
	lo, hi := as, ae
	if bs > lo {
		lo = bs
	}
	if be < hi {
		hi = be
	}
	ov := hi - lo
	if ov <= 0 {
		return false
	}
	durA, durB := ae-as, be-bs
	shorter := durA
	if durB < shorter {
		shorter = durB
	}
	return shorter > 0 && ov*2 >= shorter
}

func overlapGroups(entries []SubtitleEntry) [][]int {
	n := len(entries)
	if n == 0 {
		return nil
	}
	type span struct{ i, start, end int }
	spans := make([]span, n)
	for i, e := range entries {
		s, en := subTimeToMs(e.Start, e.Format), subTimeToMs(e.End, e.Format)
		if en < s {
			s, en = en, s
		}
		spans[i] = span{i, s, en}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n && spans[j].start < spans[i].end; j++ {
			if entriesOverlapHalf(entries[spans[i].i], entries[spans[j].i]) {
				union(spans[i].i, spans[j].i)
			}
		}
	}

	byRoot := map[int][]int{}
	for i := 0; i < n; i++ {
		byRoot[find(i)] = append(byRoot[find(i)], i)
	}
	groups := make([][]int, 0, len(byRoot))
	for _, idxs := range byRoot {
		sort.Ints(idxs)
		groups = append(groups, idxs)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

func markOverlappingLangPairs(entries []SubtitleEntry, units []subtitleFixUnit) [][]int {
	groups := overlapGroups(entries)
	for _, idxs := range groups {
		if len(idxs) < 2 {
			continue
		}
		hasZH, hasFO := false, false
		for _, i := range idxs {
			switch units[i].kind {
			case fixKindChinese, fixKindBilingual:
				hasZH = true
			}
			switch units[i].kind {
			case fixKindLatin, fixKindOtherForeign, fixKindBilingual:
				hasFO = true
			}
		}
		if !hasZH || !hasFO {
			continue
		}
		for _, i := range idxs {
			switch units[i].kind {
			case fixKindChinese:
				units[i].kind = fixKindPairedZH
			case fixKindLatin, fixKindOtherForeign:
				units[i].kind = fixKindPairedForeign
			}
		}
	}
	return groups
}

func jobText(s string) string {
	t := asrCleanSubtitleText(s)
	if t == "" {
		t = strings.TrimSpace(s)
	}
	return t
}

func gatherPolishJobs(units []subtitleFixUnit, start, end int) (zhJobs, enJobs, proofJobs []fixJob) {
	if start < 0 {
		start = 0
	}
	if end > len(units) {
		end = len(units)
	}
	for ui := start; ui < end; ui++ {
		u := units[ui]
		for fi, f := range u.frags {
			text := jobText(f.text)
			if text == "" {
				continue
			}
			switch u.kind {
			case fixKindBilingual, fixKindPairedZH, fixKindChinese:
				if f.lang == lineLangChinese {
					zhJobs = append(zhJobs, fixJob{ui, fi, text})
				}
			}
			switch u.kind {
			case fixKindBilingual, fixKindPairedForeign:
				if f.lang == lineLangLatin && looksEnglish(text) && !strings.ContainsAny(f.text, "<{") {
					proofJobs = append(proofJobs, fixJob{ui, fi, strings.TrimSpace(f.text)})
				}
			case fixKindLatin:
				if f.lang == lineLangLatin && looksEnglish(text) {
					enJobs = append(enJobs, fixJob{ui, fi, text})
				}
			}
		}
	}
	return zhJobs, enJobs, proofJobs
}

func applyJobResults(units []subtitleFixUnit, jobs []fixJob, results []string) {
	applyJobResultsKind(units, jobs, results, false)
}

func applyZhJobResults(units []subtitleFixUnit, jobs []fixJob, results []string) {
	applyJobResultsKind(units, jobs, results, true)
}

func applyJobResultsKind(units []subtitleFixUnit, jobs []fixJob, results []string, chinese bool) {
	if len(results) != len(jobs) {
		return
	}
	for i, job := range jobs {
		t := strings.TrimSpace(results[i])
		if t == "" {
			if subtitleTextIsJunk(units[job.ui].frags[job.fi].text) {
				units[job.ui].frags[job.fi].text = ""
			}
			continue
		}
		if chinese {
			t = sanitizeChineseFix(t)
			if t == "" {
				if subtitleTextIsJunk(units[job.ui].frags[job.fi].text) {
					units[job.ui].frags[job.fi].text = ""
				}
				continue
			}
			if !containsCJK(t) {
				continue
			}
			if lostLatinToken(units[job.ui].frags[job.fi].text, t) {
				continue
			}
		}
		if !chinese {
			if containsCJK(t) && !containsCJK(units[job.ui].frags[job.fi].text) {
				continue
			}
			if subtitleFixEquivalent(units[job.ui].frags[job.fi].text, t) {
				continue
			}
		}
		if t != units[job.ui].frags[job.fi].text {
			units[job.ui].frags[job.fi].raw = ""
		}
		units[job.ui].frags[job.fi].text = t
	}
}

func subtitleCoreText(s string) string {
	var sb strings.Builder
	for _, r := range stripAllTags(expandSubtitleBreaks(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(unicode.ToLower(r))
		}
	}
	return sb.String()
}

func subtitleFixEquivalent(orig, fixed string) bool {
	a := subtitleCoreText(orig)
	if a == "" {
		return false
	}
	return a == subtitleCoreText(fixed)
}

func lineKey(s string) string {
	if c := subtitleCoreText(s); c != "" {
		return c
	}
	return strings.TrimSpace(s)
}

func dedupeFragLines(frags []styledFrag) []styledFrag {
	out := make([]styledFrag, 0, len(frags))
	seen := make(map[lineLang]map[string]int, 2)
	for _, f := range frags {
		t := strings.TrimSpace(f.text)
		if t == "" {
			out = append(out, f)
			continue
		}
		fam := langFamily(f.lang)
		key := lineKey(t)
		if key == "" {
			out = append(out, f)
			continue
		}
		if seen[fam] == nil {
			seen[fam] = make(map[string]int, len(frags))
		}
		idx, ok := seen[fam][key]
		if !ok {
			seen[fam][key] = len(out)
			out = append(out, f)
			continue
		}
		if len(t) > len(strings.TrimSpace(out[idx].text)) {
			out[idx].text = f.text
			out[idx].raw = ""
		}
	}
	return dropConcatRedundantFrags(out)
}

func concatRedundantDrop(keys []string, mixed []bool) []bool {
	drop := make([]bool, len(keys))
	if len(keys) < 2 {
		return drop
	}
	for i := range keys {
		if !mixed[i] || keys[i] == "" {
			continue
		}
		var fwd, rev strings.Builder
		for j := range keys {
			if j != i {
				fwd.WriteString(keys[j])
			}
		}
		for j := len(keys) - 1; j >= 0; j-- {
			if j != i {
				rev.WriteString(keys[j])
			}
		}
		if keys[i] == fwd.String() || keys[i] == rev.String() {
			drop[i] = true
		}
	}
	return drop
}

func dropConcatRedundantLines(lines []string) []string {
	if len(lines) < 2 {
		return lines
	}
	keys := make([]string, len(lines))
	mixed := make([]bool, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		keys[i] = subtitleCoreText(t)
		mixed[i] = classifyLineLang(stripAllTags(t)) == lineLangMixed
	}
	drop := concatRedundantDrop(keys, mixed)
	hit := false
	for _, d := range drop {
		if d {
			hit = true
			break
		}
	}
	if !hit {
		return lines
	}
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return out
}

func dropConcatRedundantFrags(frags []styledFrag) []styledFrag {
	if len(frags) < 2 {
		return frags
	}
	keys := make([]string, len(frags))
	mixed := make([]bool, len(frags))
	for i, f := range frags {
		t := strings.TrimSpace(f.text)
		keys[i] = subtitleCoreText(t)
		mixed[i] = classifyLineLang(stripAllTags(t)) == lineLangMixed
	}
	drop := concatRedundantDrop(keys, mixed)
	hit := false
	for _, d := range drop {
		if d {
			hit = true
			break
		}
	}
	if !hit {
		return frags
	}
	out := make([]styledFrag, 0, len(frags))
	for i, f := range frags {
		if !drop[i] {
			out = append(out, f)
		}
	}
	return out
}

func dedupeEntryLines(text string) string {
	lines := splitRawSubtitleLines(text)
	if len(lines) <= 1 {
		return strings.TrimSpace(text)
	}
	sep := subtitleLineSep(text)
	kept := make([]string, 0, len(lines))
	seen := make(map[string]int, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		key := lineKey(t)
		if key != "" {
			if idx, ok := seen[key]; ok {
				if len(t) > len(kept[idx]) {
					kept[idx] = t
				}
				continue
			}
			seen[key] = len(kept)
		}
		kept = append(kept, t)
	}
	return strings.Join(dropConcatRedundantLines(kept), sep)
}

func sanitizeChineseFix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var zh []string
	for _, line := range strings.Split(expandSubtitleBreaks(s), "\n") {
		line = strings.TrimSpace(stripAllTags(line))
		if line == "" {
			continue
		}
		switch classifyLineLang(line) {
		case lineLangChinese:
			zh = append(zh, line)
		case lineLangMixed:
			if mixedSplittable(line) {
				if z, _ := splitMixedScript(line); z != "" {
					zh = append(zh, z)
				}
			} else {
				zh = append(zh, line)
			}
		}
	}
	if len(zh) == 0 {
		if containsCJK(s) {
			z, _ := splitMixedScript(stripAllTags(expandSubtitleBreaks(s)))
			return z
		}
		return ""
	}
	return strings.Join(zh, "")
}

func unitJoined(u subtitleFixUnit, pred func(lineLang) bool) string {
	var parts []string
	for _, f := range u.frags {
		if pred(f.lang) {
			if t := strings.TrimSpace(f.text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, " ")
}

func planFixLanguages(units []subtitleFixUnit, groups [][]int) {
	for _, g := range groups {
		hasZH, hasEN := false, false
		for _, ui := range g {
			hasZH = hasZH || unitHasZH(units[ui])
			hasEN = hasEN || unitHasEN(units[ui])
		}
		for _, ui := range g {
			u := &units[ui]
			if unitHasZH(*u) || unitHasEN(*u) {
				continue
			}
			fo := unitForeignText(*u)
			switch {
			case fo == "":
			case !hasZH:
				u.foreignSrc, u.foreignZH = fo, true
				hasZH, hasEN = true, true
			case !hasEN:
				u.foreignSrc, u.foreignEN = fo, true
				hasEN = true
			default:
				u.drop = true
			}
		}
		for _, ui := range g {
			u := &units[ui]
			if u.drop || u.foreignEN || u.foreignZH || u.addZH {
				continue
			}
			if fo := unitForeignText(*u); fo != "" {
				if hasEN {
					u.foreignDrop = true
				} else {
					u.foreignSrc, u.foreignEN = fo, true
					hasEN = true
				}
				continue
			}
			if unitHasZH(*u) && !unitHasEN(*u) && u.kind == fixKindChinese && !subtitleNotDialogue(unitJoined(*u, isChineseLineLang)) {
				u.addEN = true
				hasEN = true
			}
			if unitHasEN(*u) && !unitHasZH(*u) && u.kind == fixKindLatin && !subtitleNotDialogue(unitJoined(*u, isForeignLineLang)) {
				u.addZH = true
				hasZH = true
			}
		}
	}
}

func unitHasZH(u subtitleFixUnit) bool {
	for _, f := range u.frags {
		if f.lang == lineLangChinese || f.lang == lineLangMixed {
			return true
		}
	}
	return false
}

func unitHasEN(u subtitleFixUnit) bool {
	for _, f := range u.frags {
		if f.lang == lineLangLatin && looksEnglish(f.text) {
			return true
		}
	}
	return false
}

func isThirdLangFrag(f styledFrag) bool {
	switch f.lang {
	case lineLangJapanese, lineLangKorean:
		return true
	case lineLangOther:
		return hasExtendedLetter(f.text)
	case lineLangLatin:
		return !looksEnglish(f.text)
	}
	return false
}

func unitForeignText(u subtitleFixUnit) string {
	var parts []string
	for _, f := range u.frags {
		if isThirdLangFrag(f) {
			if t := strings.TrimSpace(f.text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, " ")
}

func fragHasText(u subtitleFixUnit, text string) bool {
	key := lineKey(text)
	if key == "" {
		return true
	}
	for _, f := range u.frags {
		if lineKey(f.text) == key {
			return true
		}
	}
	return false
}

func addLanguageToUnits(units []subtitleFixUnit, idxs []int, texts []string, lang lineLang) {
	if len(idxs) != len(texts) {
		return
	}
	for i, ui := range idxs {
		t := strings.TrimSpace(texts[i])
		if fragHasText(units[ui], t) {
			continue
		}
		frag := styledFrag{text: t, lang: lang}
		if lang == lineLangChinese {
			units[ui].frags = append([]styledFrag{frag}, units[ui].frags...)
		} else {
			units[ui].frags = append(units[ui].frags, frag)
		}
	}
}

func currentUnitSrc(u subtitleFixUnit, chinese bool) string {
	var parts []string
	for _, f := range u.frags {
		if chinese {
			if !isChineseLineLang(f.lang) {
				continue
			}
		} else if !isForeignLineLang(f.lang) {
			continue
		}
		if t := strings.TrimSpace(f.text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

func gatherAddJobs(units []subtitleFixUnit, start, end int) (enJobs, zhJobs []fixJob) {
	if start < 0 {
		start = 0
	}
	if end > len(units) {
		end = len(units)
	}
	for ui := start; ui < end; ui++ {
		if units[ui].addEN {
			if src := currentUnitSrc(units[ui], true); src != "" {
				enJobs = append(enJobs, fixJob{ui: ui, text: src})
			}
		}
		if units[ui].addZH {
			if src := currentUnitSrc(units[ui], false); src != "" {
				zhJobs = append(zhJobs, fixJob{ui: ui, text: src})
			}
		}
	}
	return enJobs, zhJobs
}

func gatherForeignJobs(units []subtitleFixUnit, start, end int) (zhJobs, enJobs []fixJob) {
	if start < 0 {
		start = 0
	}
	if end > len(units) {
		end = len(units)
	}
	for ui := start; ui < end; ui++ {
		if units[ui].foreignZH {
			zhJobs = append(zhJobs, fixJob{ui: ui, text: units[ui].foreignSrc})
		}
		if units[ui].foreignEN {
			enJobs = append(enJobs, fixJob{ui: ui, text: units[ui].foreignSrc})
		}
	}
	return zhJobs, enJobs
}

func applyForeignChinese(units []subtitleFixUnit, jobs []fixJob, results []string) {
	if len(results) != len(jobs) {
		return
	}
	for i, j := range jobs {
		t := strings.TrimSpace(results[i])
		if t == "" {
			continue
		}
		u := &units[j.ui]
		kept := make([]styledFrag, 0, len(u.frags))
		wrote := false
		for _, f := range u.frags {
			if !isThirdLangFrag(f) {
				kept = append(kept, f)
				continue
			}
			if wrote {
				continue
			}
			f.text = t
			f.raw = ""
			f.lang = lineLangChinese
			kept = append(kept, f)
			wrote = true
		}
		if !wrote {
			continue
		}
		u.frags = kept
		u.addEN = true
	}
}

func applyAddJobs(units []subtitleFixUnit, jobs []fixJob, results []string, lang lineLang) {
	if len(results) != len(jobs) {
		return
	}
	idxs := make([]int, len(jobs))
	texts := make([]string, len(jobs))
	for i, j := range jobs {
		idxs[i] = j.ui
		texts[i] = results[i]
	}
	addLanguageToUnits(units, idxs, texts, lang)
}

func applyForeignJobs(units []subtitleFixUnit, jobs []fixJob, results []string) {
	if len(results) != len(jobs) {
		return
	}
	for i, j := range jobs {
		t := strings.TrimSpace(results[i])
		if t == "" {
			continue
		}
		u := &units[j.ui]
		kept := u.frags[:0:0]
		wrote := false
		for _, f := range u.frags {
			if !isThirdLangFrag(f) {
				kept = append(kept, f)
				continue
			}
			if wrote {
				continue
			}
			f.text = t
			f.raw = ""
			f.lang = lineLangLatin
			kept = append(kept, f)
			wrote = true
		}
		u.frags = kept
	}
}

func applyForeignDrops(units []subtitleFixUnit) {
	for i := range units {
		if !units[i].foreignDrop {
			continue
		}
		kept := make([]styledFrag, 0, len(units[i].frags))
		for _, f := range units[i].frags {
			if isThirdLangFrag(f) {
				continue
			}
			kept = append(kept, f)
		}
		units[i].frags = kept
	}
}

func dropRedundantUnits(entries []SubtitleEntry, units []subtitleFixUnit) ([]SubtitleEntry, []subtitleFixUnit) {
	keptEntries := make([]SubtitleEntry, 0, len(entries))
	keptUnits := make([]subtitleFixUnit, 0, len(units))
	for i := range units {
		if units[i].drop {
			continue
		}
		keptEntries = append(keptEntries, entries[i])
		keptUnits = append(keptUnits, units[i])
	}
	return keptEntries, keptUnits
}

func dedupeTexts(in []string) (uniq []string, index []int) {
	pos := make(map[string]int, len(in))
	index = make([]int, len(in))
	for i, t := range in {
		if j, ok := pos[t]; ok {
			index[i] = j
			continue
		}
		j := len(uniq)
		pos[t] = j
		uniq = append(uniq, t)
		index[i] = j
	}
	return uniq, index
}

func expandTexts(uniq []string, index []int) []string {
	out := make([]string, len(index))
	for i, j := range index {
		if j >= 0 && j < len(uniq) {
			out[i] = uniq[j]
		}
	}
	return out
}

func renderFixUnit(u subtitleFixUnit) string {
	if u.kind == fixKindEmpty && strings.TrimSpace(u.origText) != "" {
		return u.origText
	}
	var parts []string
	for _, f := range dedupeFragLines(u.frags) {
		t := strings.TrimSpace(f.text)
		if t == "" {
			continue
		}
		if f.raw != "" && t == strings.TrimSpace(stripAllTags(f.raw)) {
			parts = append(parts, f.prefix+strings.TrimSpace(f.raw)+f.suffix)
			continue
		}
		suffix := f.suffix
		if f.prefix == "" && strings.Contains(suffix, "</") {
			suffix = ""
		}
		parts = append(parts, f.prefix+t+suffix)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, u.joiner)
}

func summarizeFixUnits(units []subtitleFixUnit) string {
	var bi, pair, zh, la, ot, empty int
	var jaFrag, koFrag, laFrag int
	for _, u := range units {
		switch u.kind {
		case fixKindBilingual:
			bi++
		case fixKindPairedZH, fixKindPairedForeign:
			pair++
		case fixKindChinese:
			zh++
		case fixKindLatin:
			la++
		case fixKindOtherForeign:
			ot++
		default:
			empty++
		}
		for _, f := range u.frags {
			switch f.lang {
			case lineLangJapanese:
				jaFrag++
			case lineLangKorean:
				koFrag++
			case lineLangLatin:
				laFrag++
			}
		}
	}
	n := len(units) - empty
	if n < 0 {
		n = 0
	}
	label := "混合"
	switch {
	case n == 0:
		label = "空"
	case (bi+pair)*4 >= n && bi+pair > 0:
		switch {
		case jaFrag > 0 && laFrag == 0 && koFrag == 0:
			label = "中日双语"
		case koFrag > 0 && laFrag == 0 && jaFrag == 0:
			label = "中韩双语"
		default:
			label = "中英双语"
		}
	case zh*2 >= n && zh > 0 && bi+pair == 0:
		label = "仅中文"
	case la*2 >= n && la > 0 && bi+pair == 0:
		label = "仅英文"
	case ot*2 >= n && ot > 0:
		if jaFrag > 0 && koFrag == 0 {
			label = "日文"
		} else if koFrag > 0 && jaFrag == 0 {
			label = "韩文"
		} else {
			label = "其他外语"
		}
	}
	return label
}

func alreadyStackedBilingual(units []subtitleFixUnit) bool {
	for _, u := range units {
		if u.kind == fixKindBilingual || u.kind == fixKindPairedZH || u.kind == fixKindPairedForeign {
			return true
		}
	}
	return false
}

func flattenEntryText(raw string) string {
	t := strings.TrimSpace(stripAllTags(raw))
	t = strings.ReplaceAll(t, "\n", " ")
	return asrCleanSubtitleText(t)
}

func isChineseLineLang(l lineLang) bool {
	return l == lineLangChinese || l == lineLangMixed
}

func translateSourceText(u subtitleFixUnit, raw, targetLang string) string {
	target := strings.ToUpper(strings.TrimSpace(targetLang))
	var src string
	switch u.kind {
	case fixKindBilingual:
		if target == "ZH" {
			src = unitJoined(u, isForeignLineLang)
			if src == "" {
				src = unitJoined(u, isChineseLineLang)
			}
		} else {
			src = unitJoined(u, isChineseLineLang)
			if src == "" {
				src = unitJoined(u, isForeignLineLang)
			}
		}
	case fixKindPairedZH, fixKindChinese:
		src = unitJoined(u, isChineseLineLang)
	case fixKindPairedForeign, fixKindLatin, fixKindOtherForeign:
		src = unitJoined(u, isForeignLineLang)
	}
	if src == "" {
		src = flattenEntryText(raw)
	}
	return jobText(src)
}

func applyTranslatedText(u subtitleFixUnit, orig, translated, mode, targetLang string) string {
	translated = strings.TrimSpace(translated)
	if translated == "" {
		if subtitleTextIsJunk(orig) {
			return ""
		}
		return orig
	}
	target := strings.ToUpper(strings.TrimSpace(targetLang))
	if !strings.EqualFold(strings.TrimSpace(mode), "bilingual") {
		return translated
	}
	switch u.kind {
	case fixKindBilingual:
		frags := append([]styledFrag{}, u.frags...)
		want := lineLangChinese
		if target != "ZH" {
			want = lineLangLatin
		}
		found := false
		for i := range frags {
			if frags[i].lang == want || (want == lineLangChinese && frags[i].lang == lineLangMixed) {
				frags[i].text = translated
				found = true
				break
			}
		}
		if !found {
			frag := styledFrag{text: translated, lang: want}
			if want == lineLangChinese {
				frags = append([]styledFrag{frag}, frags...)
			} else {
				frags = append(frags, frag)
			}
		}
		u.frags = frags
		return renderFixUnit(u)
	case fixKindPairedZH:
		if target != "ZH" {
			return orig
		}
		return translated
	case fixKindPairedForeign:
		if target == "ZH" {
			return orig
		}
		return translated
	default:
		if strings.TrimSpace(stripAllTags(orig)) == translated {
			return orig
		}
		if target == "ZH" && u.kind == fixKindChinese {
			return translated
		}
		if target == "EN" && u.kind == fixKindLatin {
			return translated
		}
		if target == "ZH" {
			return translated + u.joiner + orig
		}
		return orig + u.joiner + translated
	}
}
