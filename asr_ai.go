package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

func asrCorrectChinese(apiKey string, texts, ctxSrc, ctxDst []string) ([]string, error) {
	var sb strings.Builder
	if len(ctxSrc) > 0 && len(ctxSrc) == len(ctxDst) {
		sb.WriteString("上一批上下文（仅供参考，不要修正）：\n")
		for i := range ctxSrc {
			sb.WriteString(fmt.Sprintf("- \"%s\" → \"%s\"\n", ctxSrc[i], ctxDst[i]))
		}
		sb.WriteString("\n")
	}
	for i, text := range texts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}

	systemPrompt := "你是" + brandActor() + "的专业字幕校对专家。以下是电影/电视剧的 ASR 自动语音识别结果，可能包含以下问题：" +
		"1. 方言误识：各地方言被错误转写（如闽南语「开刀」被识别为「买地」，粤语词汇被误写等）。" +
		"2. 同音错字：语音相似但文字错误（如人名、地名、专有名词）。" +
		"3. 语义不通：结合上下文修正不合逻辑的句子。" +
		"4. 无意义语气词重复或 ASR 幻觉文本：删除或精简。" +
		"5. 外语片段（英文/日文/韩文等）必须翻译为简体中文，绝对不能原样保留外语，输出里不允许出现外语原文。" +
		"修正规则：" +
		"1. 统一输出简体中文，口语化，适合字幕阅读。" +
		"2. 保持原始语义和说话风格，不要过度书面化。" +
		"3. 每行不超过35个中文字符。" +
		"4. 如果原文已经是正确的中文，保持不变；但此规则不适用于外语——外语一律翻译为中文。" +
		"5. 行号必须与输入完全一致，每个行号返回且仅返回一个值。" +
		"6. 如果提供了上一批上下文，请参考其中的人名、专有名词和风格保持一致，但不要重复修正。" +
		"7. 每一行独立修正，禁止把某一行的内容移动、合并或拆分到其它行号。" +
		"8. 每行结尾不要保留句号（。或 .），但保留问号、感叹号、省略号等。" +
		subtitleJunkRule() +
		"返回格式：严格返回 JSON 对象，键为输入的行号字符串、值为该行修正后的文本，" +
		"例如 {\"1\":\"修正后文本\",\"2\":\"\"}。" +
		"必须为每一个输入行号返回且仅返回一个对应值，行号集合与输入完全一致。" +
		"不要有任何解释、markdown、多余字段。输出必须是合法 JSON。"

	content, err := chatContentCfg(apiKey, systemPrompt, sb.String(), 0.7, 16384, chatModelFast)
	if err != nil {
		return nil, err
	}
	return asrParseKeyedCorrection(content, len(texts))
}

