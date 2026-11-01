package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var videoAudioExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".flv": true, ".webm": true, ".ts": true, ".m2ts": true, ".mpg": true,
	".mpeg": true, ".3gp": true, ".m4v": true, ".vob": true, ".rmvb": true,
	".mp3": true, ".wav": true, ".flac": true, ".aac": true, ".ogg": true,
	".wma": true, ".m4a": true, ".opus": true,
}

func (a *App) QwenASRTranscribe(inputPath, outputDir, language, dashKey, ossID, ossSecret, ossBucketArg, ossEndpointArg string, aiCorrect bool) bool {
	qwenOverrideKey = dashKey
	qwenOverrideOSSID = ossID
	qwenOverrideOSSSecret = ossSecret
	qwenOverrideBucket = ossBucketArg
	qwenOverrideEndpoint = ossEndpointArg

	if !a.beginJob() {
		return false
	}
	defer a.endJob()
	if inputPath == "" {
		a.log("error", "未选择文件")
		return false
	}
	if outputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	if dashscopeKey() == "" || ossAccessKeyID() == "" || ossAccessKeySecret() == "" {
		a.log("error", "未配置千问 ASR 密钥")
		return false
	}

	info, err := os.Stat(inputPath)
	if err != nil {
		a.log("error", "无法访问路径: "+inputPath)
		return false
	}

	var files []string
	if info.IsDir() {
		files = findFilesRecursive(inputPath, videoAudioExts)
		sort.Strings(files)
		if len(files) == 0 {
			a.log("error", "目录中未找到视频/音频文件")
			return false
		}
		files = dedupBasenames(files, func(kept, dropped string) {
			a.log("warn", "同名跳过: "+filepath.Base(dropped))
		})
		a.log("info", fmt.Sprintf("批量 %d 个文件", len(files)))
	} else {
		files = []string{inputPath}
	}

	allOK := true
	for i, videoFile := range files {
		if a.stopped.Load() {
			a.log("warn", "已取消")
			return false
		}
		if len(files) > 1 {
			a.log("info", fmt.Sprintf("── [%d/%d] %s ──", i+1, len(files), filepath.Base(videoFile)))
		}
		if !a.qwenASRSingle(videoFile, outputDir, language, aiCorrect) {
			allOK = false
		}
	}
	if len(files) > 1 {
		a.log("info", fmt.Sprintf("批量转字幕完成，共 %d 个文件", len(files)))
	}
	return allOK
}

