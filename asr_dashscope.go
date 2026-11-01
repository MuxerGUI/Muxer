package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	dashscopeASRModel         = "qwen3-asr-flash-filetrans"
	dashscopeASRProgressEvery = 15
)

var (
	dashscopeASRSubmitURL    = "https://dashscope.aliyuncs.com/api/v1/services/audio/asr/transcription"
	dashscopeASRTaskURL      = "https://dashscope.aliyuncs.com/api/v1/tasks/"
	dashscopeASRPollInterval = 2 * time.Second
)

var errASRTimeout = errors.New("识别超时")

var errASRNoSpeech = errors.New("未检测到有效语音")

var asrStatusHints = map[string]string{
	"SUCCESS_WITH_NO_VALID_FRAGMENT": "未检测到有效语音（VAD 判定音频里没有人说话；纯音乐、环境音、静音都会这样）",
	"21050003":                       "未检测到有效语音（VAD 判定音频里没有人说话；纯音乐、环境音、静音都会这样）",
	"ASR_RESPONSE_HAVE_NO_WORDS":     "识别结果为空（检测到声音但没有可转写的词，或语气词被顺滑参数过滤）",
	"SUCCESS_WITH_NO_WORDS":          "识别结果为空（检测到声音但没有可转写的词）",
	"INVALIDAPIKEY":                  "DashScope API Key 无效 — 请检查 keys.json 的 dashscopeKey",
	"ARREARAGE":                      "千问 ASR 账户欠费 — 请到阿里云百炼控制台充值后重试",
	"THROTTLING":                     "千问 ASR 请求过于频繁 — 请稍后再试",
	"USER_BIZDURATION_QUOTA_EXCEED":  "千问 ASR 单日时长配额已用尽（免费额度每日 2 小时）— 请充值或次日再试",
	"41050001":                       "千问 ASR 单日时长配额已用尽（免费额度每日 2 小时）— 请充值或次日再试",
	"FILE_DOWNLOAD_FAILED":           "音频文件下载失败 — 服务端取不到 OSS 上的临时音频，请检查 Bucket 读权限与签名 URL 是否过期",
	"41050002":                       "音频文件下载失败 — 服务端取不到 OSS 上的临时音频，请检查 Bucket 读权限与签名 URL 是否过期",
	"FILE_CHECK_FAILED":              "音频格式不被接受 — 需要单/双声道 WAV 或 MP3",
	"41050003":                       "音频格式不被接受 — 需要单/双声道 WAV 或 MP3",
	"FILE_TOO_LARGE":                 "音频文件过大（超过 512MB）— 请先切分再识别",
	"41050004":                       "音频文件过大（超过 512MB）— 请先切分再识别",
	"FILE_NORMALIZE_FAILED":          "音频归一化失败 — 文件可能已损坏",
	"41050005":                       "音频归一化失败 — 文件可能已损坏",
	"FILE_PARSE_FAILED":              "音频解析失败 — 文件可能已损坏",
	"41050006":                       "音频解析失败 — 文件可能已损坏",
	"MKV_PARSE_FAILED":               "MKV 解析失败 — 文件可能已损坏",
	"41050007":                       "MKV 解析失败 — 文件可能已损坏",
	"UNSUPPORTED_SAMPLE_RATE":        "采样率不匹配 — 需要 16kHz 单声道音频",
	"41050008":                       "采样率不匹配 — 需要 16kHz 单声道音频",
	"FILE_TRANS_TASK_EXPIRED":        "识别任务已过期 — 请重新提交",
	"41050010":                       "识别任务已过期 — 请重新提交",
	"RAM_CHECK_FAILED":               "阿里云 RAM 权限不足 — 该账号未授权调用语音识别服务",
	"41050021":                       "阿里云 RAM 权限不足 — 该账号未授权调用语音识别服务",
	"AUDIO_DURATION_TOO_LONG":        "音频时长超过 12 小时上限 — 请切分后再识别",
	"41050103":                       "音频时长超过 12 小时上限 — 请切分后再识别",
	"DECODER_ERROR":                  "音频解码失败 — 链接里的文件不是受支持的音频格式",
	"40270003":                       "音频解码失败 — 链接里的文件不是受支持的音频格式",
	"INTERNAL_ERROR":                 "千问 ASR 服务端内部错误（偶发）— 重试通常可恢复",
	"51050000":                       "千问 ASR 服务端内部错误（偶发）— 重试通常可恢复",
}