func asrTranslateToEnglish(apiKey string, srcTexts, ctxSrc, ctxDst []string) ([]string, error) {
	if len(srcTexts) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	if len(ctxSrc) > 0 && len(ctxSrc) == len(ctxDst) {
		sb.WriteString("上一批已定译法（保持人名、称呼、专有名词一致，不要重译）：\n")
		for i := range ctxSrc {
			sb.WriteString(fmt.Sprintf("- \"%s\" → \"%s\"\n", ctxSrc[i], ctxDst[i]))
		}
		sb.WriteString("\n")
	}
	for i, text := range srcTexts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}
	systemPrompt := "你是" + brandActor() + "的专业字幕译者。输入是逐行编号的电影/电视剧对白，源语言可能是中文，也可能是日文、韩文、俄文等其它语言；一律逐行译成英文字幕。" +
		"译法：" +
		"1. 译成英语母语者日常会说的话：用缩写（don't / I'm / gonna / what's），用短句，不用书面语；按英语的说法整句重写，不按源语言语序搬。" +
		"2. 称呼、成语、俗语按语境意译；没有对等说法时保意舍形，不硬凑谐音、不生造俚语。" +
		"3. 先通读整批，看清谁在对谁说话、什么语气，再逐行落笔；每行只输出一行，行号与输入一一对应，禁止合并、拆分、省略或调换顺序。" +
		"4. 原文是半句，译文也是半句，不要把它补成完整句。" +
		"5. 译文只含英文：不附原文、不附拼音、不加括号注释或解释。" +
		"6. 不使用网络流行语和时代错位的梗，语域贴合作品本身的年代与场合。" +
		"7. 每行不超过42字符，过长就精简到意思不丢；行尾不用句号，保留问号、感叹号、省略号。" +
		"8. 上一批已定的人名、称呼、专有名词译法保持一致。" +
		subtitleJunkLineRule() +
		"输出前自查：行数与输入一致；每行都是英文且不超过42字符；没有把两句并成一句。" +
		"返回格式：严格返回 JSON 对象，键为输入行号字符串、值为该行英文译文，" +
		"例如 {\"1\":\"English\",\"2\":\"\"}。必须为每个输入行号返回且仅返回一个值，集合与输入完全一致。" +
		"不要有任何解释、markdown、多余字段。输出必须是合法 JSON。"
	content, err := chatContentCfg(apiKey, systemPrompt, sb.String(), 0.7, 16384, chatModelFast)
	if err != nil {
		return nil, err
	}
	return asrParseKeyedCorrection(content, len(srcTexts))
}

func asrTranslateToChinese(apiKey string, srcTexts, ctxSrc, ctxDst []string) ([]string, error) {
	if len(srcTexts) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	if len(ctxSrc) > 0 && len(ctxSrc) == len(ctxDst) {
		sb.WriteString("上一批已定译法（保持人名、称呼、专有名词一致，不要重译）：\n")
		for i := range ctxSrc {
			sb.WriteString(fmt.Sprintf("- \"%s\" → \"%s\"\n", ctxSrc[i], ctxDst[i]))
		}
		sb.WriteString("\n")
	}
	for i, text := range srcTexts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}
	systemPrompt := "你是" + brandActor() + "的专业字幕译者。输入是逐行编号的外语（英文、日文、韩文、俄文等）电影/电视剧对白，一律逐行译成简体中文字幕。" +
		"译法：" +
		"1. 译成中国人日常会说的话：语序、语气词、称呼都按中文习惯，不照搬外语句式。" +
		"2. 按中文口语整句重写；禁用外语句式的生硬对应，如「你知道的」「我的天哪」「噢我的上帝」「听起来不错」「让我们…吧」，该说「我跟你说」「天哪」「听着」「那就这样」就那样说。" +
		"3. 先通读整批，确定人物关系与语气再逐行落笔；每行只输出一行，行号与输入一一对应，禁止合并、拆分、省略或调换顺序。" +
		"4. 原文是半句，译文也是半句，不要补成完整句。" +
		"5. 指代不清时把代词换成具体的人名或称呼，别留「他」「那个东西」这类歧义；除此之外不增删内容。" +
		"6. 译文只含简体中文：不保留外语原词、不在括号里附原文；品牌、作品名、缩写等确属专有名词的可保留外文。" +
		"7. 不使用网络流行语和时代错位的梗，语域贴合作品本身的年代与场合。" +
		"8. 每行不超过35个中文字符，过长就精简；行尾不用句号，保留问号、感叹号、省略号。" +
		"9. 上一批已定的人名、称呼、专有名词译法保持一致。" +
		subtitleJunkLineRule() +
		"输出前自查：行数与输入一致；每行都是简体中文且不超过35字；没有残留外语。" +
		"返回格式：严格返回 JSON 对象，键为输入行号字符串、值为该行中文译文，" +
		"例如 {\"1\":\"中文\",\"2\":\"\"}。必须为每个输入行号返回且仅返回一个值，集合与输入完全一致。" +
		"不要有任何解释、markdown、多余字段。输出必须是合法 JSON。"
	content, err := chatContentCfg(apiKey, systemPrompt, sb.String(), 0.7, 16384, chatModelFast)
	if err != nil {
		return nil, err
	}
	return asrParseKeyedCorrection(content, len(srcTexts))
}

