package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

var (
	chatCallCount    atomic.Int64
	chatCallProvider atomic.Value
)

func stripMarkdownFence(content string) string {
	content = strings.TrimSpace(content)
	if idx := strings.Index(content, "```"); idx != -1 {
		content = content[idx+3:]
		if strings.HasPrefix(content, "json") {
			content = content[4:]
		}
		if end := strings.LastIndex(content, "```"); end != -1 {
			content = content[:end]
		}
		content = strings.TrimSpace(content)
	}
	return content
}

type chatModelConfig struct {
	model    string
	thinking string
	effort   string
	timeout  time.Duration
}

var chatModelFast = chatModelConfig{
	model: "deepseek-flash", thinking: "disabled", timeout: 60 * time.Second,
}

func providerTemperature(provider string, t float64) float64 {
	if t < 0 {
		t = 0
	}
	if provider == "zhipu" {
		t = math.Round(t*50) / 100
		if t > 1 {
			t = 1
		}
		return t
	}
	if t > 2 {
		t = 2
	}
	return math.Round(t*100) / 100
}

func chatContent(apiKey, systemPrompt, userContent string, temperature float64, maxTokens int) (string, error) {
	return chatContentCfg(apiKey, systemPrompt, userContent, temperature, maxTokens, chatModelFast)
}

func chatContentCfg(apiKey, systemPrompt, userContent string, temperature float64, maxTokens int, cfg chatModelConfig) (string, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	provider := aiProvider()
	model := chatModelName(cfg.model)
	endpoint := chatCompletionsURL()
	if strings.TrimSpace(apiKey) == "" {
		apiKey = chatAPIKey()
	}
	if strings.TrimSpace(apiKey) == "" {
		if provider == "zhipu" {
			return "", fmt.Errorf("未配置 GLM API Key")
		}
		return "", fmt.Errorf("未配置 DeepSeek API Key")
	}
	temperature = providerTemperature(provider, temperature)

	bodyMap := map[string]interface{}{
		"model": model,
		"messages": []message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		"temperature":     temperature,
		"max_tokens":      maxTokens,
		"stream":          false,
		"response_format": map[string]string{"type": "json_object"},
	}
	if provider == "zhipu" {
		bodyMap["thinking"] = map[string]string{"type": "enabled"}
		bodyMap["reasoning_effort"] = "low"
	} else {
		thinkingType := cfg.thinking
		if thinkingType == "" {
			thinkingType = "disabled"
		}
		bodyMap["thinking"] = map[string]string{"type": thinkingType}
		if thinkingType == "enabled" && cfg.effort != "" {
			bodyMap["reasoning_effort"] = cfg.effort
		}
	}

	body, _ := json.Marshal(bodyMap)
	timeout := cfg.timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if provider == "zhipu" && timeout < 90*time.Second {
		timeout = 90 * time.Second
	}
	client := &http.Client{Timeout: timeout}

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, rerr := http.NewRequest("POST", endpoint, bytes.NewReader(body))
		if rerr != nil {
			return "", rerr
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		chatCallCount.Add(1)
		chatCallProvider.Store(aiProviderLabel())
		var derr error
		resp, derr = client.Do(req)
		if derr != nil {
			return "", fmt.Errorf("请求失败：%w", derr)
		}
		if (resp.StatusCode == 429 || resp.StatusCode == 503) && attempt < 2 {
			resp.Body.Close()
			time.Sleep(time.Duration(3*(attempt*2+1)) * time.Second)
			continue
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s", chatServiceError(resp.StatusCode, string(b)))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析失败：%w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("无返回结果")
	}

	rawContent := result.Choices[0].Message.Content
	if strings.TrimSpace(rawContent) == "" {
		return "", fmt.Errorf("AI 返回了空内容，请稍后重试")
	}

	return stripMarkdownFence(rawContent), nil
}

func subtitleJunkRule() string {
	return "清洗规则：" +
		"1. 每条只保留影视本身的台词、旁白、画面说明。" +
		"2. 删除与剧情无关的内容：字幕组或汉化组署名与招募、" +
		"制作名单（翻译/听译/时间轴/校对/润色/后期/压制/片源/特效/美工/海报/监制/统筹/出品等岗位及其人名）、" +
		"免责声明与24小时删除要求、网站域名与网址、邮箱、" +
		"网盘与下载链接、磁力、群号、" +
		"QQ群、微信群、公众号、二维码、扫码加群、" +
		"原创翻译、双语字幕、更多资源、欢迎关注等宣传语、" +
		"片头片尾的logo文字、纯符号或矢量图形装饰。" +
		"3. 无关内容与台词混在同一条时，只删无关部分，台词原样保留。" +
		"4. 整条都是无关内容时返回空字符串，不要为它编造或补充内容。" +
		"5. 判断不准是否与剧情无关时，原样保留，宁可少删不可误删。"
}

func subtitleJunkLineRule() string {
	return "清理：若某行本身就是组信息、广告、网址或纯符号装饰，返回空字符串；其余各行原样翻译，不做删减。"
}

func subtitleJunkRuleEnglish() string {
	return "Cleanup rules: " +
		"1. Keep only lines that belong to the film itself: dialogue, narration, on-screen text." +
		"2. Remove anything unrelated: fansub or release group credits and recruitment, " +
		"production role lists (translator, timer, editor, encoder, QC, typesetter, source, poster, supervisor etc. and their names), " +
		"disclaimers and 24-hour deletion notices, site URLs, domains, email addresses, " +
		"cloud-drive or download links, magnet links, group numbers, " +
		"QQ/WeChat/Telegram contacts, QR codes, promo lines such as original translation, bilingual subtitles, more resources, follow us, " +
		"logo text and decorative symbol or vector-art lines." +
		"3. When a line mixes junk and dialogue, remove only the junk and keep the dialogue unchanged." +
		"4. If a numbered item is entirely junk, return an empty string for it; never invent content." +
		"5. If unsure whether something is unrelated, keep it as is."
}

func callChatTextList(apiKey, systemPrompt, userContent string, temperature float64, maxTokens int) ([]string, error) {
	content, err := chatContent(apiKey, systemPrompt, userContent, temperature, maxTokens)
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		T []string `json:"t"`
	}
	if err := json.Unmarshal([]byte(content), &wrapper); err != nil {
		return nil, fmt.Errorf("结果格式异常，正在重试")
	}
	return wrapper.T, nil
}