func asrStatusText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if zh, ok := asrStatusHints[strings.ToUpper(s)]; ok {
		return zh
	}
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\r', '\n', ':', ',', ';', '，', '：':
			return true
		}
		return false
	}) {
		if zh, ok := asrStatusHints[strings.ToUpper(tok)]; ok {
			return zh
		}
	}
	return s
}

func asrStatusNoSpeech(s string) bool {
	upper := strings.ToUpper(s)
	for _, key := range []string{
		"SUCCESS_WITH_NO_VALID_FRAGMENT",
		"ASR_RESPONSE_HAVE_NO_WORDS",
		"SUCCESS_WITH_NO_WORDS",
		"21050003",
	} {
		if strings.Contains(upper, key) {
			return true
		}
	}
	return false
}

func dashscopeFailureText(code, message string) string {
	for _, raw := range []string{code, message} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if zh := asrStatusText(raw); zh != raw {
			return zh
		}
	}
	if m := strings.TrimSpace(message); m != "" {
		return m
	}
	if c := strings.TrimSpace(code); c != "" {
		return c
	}
	return "未知错误"
}

type dashscopeASRRequest struct {
	FileURL     string
	Language    string
	MaxWait     time.Duration
	OnSubmitted func(taskID string)
	OnWaiting   func(elapsed time.Duration)
}

type dashscopeASRResult struct {
	TranscriptionURL string
	AudioSeconds     int
}

func (a *App) dashscopeASRTranscribe(req dashscopeASRRequest) (dashscopeASRResult, error) {
	if a.stopped.Load() {
		return dashscopeASRResult{}, errCancelled
	}
	apiKey := dashscopeKey()
	taskID, err := a.dashscopeASRSubmit(apiKey, req)
	if err != nil {
		return dashscopeASRResult{}, err
	}
	if req.OnSubmitted != nil {
		req.OnSubmitted(taskID)
	}
	return a.dashscopeASRPoll(apiKey, taskID, req)
}