func asrCorrectEnglish(apiKey string, enTexts []string) ([]string, error) {
	if len(enTexts) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	for i, text := range enTexts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}
	systemPrompt := "你是" + brandActor() + "的专业字幕校对。以下是英文电影/电视剧字幕（可能是 ASR 识别结果，也可能是现成字幕），" +
		"常见问题：听错的词（同音/近音，尤其人名地名专有名词）、口水词（um, uh, er, you know, I mean）、" +
		"口吃与重复（it's it's）、半截重启的句子（I was— I went）。请逐行修正为干净、自然的英文字幕。" +
		"规则：" +
		"1. 只修正英文、保持英文，绝对不要翻译成中文或其它语言。" +
		"2. 结合上下文修正听错的词和专有名词。" +
		"3. 删除口水词、口吃重复、无意义语气词。" +
		"4. 不要改写、缩短或删减正常内容，尽量保留原意与措辞。" +
		"5. 每一行独立修正，禁止合并、拆分或移动行号。" +
		"6. 行号必须与输入完全一致，每个行号返回且仅返回一个值。" +
		"7. 行尾不要句号，但保留问号、感叹号、省略号。" +
		subtitleJunkRule() +
		"返回格式：严格返回 JSON 对象，键为输入行号字符串、值为该行修正后英文，" +
		"例如 {\"1\":\"English\",\"2\":\"\"}。必须为每个输入行号返回且仅返回一个值，集合与输入完全一致。" +
		"不要有任何解释、markdown、多余字段。输出必须是合法 JSON。"
	content, err := chatContentCfg(apiKey, systemPrompt, sb.String(), 0.7, 16384, chatModelFast)
	if err != nil {
		return nil, err
	}
	return asrParseKeyedCorrection(content, len(enTexts))
}

func asrProofreadEnglish(apiKey string, enTexts []string) ([]string, error) {
	if len(enTexts) == 0 {
		return nil, nil
	}
	var sb strings.Builder
	for i, text := range enTexts {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, text))
	}
	systemPrompt := "你是" + brandActor() + "的字幕校对。以下是已经做好的英文电影/电视剧字幕，本身已是成品。只改明确的错误：" +
		"1. 上下文足以确定唯一正确写法的同音误听词、人名地名的明显拼错。" +
		"2. 相邻重复的同一虚词（如 the the、I I）、敲错的连续字符。" +
		"3. 未说完就被打断后重来的半句重启（如 I was— I went）。" +
		"保持原样，不要改动：" +
		"用词与措辞、缩写形式（don't 与 do not 保持原样）、标点风格（弯引号、省略号、破折号、行尾句点原样保留）、" +
		"大小写（全大写喊叫保持原样）、um / uh / er / you know / I mean 等口语词与语气词、俚语与年代用语。" +
		"没有上述明确错误时，原样返回该行文字，一个字都不要改；不要润色、不要改写、不要精简、不要合并或拆分。" +
		"输出只含英文，不翻译、不加括号注释、不解释。" +
		"每一行独立处理，行号与输入完全一致，每个行号返回且仅返回一个值。" +
		"返回格式：严格返回 JSON 对象，键为输入行号字符串、值为该行文字，" +
		"例如 {\"1\":\"English\",\"2\":\"\"}。必须为每个输入行号返回且仅返回一个值，集合与输入完全一致。" +
		"不要有任何解释、markdown、多余字段。输出必须是合法 JSON。"
	content, err := chatContentCfg(apiKey, systemPrompt, sb.String(), 0.3, 16384, chatModelFast)
	if err != nil {
		return nil, err
	}
	return asrParseKeyedCorrection(content, len(enTexts))
}

type asrBilingualPlan struct {
	zh, en []string

	enFixIdx []int
	enFixTxt []string
	z2eIdx   []int
	z2eTxt   []string
	e2zIdx   []int
	e2zTxt   []string
}

