package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func chatServiceError(statusCode int, body string) string {
	switch statusCode {
	case 401:
		return "翻译服务认证失败 — API Key 无效"
	case 402:
		return "翻译服务余额不足 — 暂时不可用，请稍后再试"
	case 422:
		return "翻译服务参数错误 — 请求格式异常"
	case 429:
		return "翻译服务请求频率超限 — 请稍后再试"
	case 500:
		return "翻译服务器故障 — 请稍后重试"
	case 503:
		return "翻译服务器繁忙 — 请稍后重试"
	default:
		msg := body
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Sprintf("翻译服务错误 %d: %s", statusCode, msg)
	}
}

func translateWithChat(apiKey, targetLang string, texts, ctxSrc, ctxDst []string) ([]string, error) {
	var sb strings.Builder
	promptLang := targetLang
	if strings.EqualFold(strings.TrimSpace(targetLang), "ZH") {
		promptLang = "Simplified Chinese (zh-Hans)"
	}
	sb.WriteString(fmt.Sprintf("Target language: %s\n", promptLang))
	if len(ctxSrc) > 0 && len(ctxSrc) == len(ctxDst) {
		sb.WriteString("\nPrevious context (do not translate, for reference only):\n")
		for i := range ctxSrc {
			sb.WriteString(fmt.Sprintf("- \"%s\" → \"%s\"\n", ctxSrc[i], ctxDst[i]))
		}
	}
	sb.WriteString("\n")
	for i, text := range texts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}

	systemPrompt := "You are a professional subtitle translator working for " + brandActor() + ", a Chinese fansubbing group. " +
		"Your translations must be: natural and colloquial (not literal), " +
		"consistent in character voice and tone throughout the episode, " +
		"faithful to the original meaning while adapting cultural references for Chinese audience, " +
		"concise enough to read comfortably as subtitles (avoid overly long lines). " +
		"You may receive previous translations as context — use them to keep names, tone and style consistent, but do not re-translate them. " +
		"Translate ONLY the numbered subtitle texts. " +
		"The first line specifies the target language. " +
		subtitleJunkRuleEnglish() +
		"The number of returned items must equal the number of input items, in the same order. " +
		"Return ONLY a json object in this exact format: {\"t\":[\"translated text 1\",\"translated text 2\",...]}. " +
		"No explanation, no markdown, no extra fields. Output must be valid json only."

	translated, err := callChatTextList(apiKey, systemPrompt, sb.String(), 1.3, 4096)
	if err != nil {
		return nil, err
	}

	if len(translated) > len(texts) {
		translated = translated[:len(texts)]
	}
	if len(translated) < len(texts) {
		if len(translated) == 0 {
			return nil, fmt.Errorf("翻译结果为空")
		}
		for len(translated) < len(texts) {
			translated = append(translated, "")
		}
	}

	return translated, nil
}

func translateDeepL(apiKey, targetLang string, texts []string) ([]string, error) {
	type reqBody struct {
		Text       []string `json:"text"`
		TargetLang string   `json:"target_lang"`
	}
	body, _ := json.Marshal(reqBody{Text: texts, TargetLang: strings.ToUpper(targetLang)})
	req, err := http.NewRequest("POST", "https://api-free.deepl.com/v2/translate", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "DeepL-Auth-Key "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("网络请求失败，请检查网络连接")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DeepL 翻译服务异常（状态码 %d）", resp.StatusCode)
	}
	var result struct {
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("DeepL 返回数据解析失败")
	}
	out := make([]string, len(result.Translations))
	for i, t := range result.Translations {
		out[i] = t.Text
	}
	return out, nil
}