func polishChineseTexts(apiKey string, texts, ctxSrc, ctxDst []string) ([]string, error) {
	var sb strings.Builder
	if len(ctxSrc) > 0 && len(ctxSrc) == len(ctxDst) {
		sb.WriteString("上一批上下文（仅供参考，不要再改）：\n")
		for i := range ctxSrc {
			sb.WriteString(fmt.Sprintf("- \"%s\" → \"%s\"\n", ctxSrc[i], ctxDst[i]))
		}
		sb.WriteString("\n")
	}
	for i, text := range texts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}

	systemPrompt := "你是" + brandActor() + "的专业字幕校对。输入是逐行编号的中文字幕，任务是逐行校对并统一为简体中文。" +
		"规则：" +
		"1. 整句用简体中文书写：原文里的繁体字一律换成对应的简体字，人名、地名、专有名词、原片字卡也不保留繁体写法。" +
		"2. 每条只输出该行的简体中文；英文由后续步骤逐行生成，这里不输出英文或任何外语，也不在括号里附原文。" +
		"3. 修正错别字、语病、同音错字，保持原意与说话人语气；原文通顺就照原样输出。" +
		"4. 原文是翻译腔、书面腔或机翻感时，按角色真会说的口语整句重写：调换语序、去掉冗余的「的/了/着」、把别扭的被动句改主动；只改表达不改内容，不添加原文没有的情节、解释或笑点。" +
		"5. 指代不清时把代词换成具体的人名或称呼，除此之外不增删内容。" +
		"6. 一条一行，不拆分不合并，不重复输出原文，不添加样式标签、特效代码或解释。" +
		"7. 每行不超过35个中文字符，过长则精简。" +
		"8. 按相同编号顺序返回，条数必须与输入一致。" +
		"9. 若提供了上一批上下文，只用来统一人名、称呼与语气，不要重改那些句子。" +
		"10. 语言要求优先于其它规则：判断不准时输出也必须是简体中文。" +
		subtitleJunkRule() +
		"输出前自查：行数与输入一致；每行都是简体中文；没有繁体字残留；没有英文残留。" +
		"返回格式：严格返回 JSON 对象 {\"t\":[\"校对后文本1\",\"校对后文本2\",...]}。" +
		"不要解释，不要 markdown，不要多余字段。"

	result, err := callChatTextList(apiKey, systemPrompt, sb.String(), 0.7, 8192)
	if err != nil {
		return nil, err
	}
	if len(result) != len(texts) {
		return nil, fmt.Errorf("修复结果数量异常，正在重试")
	}
	return result, nil
}

