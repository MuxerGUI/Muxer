package main

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	reCreditDisclaimer = regexp.MustCompile(
		`(?:请|請|敬请|敬請)?(?:于|於)?(?:下载|下載)?后?\s*(?:的)?\s*24\s*(?:小时|小時|h|hr|hrs|hours?)\s*(?:内|內|之内|之內)?\s*(?:删除|刪除|自行删除|自行刪除|删|刪)` +
			`|(?:仅供|僅供|只供|仅作|僅作)(?:交流|學習|学习|試看|试看|参考|參考|观赏|觀賞|测试|測試|个人|個人|预览|預覽)` +
			`|(?:禁止|请勿|請勿|严禁|嚴禁|谢绝|謝絕|不得)(?:转载|轉載|用于商业|用於商業|商用|二次上传|二次上傳|售卖|售賣|盈利|牟利)` +
			`|(?:侵删|侵刪|侵权(?:请|請|即)?(?:联系|聯繫|删除|刪除))`)

	reCreditGroup = regexp.MustCompile(
		`[\p{Han}A-Za-z0-9]{2,16}(?:字幕|漢化|汉化|压制|壓制|发布|發布|资源|資源)(?:组|組|社|网|網|站|团队|團隊|工作室)` +
			`|(?:字幕|漢化|汉化|压制|壓制|发布|發布|资源|資源)(?:组|組|社|网|網|站)` +
			`|本(?:片|字幕|影片|影視|影视|作品)(?:由|来自|來自|出品|制作|製作|压制|壓制|发布|發布)`)

	reCreditBanner = regexp.MustCompile(
		`(?i)原创翻译|原創翻譯|原创字幕|原創字幕|双语字幕|雙語字幕|中英字幕|中日字幕|简繁字幕|簡繁字幕|` +
			`字幕制作|字幕製作|字幕组招募|字幕組招募|招募翻译|招募翻譯|长期招募|長期招募|长期招收|長期招收|` +
			`字幕(?:翻译|翻譯|校对|校對|润色|潤色|压制|壓制)|(?:基于|基於|根据|根據).{0,12}(?:字幕|原文|英字)|` +
			`招新|作品首发|作品首發`)

	reCreditPromo = regexp.MustCompile(
		`(?i)公众号|公眾號|二维码|二維碼|扫码|掃碼|加群|进群|進群|QQ群|关注我们|關注我們|订阅我们|訂閱我們|更多资源|更多資源|获取资源|獲取資源`)

	reCreditSiteName = regexp.MustCompile(
		`(?i)豆瓣|douban|微博|weibo|贴吧|貼吧|知乎|小红书|小紅書|快手|抖音|bilibili|b站`)
	reCreditSiteCue = regexp.MustCompile(
		`(?i)关注|關注|搜索|搜尋|小组|小組|账号|帳號|主页|主頁|官方|私信|超话|超話|@|＠|[:：]|\bid\b`)

	reCreditRole = regexp.MustCompile(
		`(?:翻译|翻譯|听译|聽譯|校对|校對|润色|潤色|时间轴|時間軸|时轴|時軸|打轴|打軸|后期|後期|压制|壓制|片源|特效|美工|海报|海報|监制|監製|统筹|統籌|出品|发布|發布|分割|字体|字體)\s*[:：]`)

	reCreditRoleStart = regexp.MustCompile(
		`^\s*(?:翻译|翻譯|听译|聽譯|校对|校對|润色|潤色|时间轴|時間軸|时轴|時軸|打轴|打軸|后期|後期|压制|壓制|片源|特效|美工|海报|海報|监制|監製|统筹|統籌|出品|发布|發布|字体|字體)\s*[:：]`)

	reCreditEnglish = regexp.MustCompile(
		`(?i)this\s+(?:subtitle|subtitles|sub|fansub)\s+(?:is|was)\s+(?:produced|made|brought|translated|created|presented)` +
			`|(?:please\s+)?delete\s+(?:it\s+)?(?:within|after|in)\s*(?:24|twenty[\s\-]?four)\s*hours?` +
			`|for\s+(?:communication|preview|study|learning|exchange|research|personal)\s*(?:and\s+\w+\s+)?(?:purpose|use|only)` +
			`|(?:subtitles?|sync(?:ed)?|timed?|encoded|ripped|captioned|translated)\s+by\s` +
			`|do\s+not\s+(?:sell|reupload|redistribute|share|upload)` +
			`|please\s+(?:support|visit|join)\s+us`)
)

func subtitleCreditLineReason(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	switch {
	case isSubtitleDrawingText(line):
		return "矢量绘图"
	case isSubtitleOrnamentText(line):
		return "符号装饰"
	case reCreditDisclaimer.MatchString(line):
		return "免责/删除声明"
	case reCreditBanner.MatchString(line):
		return "片头字幕卡"
	case reCreditEnglish.MatchString(line):
		return "英文组信息"
	case reCreditGroup.MatchString(line):
		return "字幕组署名/招募"
	case reCreditPromo.MatchString(line):
		return "导流"
	case reCreditSiteName.MatchString(line) && reCreditSiteCue.MatchString(line):
		return "站点/社媒"
	case reSubtitleAdCertain.MatchString(line):
		return "资源/网盘推广"
	case reCreditRoleStart.MatchString(line):
		return "制作名单"
	case len(reCreditRole.FindAllString(line, -1)) >= 2:
		return "制作名单"
	}
	return ""
}