func translateBaidu(appID, secretKey, targetLang string, texts []string) ([]string, error) {
	langMap := map[string]string{
		"ZH": "zh", "EN": "en", "JA": "jp", "KO": "kor",
		"FR": "fra", "DE": "de", "ES": "spa", "RU": "ru",
	}
	to := langMap[strings.ToUpper(targetLang)]
	if to == "" {
		to = strings.ToLower(targetLang)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	out := make([]string, len(texts))
	for i, text := range texts {
		salt := fmt.Sprintf("%d", time.Now().UnixNano())
		sign := fmt.Sprintf("%x", md5.Sum([]byte(appID+text+salt+secretKey)))
		params := url.Values{
			"q": {text}, "from": {"auto"}, "to": {to},
			"appid": {appID}, "salt": {salt}, "sign": {sign},
		}
		resp, err := client.Get("https://fanyi-api.baidu.com/api/trans/vip/translate?" + params.Encode())
		if err != nil {
			return nil, fmt.Errorf("网络请求失败，请检查网络连接")
		}
		var r struct {
			TransResult []struct {
				Dst string `json:"dst"`
			} `json:"trans_result"`
			ErrorCode string `json:"error_code"`
			ErrorMsg  string `json:"error_msg"`
		}
		err = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("百度翻译返回数据解析失败")
		}
		if r.ErrorCode != "" && r.ErrorCode != "0" {
			return nil, fmt.Errorf("百度翻译服务异常，请检查 API 配置")
		}
		if len(r.TransResult) == 0 {
			out[i] = text
		} else {
			out[i] = r.TransResult[0].Dst
		}
		time.Sleep(200 * time.Millisecond)
	}
	return out, nil
}

func translateTencent(secretID, secretKey, targetLang string, texts []string) ([]string, error) {
	to := strings.ToLower(targetLang)
	if to == "zh" {
		to = "zh"
	}

	host := "tmt.tencentcloudapi.com"
	service := "tmt"
	action := "TextTranslate"
	version := "2018-03-21"
	region := "ap-beijing"
	algorithm := "TC3-HMAC-SHA256"

	tc3Sha256Hex := func(s string) string {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	tc3HmacSHA256 := func(key []byte, data string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	out := make([]string, len(texts))
	for i, text := range texts {
		payload, _ := json.Marshal(map[string]interface{}{
			"SourceText": text,
			"Source":     "auto",
			"Target":     to,
			"ProjectId":  0,
		})
		payloadStr := string(payload)

		timestamp := time.Now().Unix()
		date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")

		canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-tc-action:%s\n",
			"application/json; charset=utf-8", host, strings.ToLower(action))
		signedHeaders := "content-type;host;x-tc-action"
		canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
			"POST", "/", "", canonicalHeaders, signedHeaders, tc3Sha256Hex(payloadStr))

		credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, service)
		stringToSign := fmt.Sprintf("%s\n%d\n%s\n%s",
			algorithm, timestamp, credentialScope, tc3Sha256Hex(canonicalRequest))

		secretDate := tc3HmacSHA256([]byte("TC3"+secretKey), date)
		secretService := tc3HmacSHA256(secretDate, service)
		secretSigning := tc3HmacSHA256(secretService, "tc3_request")
		signature := hex.EncodeToString(tc3HmacSHA256(secretSigning, stringToSign))

		authorization := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
			algorithm, secretID, credentialScope, signedHeaders, signature)

		req, err := http.NewRequest("POST", "https://"+host, strings.NewReader(payloadStr))
		if err != nil {
			return nil, fmt.Errorf("构造请求失败")
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("Host", host)
		req.Header.Set("Authorization", authorization)
		req.Header.Set("X-TC-Action", action)
		req.Header.Set("X-TC-Timestamp", fmt.Sprintf("%d", timestamp))
		req.Header.Set("X-TC-Version", version)
		req.Header.Set("X-TC-Region", region)

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("网络请求失败，请检查网络连接")
		}
		var r struct {
			Response struct {
				TargetText string `json:"TargetText"`
				Error      struct {
					Code    string `json:"Code"`
					Message string `json:"Message"`
				} `json:"Error"`
			} `json:"Response"`
		}
		err = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("腾讯翻译返回数据解析失败")
		}
		if r.Response.Error.Code != "" {
			return nil, fmt.Errorf("腾讯翻译服务异常: %s", r.Response.Error.Message)
		}
		out[i] = r.Response.TargetText
		time.Sleep(150 * time.Millisecond)
	}
	return out, nil
}