func planASRBilingual(origTexts, corrected []string) asrBilingualPlan {
	n := len(corrected)
	p := asrBilingualPlan{zh: make([]string, n), en: make([]string, n)}

	for i := 0; i < n; i++ {
		orig := ""
		if i < len(origTexts) {
			orig = strings.TrimSpace(strings.ReplaceAll(origTexts[i], "\n", " "))
		}
		corr := strings.TrimSpace(corrected[i])
		if corr == "" {
			continue
		}
		if containsCJK(corr) {
			p.zh[i] = corr
			if looksForeignSubtitle(orig) {
				p.en[i] = orig
				p.enFixIdx = append(p.enFixIdx, i)
				p.enFixTxt = append(p.enFixTxt, orig)
			} else if orig != "" {
				p.z2eIdx = append(p.z2eIdx, i)
				p.z2eTxt = append(p.z2eTxt, corr)
			}
		} else {
			src := corr
			if src == "" {
				src = orig
			}
			if src != "" {
				p.en[i] = src
				p.e2zIdx = append(p.e2zIdx, i)
				p.e2zTxt = append(p.e2zTxt, src)
			}
		}
	}
	return p
}

func joinBilingual(zh, en string) string {
	zh = strings.TrimSpace(zh)
	en = strings.TrimSpace(strings.ReplaceAll(en, "\n", " "))
	switch {
	case zh != "" && en != "" && zh != en:
		return zh + "\n" + en
	case zh != "":
		return zh
	default:
		return en
	}
}

func asrBuildBilingualLines(apiKey string, origTexts, corrected []string) []string {
	plan := planASRBilingual(origTexts, corrected)
	n := len(corrected)
	zh, en := plan.zh, plan.en
	z2eIdx, z2eTxt := plan.z2eIdx, plan.z2eTxt
	e2zIdx, e2zTxt := plan.e2zIdx, plan.e2zTxt
	enFixIdx, enFixTxt := plan.enFixIdx, plan.enFixTxt

	var wg sync.WaitGroup

	if len(enFixTxt) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fixed, err := asrCorrectEnglish(apiKey, enFixTxt)
			if err != nil || len(fixed) != len(enFixTxt) {
				return
			}
			for k, idx := range enFixIdx {
				en[idx] = fixed[k]
			}
			cn, cErr := asrTranslateToChinese(apiKey, fixed, nil, nil)
			if cErr != nil || len(cn) != len(fixed) {
				return
			}
			for k, idx := range enFixIdx {
				if strings.TrimSpace(cn[k]) != "" {
					zh[idx] = cn[k]
				}
			}
		}()
	}
	if len(z2eTxt) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tr, err := asrTranslateToEnglish(apiKey, z2eTxt, nil, nil); err == nil && len(tr) == len(z2eTxt) {
				for k, idx := range z2eIdx {
					en[idx] = tr[k]
				}
			}
		}()
	}
	if len(e2zTxt) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tr, err := asrTranslateToChinese(apiKey, e2zTxt, nil, nil); err == nil && len(tr) == len(e2zTxt) {
				for k, idx := range e2zIdx {
					zh[idx] = tr[k]
				}
			}
		}()
	}
	wg.Wait()

	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = joinBilingual(zh[i], en[i])
	}
	return out
}

func asrParseKeyedCorrection(content string, n int) ([]string, error) {
	content = stripMarkdownFence(content)
	var m map[string]string
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return nil, fmt.Errorf("结果格式异常，正在重试")
	}
	if len(m) != n {
		return nil, fmt.Errorf("修正结果数量异常（期望 %d，实际 %d），正在重试", n, len(m))
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		v, ok := m[strconv.Itoa(i+1)]
		if !ok {
			return nil, fmt.Errorf("修正结果缺少第 %d 行，正在重试", i+1)
		}
		out[i] = strings.TrimSpace(v)
	}
	return out, nil
}