func isSubtitleDrawingText(s string) bool {
	plain := strings.TrimSpace(stripAllTags(s))
	if plain == "" {
		return false
	}
	cmd, digit := 0, 0
	for _, r := range plain {
		switch {
		case r >= '0' && r <= '9':
			digit++
		case r == 'm' || r == 'l' || r == 'b' || r == 's' || r == 'p' || r == 'c' || r == 'n':
			cmd++
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '.' || r == ',' || r == '-' || r == '+':
		default:
			return false
		}
	}
	return cmd >= 2 && digit >= 4
}

func isSubtitleOrnamentText(s string) bool {
	plain := strings.TrimSpace(stripAllTags(s))
	if plain == "" {
		return false
	}
	for _, r := range plain {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
		if r == '♪' || r == '♫' || r == '♬' {
			return false
		}
	}
	return true
}

func isSubtitleTitleCard(plain string) bool {
	t := strings.TrimSpace(plain)
	if t == "" {
		return false
	}
	pairs := [][2]string{{"《", "》"}, {"〈", "〉"}, {"「", "」"}, {"『", "』"}}
	for _, p := range pairs {
		if !strings.HasPrefix(t, p[0]) || !strings.HasSuffix(t, p[1]) {
			continue
		}
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, p[0]), p[1]))
		if inner == "" {
			return false
		}
		if strings.ContainsAny(inner, "《》〈〉「」『』。！？，、；") {
			return false
		}
		return len([]rune(inner)) <= 24
	}
	return false
}

func subtitleNotDialogue(text string) bool {
	plain := strings.TrimSpace(stripAllTags(text))
	if plain == "" {
		return false
	}
	return isSubtitleTitleCard(plain) || subtitleCreditLineReason(plain) != ""
}

func isSubtitleCreditEntry(plain string) bool {
	lines := splitNonEmptyLines(plain)
	if len(lines) == 0 {
		return false
	}
	junk := 0
	roles := map[string]bool{}
	for _, line := range lines {
		if subtitleCreditLineReason(line) != "" {
			junk++
		}
		for _, m := range reCreditRole.FindAllString(line, -1) {
			roles[m] = true
		}
	}
	if junk == len(lines) {
		return true
	}
	return len(roles) >= 2
}

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(expandSubtitleBreaks(s), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitRawSubtitleLines(raw string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(raw); {
		if raw[i] == '\\' && i+1 < len(raw) && (raw[i+1] == 'N' || raw[i+1] == 'n') {
			out = append(out, cur.String())
			cur.Reset()
			i += 2
			continue
		}
		if raw[i] == '\n' || raw[i] == '\r' {
			if raw[i] == '\r' && i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			out = append(out, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(raw[i])
		i++
	}
	out = append(out, cur.String())
	return out
}

func subtitleLineSep(raw string) string {
	if strings.Contains(raw, `\N`) || strings.Contains(raw, `\n`) {
		return `\N`
	}
	return "\n"
}

func cleanSubtitleCreditText(raw string) (text string, drop bool) {
	if strings.TrimSpace(raw) == "" {
		return raw, false
	}
	if isSubtitleCreditEntry(stripAllTags(raw)) {
		return "", true
	}
	sep := subtitleLineSep(raw)
	kept := make([]string, 0, 4)
	for _, line := range splitRawSubtitleLines(raw) {
		plain := strings.TrimSpace(stripAllTags(line))
		if plain == "" {
			continue
		}
		if subtitleCreditLineReason(plain) != "" {
			continue
		}
		cleaned := strings.TrimSpace(stripContactInfo(line))
		if strings.TrimSpace(stripAllTags(cleaned)) == "" {
			continue
		}
		kept = append(kept, cleaned)
	}
	if len(kept) == 0 {
		return "", true
	}
	return strings.Join(kept, sep), false
}

func subtitleTextIsJunk(text string) bool {
	plain := strings.TrimSpace(stripAllTags(text))
	if plain == "" {
		return true
	}
	lines := splitNonEmptyLines(plain)
	if len(lines) == 0 {
		return true
	}
	for _, line := range lines {
		if !isSubtitleCreditEntry(line) && subtitleCreditLineReason(line) != "" {
			return true
		}
	}
	if isSubtitleCreditEntry(plain) || isSuspiciousSubtitleAd(plain) {
		return true
	}
	return strings.TrimSpace(stripContactInfo(plain)) == ""
}

func cleanSubtitleCreditEntries(entries []SubtitleEntry) ([]SubtitleEntry, int) {
	out := make([]SubtitleEntry, 0, len(entries))
	removed := 0
	for _, e := range entries {
		text, drop := cleanSubtitleCreditText(e.Text)
		if drop {
			removed++
			continue
		}
		e.Text = text
		out = append(out, e)
	}
	return out, removed
}