type TranslateSubtitleConfig struct {
	Provider   string `json:"provider"`
	Key1       string `json:"key1"`
	Key2       string `json:"key2"`
	TargetLang string `json:"targetLang"`
	Mode       string `json:"mode"`
	ForceSRT   bool   `json:"forceSRT"`
}

func deepseekKey() string {
	return keys().DeepSeekKey
}

func translateProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "deepseek":
		return aiProviderLabel()
	case "deepl":
		return "DeepL"
	case "baidu":
		return "百度翻译"
	case "tencent":
		return "腾讯翻译君"
	default:
		return provider
	}
}

const translateBatchConcurrency = 4

type batchRange struct{ start, end int }

func splitBatchRanges(total, size int) []batchRange {
	if total <= 0 || size <= 0 {
		return nil
	}
	ranges := make([]batchRange, 0, (total+size-1)/size)
	for start := 0; start < total; start += size {
		end := start + size
		if end > total {
			end = total
		}
		ranges = append(ranges, batchRange{start, end})
	}
	return ranges
}

func (a *App) TranslateSubtitle(inputFile, outputDir string, cfg TranslateSubtitleConfig) bool {
	if inputFile == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	if outputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}
	if !a.beginJob() {
		return false
	}
	defer a.endJob()
	chatKey := chatAPIKey()
	if chatKey == "" && aiProvider() == "deepseek" {
		chatKey = strings.TrimSpace(cfg.Key1)
	}
	if cfg.Provider == "deepseek" {
		if chatKey == "" {
			a.log("error", "未配置 AI Key")
			return false
		}
	} else if cfg.Key1 == "" {
		a.log("error", "未填写 Key")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	entries := a.LoadSubtitle(inputFile)
	if len(entries) == 0 {
		a.log("error", "字幕解析失败或为空")
		return false
	}

	entries, removedCredits := cleanSubtitleCreditEntries(entries)
	if removedCredits > 0 {
		a.log("info", fmt.Sprintf("移除无关条目 %d 条", removedCredits))
	}
	if len(entries) == 0 {
		a.log("error", "清理后字幕为空")
		return false
	}

	origTexts := make([]string, len(entries))
	cleanTexts := make([]string, len(entries))
	fixUnits := planSubtitleFix(entries)
	shape := summarizeFixUnits(fixUnits)
	a.log("info", fmt.Sprintf("字幕语言 %s · %d 条", shape, len(entries)))
	if alreadyStackedBilingual(fixUnits) && cfg.Mode == "bilingual" {
		a.log("info", "中外叠显，仅译目标侧")
	}
	for i, e := range entries {
		origTexts[i] = e.Text
		cleanTexts[i] = translateSourceText(fixUnits[i], e.Text, cfg.TargetLang)
		if cleanTexts[i] == "" {
			cleanTexts[i] = "…"
		}
	}

	a.log("info", fmt.Sprintf("翻译 %d 条 · %s → %s", len(entries), translateProviderLabel(cfg.Provider), cfg.TargetLang))

	ext := filepath.Ext(inputFile)
	stem := strings.TrimSuffix(filepath.Base(inputFile), ext)
	suffix := "." + strings.ToLower(cfg.TargetLang)
	if cfg.Mode == "bilingual" {
		suffix = ".bilingual"
	}
	outExt := ".srt"
	if (ext == ".ass" || ext == ".ssa") && !cfg.ForceSRT {
		outExt = ".ass"
	}
	outFile := filepath.Join(outputDir, stem+suffix+outExt)

	isASS := outExt == ".ass"

	var srtFile *os.File
	if !isASS {
		var ferr error
		srtFile, ferr = os.Create(outFile)
		if ferr != nil {
			a.log("error", "创建输出文件失败: "+ferr.Error())
			return false
		}
		defer func() {
			srtFile.Close()
		}()
	}

	const contextLines = 5
	var prevCtxSrc, prevCtxDst []string

	translateBatch := func(texts []string) ([]string, error) {
		switch cfg.Provider {
		case "deepseek":
			return translateWithChat(chatKey, cfg.TargetLang, texts, prevCtxSrc, prevCtxDst)
		case "baidu":
			return translateBaidu(cfg.Key1, cfg.Key2, cfg.TargetLang, texts)
		case "tencent":
			return translateTencent(cfg.Key1, cfg.Key2, cfg.TargetLang, texts)
		default:
			return translateDeepL(cfg.Key1, cfg.TargetLang, texts)
		}
	}

	isFatalErr := func(err error) bool {
		msg := err.Error()
		return strings.Contains(msg, "余额不足") || strings.Contains(msg, "认证失败")
	}
	isRateLimited := func(err error) bool {
		return strings.Contains(err.Error(), "频率超限")
	}

	const maxSplitDepth = 4
	var translateWithSplit func(texts []string, depth int) ([]string, error)
	translateWithSplit = func(texts []string, depth int) ([]string, error) {
		const maxRetry = 3
		var results []string
		var err error
		for attempt := 1; attempt <= maxRetry; attempt++ {
			if a.stopped.Load() {
				return nil, errCancelled
			}
			results, err = translateBatch(texts)
			if err == nil {
				return results, nil
			}
			if isFatalErr(err) {
				return nil, err
			}
			if attempt < maxRetry {
				if !a.sleepOrStop(time.Duration(attempt) * 2 * time.Second) {
					return nil, errCancelled
				}
			}
		}
		if isRateLimited(err) {
			return nil, fmt.Errorf("请求频率持续超限，已停止翻译以避免费用放大，请稍后重试")
		}
		if depth >= maxSplitDepth || len(texts) <= 5 {
			if depth == 0 {
				a.log("warn", fmt.Sprintf("翻译失败 %d 条，逐条重试", len(texts)))
			}
			fallback := make([]string, len(texts))
			kept := 0
			for i, t := range texts {
				if a.stopped.Load() {
					return nil, errCancelled
				}
				sr, se := translateBatch([]string{t})
				if se != nil && isFatalErr(se) {
					return nil, se
				}
				if se == nil && len(sr) > 0 && sr[0] != "" {
					fallback[i] = sr[0]
				} else {
					fallback[i] = t
					kept++
				}
			}
			if kept > 0 {
				a.log("warn", fmt.Sprintf("保留原文 %d 条", kept))
			}
			return fallback, nil
		}
		mid := len(texts) / 2
		if depth == 0 {
			a.log("warn", fmt.Sprintf("翻译失败 %d 条，拆分重试", len(texts)))
		}
		left, errL := translateWithSplit(texts[:mid], depth+1)
		if errL != nil {
			return nil, errL
		}
		right, errR := translateWithSplit(texts[mid:], depth+1)
		if errR != nil {
			return nil, errR
		}
		return append(left, right...), nil
	}

	const batchSize = 50
	translated := make([]string, len(entries))
	globalIdx := 1

	batches := splitBatchRanges(len(cleanTexts), batchSize)

	prefetch := cfg.Provider != "deepseek" && len(batches) > 1
	var fetchBatch func(bi int) ([]string, error)
	if prefetch {
		outcomes := make([]struct {
			results []string
			err     error
		}, len(batches))
		dones := make([]chan struct{}, len(batches))
		for i := range dones {
			dones[i] = make(chan struct{})
		}
		prefetchCtx, cancelPrefetch := context.WithCancel(context.Background())
		defer cancelPrefetch()

		sem := make(chan struct{}, translateBatchConcurrency)
		for i, r := range batches {
			go func(i int, r batchRange) {
				defer close(dones[i])
				select {
				case <-prefetchCtx.Done():
					outcomes[i].err = errCancelled
					return
				case sem <- struct{}{}:
				}
				defer func() { <-sem }()
				if a.stopped.Load() {
					outcomes[i].err = errCancelled
					return
				}
				outcomes[i].results, outcomes[i].err = translateWithSplit(cleanTexts[r.start:r.end], 0)
			}(i, r)
		}
		a.log("info", fmt.Sprintf("并发翻译 %d 批 / 并发 %d", len(batches), translateBatchConcurrency))
		fetchBatch = func(bi int) ([]string, error) {
			<-dones[bi]
			return outcomes[bi].results, outcomes[bi].err
		}
	} else {
		fetchBatch = func(bi int) ([]string, error) {
			if a.stopped.Load() {
				return nil, errCancelled
			}
			return translateWithSplit(cleanTexts[batches[bi].start:batches[bi].end], 0)
		}
	}

	for bi, r := range batches {
		start, end := r.start, r.end
		batch := cleanTexts[start:end]

		results, err := fetchBatch(bi)
		if err != nil {
			switch {
			case isCancelled(err):
				a.log("warn", fmt.Sprintf("翻译已取消 %d / %d", start, len(entries)))
			case isFatalErr(err):
				a.log("error", err.Error())
			default:
				a.log("error", fmt.Sprintf("翻译失败 %d-%d: %s", start+1, end, err.Error()))
			}
			if isFatalErr(err) && !isASS && srtFile != nil {
				srtFile.Close()
				os.Remove(outFile)
			}
			return false
		}
		for ri, res := range results {
			if res != "" {
				continue
			}
			if a.stopped.Load() {
				results[ri] = batch[ri]
				continue
			}
			sr, se := translateBatch([]string{batch[ri]})
			switch {
			case se != nil:
				results[ri] = batch[ri]
				a.log("warn", fmt.Sprintf("补译失败 %d，保留原文", start+ri+1))
			case len(sr) == 0:
				results[ri] = batch[ri]
				a.log("warn", fmt.Sprintf("补译失败 %d，保留原文", start+ri+1))
			case strings.TrimSpace(sr[0]) != "":
				results[ri] = sr[0]
				a.log("info", fmt.Sprintf("补译 %d", start+ri+1))
			default:
				results[ri] = ""
				a.log("info", fmt.Sprintf("移除无关条目 %d", start+ri+1))
			}
		}
		copy(translated[start:end], results)

		if cfg.Provider == "deepseek" {
			ctxStart := 0
			if len(batch) > contextLines {
				ctxStart = len(batch) - contextLines
			}
			prevCtxSrc = append([]string{}, batch[ctxStart:]...)
			prevCtxDst = append([]string{}, results[ctxStart:]...)
		}

		a.log("info", fmt.Sprintf("进度 %d / %d", end, len(entries)))

		if !isASS {
			for i, res := range results {
				entryIdx := start + i
				e := entries[entryIdx]
				text := applyTranslatedText(fixUnits[entryIdx], origTexts[entryIdx], res, cfg.Mode, cfg.TargetLang)
				if strings.TrimSpace(stripAllTags(text)) == "" {
					continue
				}
				if _, werr := fmt.Fprintf(srtFile, "%d\n%s --> %s\n%s\n\n", globalIdx, e.Start, e.End, text); werr != nil {
					a.log("error", "写入字幕文件失败: "+werr.Error())
					srtFile.Close()
					os.Remove(outFile)
					return false
				}
				globalIdx++
			}
		}
	}

	if isASS {
		outEntries := make([]SubtitleEntry, 0, len(entries))
		for i, e := range entries {
			newEntry := e
			newEntry.Text = applyTranslatedText(fixUnits[i], origTexts[i], translated[i], cfg.Mode, cfg.TargetLang)
			if strings.TrimSpace(stripAllTags(newEntry.Text)) == "" {
				continue
			}
			outEntries = append(outEntries, newEntry)
		}
		if len(outEntries) == 0 {
			a.log("error", "翻译后字幕为空")
			return false
		}
		if !a.SaveSubtitle(inputFile, outFile, outEntries) {
			return false
		}
	}

	a.log("ok", fmt.Sprintf("%s 翻译 → %s", translateProviderLabel(cfg.Provider), filepath.Base(outFile)))
	return true
}