func (a *App) dashscopeASRSubmit(apiKey string, req dashscopeASRRequest) (string, error) {
	type params struct {
		ChannelID []int  `json:"channel_id"`
		Language  string `json:"language,omitempty"`
		EnableITN bool   `json:"enable_itn"`
	}
	body := struct {
		Model string `json:"model"`
		Input struct {
			FileURL string `json:"file_url"`
		} `json:"input"`
		Params params `json:"parameters"`
	}{
		Model:  dashscopeASRModel,
		Params: params{ChannelID: []int{0}, EnableITN: true},
	}
	body.Input.FileURL = req.FileURL
	if req.Language != "" && req.Language != "auto" {
		body.Params.Language = req.Language
	}

	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequest("POST", dashscopeASRSubmitURL, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	dashscopeSetHeaders(httpReq, apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("提交任务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return "", errors.New(dashscopeFriendlyError(resp.StatusCode, string(raw)))
	}

	var result struct {
		Output struct {
			TaskID  string `json:"task_id"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析提交结果失败: %w", err)
	}
	if result.Output.TaskID == "" {
		return "", errors.New("提交任务失败: " + dashscopeFailureText(result.Output.Code, result.Output.Message))
	}
	return result.Output.TaskID, nil
}

func dashscopeFriendlyError(statusCode int, body string) string {
	lower := strings.ToLower(body)
	hint := ""
	switch {
	case statusCode == 401 || strings.Contains(lower, "invalidapikey"):
		hint = "千问 ASR 认证失败 — DashScope API Key 无效"
	case strings.Contains(lower, "arrearage") || strings.Contains(lower, "欠费"):
		hint = "千问 ASR 账户欠费 — 请到阿里云百炼控制台充值后重试"
	case strings.Contains(lower, "quota") || strings.Contains(lower, "limit exceeded"):
		hint = "千问 ASR 配额已用尽 — 请到阿里云百炼控制台查看用量与额度"
	case statusCode == 403:
		hint = "千问 ASR 无访问权限 — 请确认已在阿里云百炼开通语音识别服务，且该 Key 有调用权限"
	case statusCode == 429:
		hint = "千问 ASR 请求频率超限 — 请稍后再试"
	case statusCode >= 500:
		hint = "千问 ASR 服务端故障 — 请稍后重试"
	default:
		hint = "千问 ASR 请求失败"
	}
	msg := strings.TrimSpace(body)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Sprintf("%s（HTTP %d: %s）", hint, statusCode, msg)
}

func (a *App) dashscopeASRPoll(apiKey, taskID string, req dashscopeASRRequest) (dashscopeASRResult, error) {
	queryURL := dashscopeASRTaskURL + taskID
	client := &http.Client{Timeout: 30 * time.Second}
	steps := int(req.MaxWait / dashscopeASRPollInterval)
	if steps < 1 {
		steps = 1
	}

	for i := 0; i < steps; i++ {
		time.Sleep(dashscopeASRPollInterval)
		if a.stopped.Load() {
			return dashscopeASRResult{}, errCancelled
		}

		qReq, _ := http.NewRequest("GET", queryURL, nil)
		dashscopeSetHeaders(qReq, apiKey)
		qResp, err := client.Do(qReq)
		if err != nil {
			continue
		}
		var qr struct {
			Output struct {
				TaskStatus string `json:"task_status"`
				Result     struct {
					TranscriptionURL string `json:"transcription_url"`
				} `json:"result"`
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"output"`
			Usage struct {
				Seconds int `json:"seconds"`
			} `json:"usage"`
		}
		json.NewDecoder(qResp.Body).Decode(&qr)
		qResp.Body.Close()

		if req.OnWaiting != nil && (i+1)%dashscopeASRProgressEvery == 0 {
			req.OnWaiting(time.Duration(i+1) * dashscopeASRPollInterval)
		}

		switch strings.ToUpper(qr.Output.TaskStatus) {
		case "SUCCEEDED":
			if qr.Output.Result.TranscriptionURL == "" {
				return dashscopeASRResult{}, errors.New("任务成功但未返回结果地址")
			}
			return dashscopeASRResult{
				TranscriptionURL: qr.Output.Result.TranscriptionURL,
				AudioSeconds:     qr.Usage.Seconds,
			}, nil
		case "FAILED":
			if asrStatusNoSpeech(qr.Output.Code) || asrStatusNoSpeech(qr.Output.Message) {
				return dashscopeASRResult{}, errASRNoSpeech
			}
			return dashscopeASRResult{}, errors.New("识别失败: " +
				dashscopeFailureText(qr.Output.Code, qr.Output.Message))
		default:
			if asrStatusNoSpeech(qr.Output.TaskStatus) {
				return dashscopeASRResult{}, errASRNoSpeech
			}
		}
	}
	return dashscopeASRResult{}, errASRTimeout
}

func dashscopeSetHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-DashScope-Async", "enable")
}

func shortTaskID(id string) string {
	if len(id) > 8 {
		return id[:8] + "…"
	}
	return id
}

func describeAudioDuration(sec int) string {
	switch {
	case sec >= 3600:
		return fmt.Sprintf("%d 小时 %d 分钟", sec/3600, (sec%3600)/60)
	case sec >= 60:
		return fmt.Sprintf("%d 分 %d 秒", sec/60, sec%60)
	default:
		return fmt.Sprintf("%d 秒", sec)
	}
}