func cleanSuspiciousSubtitleAds(apiKey string, texts []string, suspicious []int) ([]string, int, error) {
	if len(suspicious) == 0 {
		return texts, 0, nil
	}
	const maxSuspiciousBatch = 25
	if len(suspicious) > maxSuspiciousBatch {
		refined := append([]string{}, texts...)
		totalRemoved := 0
		for start := 0; start < len(suspicious); start += maxSuspiciousBatch {
			end := start + maxSuspiciousBatch
			if end > len(suspicious) {
				end = len(suspicious)
			}
			next, removed, err := cleanSuspiciousSubtitleAds(apiKey, refined, suspicious[start:end])
			if err != nil {
				return refined, totalRemoved, err
			}
			refined = next
			totalRemoved += removed
		}
		return refined, totalRemoved, nil
	}
	const contextRadius = 3
	var sb strings.Builder
	for outIdx, textIdx := range suspicious {
		sb.WriteString(fmt.Sprintf("疑似条目 %d，对应原字幕序号 %d：\n", outIdx+1, textIdx+1))
		start := textIdx - contextRadius
		if start < 0 {
			start = 0
		}
		end := textIdx + contextRadius + 1
		if end > len(texts) {
			end = len(texts)
		}
		for i := start; i < end; i++ {
			mark := "上下文"
			if i == textIdx {
				mark = "当前"
			}
			line := strings.TrimSpace(texts[i])
			if line == "" {
				line = "空"
			}
			sb.WriteString(fmt.Sprintf("[%s %d] %s\n", mark, i+1, line))
		}
		sb.WriteString("\n")
	}

	systemPrompt := "你是" + brandActor() + "的专业字幕清理助手。你会收到若干疑似无关条目，每条都带前后对白上下文。" +
		"你的任务是判断当前条目是否与剧情相关。" +
		"规则：" +
		"1. 当前条目是影视资源、网盘、下载、磁力、QQ群、微信群、公众号、二维码、推广、水印、站点广告、字幕组署名、制作名单、免责声明、纯符号装饰，且与上下文剧情无关，返回 REMOVE。" +
		"2. 当前条目是正常台词、对白、旁白、画面说明，返回 KEEP，宁可保留也不要误删。" +
		"3. 当前条目混有无关内容和台词，只删除无关部分，返回 TEXT:后接清理后的台词。" +
		"4. 只能处理标记为当前的条目，上下文只用于判断，不能输出上下文内容。" +
		"5. 输出数组长度必须与疑似条目数量完全一致，顺序一致。" +
		"6. TEXT: 后的结果必须为简体中文。" +
		"返回格式：严格返回 JSON 对象 {\"t\":[\"KEEP\",\"REMOVE\",\"TEXT:清理后的字幕\"]}。" +
		"不要解释，不要 markdown，不要多余字段。"

	result, err := callChatTextList(apiKey, systemPrompt, sb.String(), 0.1, 4096)
	if err != nil {
		return texts, 0, err
	}
	if len(result) != len(suspicious) {
		return texts, 0, fmt.Errorf("AI 清理结果数量异常")
	}

	refined := append([]string{}, texts...)
	removed := 0
	for i, decision := range result {
		idx := suspicious[i]
		decision = strings.TrimSpace(decision)
		upper := strings.ToUpper(decision)
		switch {
		case upper == "REMOVE":
			if refined[idx] != "" {
				removed++
			}
			refined[idx] = ""
		case upper == "KEEP":
		case strings.HasPrefix(upper, "TEXT:"):
			cleaned := strings.TrimSpace(decision[len("TEXT:"):])
			cleaned = stripContactInfo(cleaned)
			cleaned = stripSubtitleAdText(cleaned)
			cleaned = asrCleanSubtitleText(cleaned)
			cleaned = mergeShortLines(cleaned)
			if cleaned == "" && refined[idx] != "" {
				removed++
			}
			refined[idx] = cleaned
		default:
		}
	}
	return refined, removed, nil
}