func (a *App) qwenASRSingle(videoFile, outputDir, language string, aiCorrect bool) bool {
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	outFile := filepath.Join(outputDir, stem+".srt")

	a.log("info", fmt.Sprintf("转字幕 %s", filepath.Base(videoFile)))
	a.log("info", "提取音频")

	tempAudio := filepath.Join(filepath.Dir(videoFile), fmt.Sprintf("muxer_asr_%d.mp3", time.Now().UnixNano()))
	defer os.Remove(tempAudio)

	ffArgs := []string{
		"ffmpeg", "-i", videoFile,
		"-vn", "-ac", "1", "-ar", "16000", "-b:a", "128k",
		"-y", tempAudio,
	}
	cmd := exec.Command(ffArgs[0], ffArgs[1:]...)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		a.log("error", "提取音频失败: "+strings.TrimSpace(string(out)))
		return false
	}

	audioInfo, err := os.Stat(tempAudio)
	if err != nil || audioInfo.Size() == 0 {
		a.log("error", "提取的音频文件为空")
		return false
	}
	a.log("info", fmt.Sprintf("音频 %.1f MB，上传 OSS", float64(audioInfo.Size())/1024/1024))

	safeKey := reNonASCIIKey.ReplaceAllString(stem, "_")
	if len(safeKey) > 80 {
		safeKey = safeKey[:80]
	}
	objectKey := fmt.Sprintf("temp/%d_%s.mp3", time.Now().UnixNano(), safeKey)
	if err := ossUploadFile(tempAudio, objectKey); err != nil {
		a.log("error", "上传 OSS 失败: "+err.Error())
		return false
	}
	defer func() {
		if derr := ossDeleteObject(objectKey); derr != nil {
			a.log("warn", fmt.Sprintf("云端临时音频删除失败: %v", derr))
		}
	}()

	signedURL := ossSignedURL(objectKey, 7200)
	a.log("info", "提交识别")

	asr, err := a.dashscopeASRTranscribe(dashscopeASRRequest{
		FileURL:  signedURL,
		Language: language,
		MaxWait:  20 * time.Minute,
		OnSubmitted: func(taskID string) {
			a.log("info", fmt.Sprintf("任务 %s", shortTaskID(taskID)))
		},
		OnWaiting: func(elapsed time.Duration) {
			a.log("info", fmt.Sprintf("识别中 %d 秒", int(elapsed.Seconds())))
		},
	})
	if err != nil {
		switch {
		case isCancelled(err):
			a.log("warn", "识别已取消")
		case errors.Is(err, errASRNoSpeech):
			a.log("warn", "未检测到语音，不产出字幕")
		case errors.Is(err, errASRTimeout):
			a.log("error", "识别超时")
		default:
			a.log("error", err.Error())
		}
		return false
	}
	transcriptionURL := asr.TranscriptionURL
	if asr.AudioSeconds > 0 {
		cost := float64(asr.AudioSeconds) * 0.00022
		a.log("info", fmt.Sprintf("识别完成 %s · ¥%.2f",
			describeAudioDuration(asr.AudioSeconds), cost))
	} else {
		a.log("info", "识别完成")
	}

	a.log("info", "下载转写结果")
	trResp, err := http.Get(transcriptionURL)
	if err != nil {
		a.log("error", "下载转写结果失败: "+err.Error())
		return false
	}
	defer trResp.Body.Close()

	var transcription struct {
		Transcripts []struct {
			Sentences []struct {
				SentenceID int    `json:"sentence_id"`
				BeginTime  int    `json:"begin_time"`
				EndTime    int    `json:"end_time"`
				Text       string `json:"text"`
				Words      []struct {
					BeginTime   int    `json:"begin_time"`
					EndTime     int    `json:"end_time"`
					Text        string `json:"text"`
					Punctuation string `json:"punctuation"`
				} `json:"words"`
			} `json:"sentences"`
		} `json:"transcripts"`
	}
	if err := json.NewDecoder(trResp.Body).Decode(&transcription); err != nil {
		a.log("error", "解析转写结果失败: "+err.Error())
		return false
	}

	if len(transcription.Transcripts) == 0 || len(transcription.Transcripts[0].Sentences) == 0 {
		a.log("error", "转写结果为空")
		return false
	}

	var segments []asrSubtitleSeg
	for _, tr := range transcription.Transcripts {
		for _, s := range tr.Sentences {
			text := strings.TrimSpace(s.Text)
			if text == "" {
				continue
			}
			words := make([]asrWordTime, 0, len(s.Words))
			for _, w := range s.Words {
				if strings.TrimSpace(w.Text+w.Punctuation) == "" {
					continue
				}
				wordEnd := w.EndTime
				if wordEnd <= w.BeginTime {
					if s.EndTime > w.BeginTime {
						wordEnd = s.EndTime
					} else {
						wordEnd = w.BeginTime + 1
					}
				}
				words = append(words, asrWordTime{
					Text:  w.Text,
					Punc:  w.Punctuation,
					Start: w.BeginTime,
					End:   wordEnd,
				})
			}
			segments = append(segments, asrSplitByWords(s.BeginTime, s.EndTime, text, words)...)
		}
	}
	count, err := writeASRSRT(outFile, segments)
	if err != nil {
		a.log("error", "创建字幕文件失败: "+err.Error())
		return false
	}
	a.log("ok", fmt.Sprintf("字幕 %d 条 → %s", count, filepath.Base(outFile)))

	if aiCorrect {
		a.asrPostCorrect(outFile)
	} else {
		a.log("info", "跳过 AI 修正")
	}

	return true
}
