package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

func findFilesRecursive(dir string, exts map[string]bool) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "screenshots" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !exts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		if isMuxerTempArtifact(name) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files
}

const (
	dedupSampleRate   = 48000
	dedupAudioBitrate = "256k"
)

const (
	wmDedupShowSec    = 6.0
	wmDedupGapMin     = 70.0
	wmDedupGapSpan    = 40.0
	wmDedupCharStep   = 0.12
	wmDedupFadeIn     = 0.40
	wmDedupFadeOut    = 0.70
	wmDedupMaxFilters = 3000
)

type DedupOptions struct {
	InputDir       string `json:"inputDir"`
	OutputDir      string `json:"outputDir"`
	Notify         bool   `json:"notify"`
	AutoOpenOutput bool   `json:"autoOpenOutput"`
}

type dedupParams struct {
	AudioPitch    float64
	Brightness    float64
	Saturation    float64
	Contrast      float64
	EdgeCrop      int
	Zoom          float64
	CropOffsetX   float64
	CropOffsetY   float64
	HeadTrimSec   float64
	FadeSec       float64
	NoiseStrength int
	GOP           int
	EQFreq        int
	EQGain        float64
}

func newDedupParams() *dedupParams {
	edgeOpts := []int{6, 8, 10, 12}
	return &dedupParams{
		AudioPitch:    1.025 + rand.Float64()*0.015,
		Brightness:    -0.015 + rand.Float64()*0.040,
		Saturation:    0.97 + rand.Float64()*0.09,
		Contrast:      0.97 + rand.Float64()*0.08,
		EdgeCrop:      edgeOpts[rand.Intn(len(edgeOpts))],
		Zoom:          1.010 + rand.Float64()*0.010,
		CropOffsetX:   rand.Float64(),
		CropOffsetY:   rand.Float64(),
		HeadTrimSec:   0.30 + rand.Float64()*0.25,
		FadeSec:       0.40 + rand.Float64()*0.20,
		NoiseStrength: 2 + rand.Intn(3),
		GOP:           48 + rand.Intn(25),
		EQFreq:        900 + rand.Intn(2601),
		EQGain:        -0.8 + rand.Float64()*1.6,
	}
}

func (a *App) dedupWMLayer(outDur float64, frameH int) string {
	raw := strings.TrimSpace(wmURL())
	if raw == "" || outDur < 60 {
		return ""
	}
	raw = stripControlRunes(raw)
	if raw == "" {
		return ""
	}

	fs := frameH / 24
	if fs < 28 {
		fs = 28
	}

	chars := []rune(raw)
	n := len(chars)
	step := wmDedupCharStep
	if maxType := wmDedupShowSec - wmDedupFadeOut - 1.0; maxType > 0 && float64(n)*step > maxType {
		step = maxType / float64(n)
	}
	typeDur := float64(n) * step

	winStart := outDur * 0.20
	winEnd := outDur * 0.88
	span := winEnd - winStart
	if span < wmDedupShowSec {
		return ""
	}

	gapMin, gapSpan := wmDedupGapMin, wmDedupGapSpan
	maxApp := wmDedupMaxFilters / n
	if maxApp < 1 {
		maxApp = 1
	}
	if span/(wmDedupShowSec+gapMin) > float64(maxApp) {
		want := span/float64(maxApp) - wmDedupShowSec
		if want < 1 {
			want = 1
		}
		gapMin, gapSpan = want, want*0.5
	}

	var parts []string
	appearances := 0
	for t := winStart; t+wmDedupShowSec <= winEnd; {
		st, ed := t, t+wmDedupShowSec
		appearances++
		fz, op, shOp, shx, shy := wmJitter(fs, 0.40, 0.40)
		holdEnd := ed - wmDedupFadeOut

		ax := 0.16 + rand.Float64()*0.14
		ay := 0.10 + rand.Float64()*0.16
		px := 7.0 + rand.Float64()*7.0
		py := 8.0 + rand.Float64()*8.0
		phx := rand.Float64() * 2 * math.Pi
		phy := rand.Float64() * 2 * math.Pi
		tType := st + typeDur
		xExpr := fmt.Sprintf("(w-tw)*0.5+(w-tw)*%.3f*sin(2*PI*max(t-%.3f\\,0)/%.2f+%.3f)", ax, tType, px, phx)
		yExpr := fmt.Sprintf("(h-th)*0.5+(h-th)*%.3f*cos(2*PI*max(t-%.3f\\,0)/%.2f+%.3f)", ay, tType, py, phy)

		envAlpha := dedupWMEnvelope(st, wmDedupFadeIn, holdEnd, ed, wmDedupFadeOut, op)
		for i := 1; i <= n; i++ {
			c0 := st + float64(i-1)*step
			c1 := st + float64(i)*step
			if i == n {
				c1 = ed
			}
			if c1 <= c0 {
				continue
			}
			sub := dtEscape(string(chars[:i]))
			parts = append(parts, fmt.Sprintf(
				"drawtext=text='%s':fontcolor=white:fontsize=%d:x='%s':y='%s':shadowcolor=black@%.2f:shadowx=%d:shadowy=%d:alpha='%s':enable='between(t\\,%.3f\\,%.3f)':expansion=none:fontfile='%s'",
				sub, fz, xExpr, yExpr, shOp, shx, shy, envAlpha, c0, c1, wmFontFor(raw)))
		}

		t = ed + gapMin + rand.Float64()*gapSpan
	}
	if len(parts) == 0 {
		return ""
	}
	a.log("info", fmt.Sprintf("中段水印 %d 次 · 打字机 %.1fs", appearances, typeDur))
	return strings.Join(parts, ",")
}

func dedupWMEnvelope(st, fadeIn, holdEnd, ed, fadeOut, op float64) string {
	return fmt.Sprintf(
		"if(lt(t\\,%.3f)\\,(t-%.3f)/%.2f*%.2f\\,"+
			"if(lt(t\\,%.3f)\\,%.2f\\,"+
			"if(lt(t\\,%.3f)\\,(%.3f-t)/%.2f*%.2f\\,0)))",
		st+fadeIn, st, fadeIn, op,
		holdEnd, op,
		ed, ed, fadeOut, op)
}

func stripControlRunes(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func (a *App) buildDedupPipeline(p *dedupParams, info *videoInfo, vertical, hasAudio bool) (filterComplex string, outDur float64) {
	headTrimmedDur := info.Duration - p.HeadTrimSec
	if headTrimmedDur < 1.0 {
		headTrimmedDur = info.Duration
		p.HeadTrimSec = 0
	}
	outDur = headTrimmedDur / p.AudioPitch
	if outDur < 2.0 {
		p.FadeSec = 0
	}
	fadeOutStart := outDur - p.FadeSec
	if fadeOutStart < p.FadeSec {
		fadeOutStart = outDur - 0.1
	}

	baseW, baseH := info.Width, info.Height
	if vertical {
		baseW, baseH = 1920, 1080
	}
	cropW := baseW - 2*p.EdgeCrop
	cropH := baseH - 2*p.EdgeCrop
	if cropW%2 != 0 {
		cropW--
	}
	if cropH%2 != 0 {
		cropH--
	}
	zoomW := int(float64(cropW) * p.Zoom)
	zoomH := int(float64(cropH) * p.Zoom)
	if zoomW%2 != 0 {
		zoomW++
	}
	if zoomH%2 != 0 {
		zoomH++
	}

	offX := int(float64(zoomW-cropW) * p.CropOffsetX)
	offY := int(float64(zoomH-cropH) * p.CropOffsetY)
	if offX < 0 {
		offX = 0
	}
	if offY < 0 {
		offY = 0
	}
	if offX%2 != 0 {
		offX--
	}
	if offY%2 != 0 {
		offY--
	}

	trimPrefix := ""
	if p.HeadTrimSec > 0 {
		trimPrefix = fmt.Sprintf("trim=start=%.3f,setpts=PTS-STARTPTS,", p.HeadTrimSec)
	}

	wmChain := a.dedupWMLayer(outDur, cropH)
	if wmChain != "" {
		wmChain += ","
	}

	coreChain := fmt.Sprintf(
		"crop=%d:%d,scale=%d:%d,crop=%d:%d:%d:%d,"+
			"eq=brightness=%.4f:saturation=%.4f:contrast=%.4f,"+
			"noise=c0s=%d:c0f=t,"+
			"setpts=PTS/%.4f,"+
			"fps=30000/1001,"+
			"fade=t=in:st=0:d=%.3f,"+
			"fade=t=out:st=%.3f:d=%.3f,"+
			"%s"+
			"setsar=1,format=yuv420p",
		cropW, cropH, zoomW, zoomH, cropW, cropH, offX, offY,
		p.Brightness, p.Saturation, p.Contrast,
		p.NoiseStrength,
		p.AudioPitch,
		p.FadeSec,
		fadeOutStart, p.FadeSec,
		wmChain,
	)

	if vertical {
		filterComplex = fmt.Sprintf(
			"[0:v]%ssplit=2[bg][fg];"+
				"[bg]scale=1920:1080:force_original_aspect_ratio=increase,crop=1920:1080,boxblur=30:5,setsar=1[bgblur];"+
				"[fg]scale=-2:1080,setsar=1[fgs];"+
				"[bgblur][fgs]overlay=(W-w)/2:0,%s[vout]", trimPrefix, coreChain)
	} else {
		filterComplex = fmt.Sprintf("[0:v]%s%s[vout]", trimPrefix, coreChain)
	}

	if hasAudio {
		atrimPrefix := ""
		if p.HeadTrimSec > 0 {
			atrimPrefix = fmt.Sprintf("atrim=start=%.3f,asetpts=PTS-STARTPTS,", p.HeadTrimSec)
		}
		noiseAmp := 0.03 + rand.Float64()*0.02
		filterComplex += fmt.Sprintf(
			";[0:a]%saformat=sample_rates=%d,atempo=%.4f,"+
				"equalizer=f=%d:width_type=o:width=1:g=%.2f,"+
				"afade=t=in:st=0:d=%.3f,afade=t=out:st=%.3f:d=%.3f[amain];"+
				"[1:a]aformat=sample_rates=%d,volume=%.4f[anoise];"+
				"[amain][anoise]amix=inputs=2:duration=first:normalize=0[aout]",
			atrimPrefix, dedupSampleRate, p.AudioPitch,
			p.EQFreq, p.EQGain,
			p.FadeSec, fadeOutStart, p.FadeSec,
			dedupSampleRate, noiseAmp,
		)
	}
	return filterComplex, outDur
}

func (a *App) buildDedupCmd(videoFile, outputPath, filterComplex, title string, gop int, hasAudio, useCuda bool, outDur float64) (*exec.Cmd, string) {
	tmpFile, err := os.CreateTemp("", "muxer-dedup-*.txt")
	tmpPath := ""
	useScript := false
	if err == nil {
		_, _ = tmpFile.WriteString(filterComplex)
		tmpFile.Close()
		tmpPath = tmpFile.Name()
		useScript = true
	}

	args := []string{"ffmpeg", "-y"}
	if useCuda {
		args = append(args, "-hwaccel", "cuda")
	}
	args = append(args, "-i", videoFile)
	if hasAudio {
		noiseSrc := fmt.Sprintf("anoisesrc=color=pink:duration=%.2f:seed=%d", outDur+2, rand.Intn(1<<31))
		args = append(args, "-f", "lavfi", "-i", noiseSrc)
	}
	if useScript {
		args = append(args, "-filter_complex_script", tmpPath)
	} else {
		args = append(args, "-filter_complex", filterComplex)
	}
	args = append(args,
		"-map", "[vout]",
		"-c:v", "h264_nvenc",
		"-preset", "p1",
		"-rc", "vbr",
		"-cq", "28",
		"-b:v", "0",
		"-maxrate", "12M",
		"-bufsize", "24M",
		"-g", fmt.Sprintf("%d", gop),
		"-pix_fmt", "yuv420p",
	)
	if hasAudio {
		args = append(args,
			"-map", "[aout]",
			"-c:a", "aac",
			"-b:a", dedupAudioBitrate,
			"-ar", fmt.Sprintf("%d", dedupSampleRate),
		)
	}
	args = append(args,
		"-map_chapters", "-1",
		"-map_metadata", "-1",
		"-metadata", "title="+title,
	)
	if wmHasBranding() {
		if artist := wmArtist(); artist != "" {
			args = append(args, "-metadata", "artist="+artist, "-metadata", "copyright="+artist)
		}
		if u := wmURL(); u != "" {
			args = append(args, "-metadata", "comment="+u)
		}
		if c := wmComment(); c != "" {
			args = append(args, "-metadata", "description="+c)
		}
	}
	args = append(args, mp4FaststartArgs(outputPath)...)
	args = append(args, outputPath)
	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	return cmd, tmpPath
}

func (a *App) processDedupOne(videoFile string, opts DedupOptions) bool {
	a.log("info", "视频: "+filepath.Base(videoFile))

	info, err := getVideoInfo(videoFile)
	if err != nil || info == nil {
		a.log("error", "无法读取视频信息")
		return false
	}
	if info.Duration <= 0 {
		a.log("error", "视频时长无效，跳过")
		return false
	}

	outExt := ".mp4"

	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	cleaned := wmCleanFilename(stem)

	subDir := cleaned
	subDirPath := filepath.Join(opts.OutputDir, subDir)
	if _, err := os.Stat(subDirPath); err == nil {
		for n := 2; n <= 999; n++ {
			candidate := filepath.Join(opts.OutputDir, fmt.Sprintf("%s(%d)", cleaned, n))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				subDir = fmt.Sprintf("%s(%d)", cleaned, n)
				subDirPath = candidate
				break
			}
		}
	}
	os.MkdirAll(subDirPath, 0755)
	outputPath := filepath.Join(subDirPath, subDir+outExt)

	a.log("info", "输出: "+filepath.Base(outputPath))

	hasAudio := len(info.AudioCodecs) > 0
	var asrText string
	if hasAudio && !a.stopped.Load() {
		asrText = a.dedupASRText(videoFile, 180)
	}

	if a.stopped.Load() {
		return false
	}

	vertical := info.Height > info.Width && info.Width > 0
	if vertical {
		a.log("info", fmt.Sprintf("竖屏视频 %dx%d → 模糊填充转 1920x1080", info.Width, info.Height))
	}

	params := newDedupParams()
	filterComplex, outDur := a.buildDedupPipeline(params, info, vertical, hasAudio)

	keepSource := false
	a.log("info", fmt.Sprintf("参数: pitch=%.3f 边裁=%dpx 缩放=%.3f 取景=%.2f,%.2f 亮=%+.3f 饱=%.3f 比=%.3f 头裁=%.2fs 淡=%.2fs 噪=%d GOP=%d EQ=%dHz/%+.2fdB",
		params.AudioPitch, params.EdgeCrop, params.Zoom,
		params.CropOffsetX, params.CropOffsetY,
		params.Brightness, params.Saturation, params.Contrast,
		params.HeadTrimSec, params.FadeSec,
		params.NoiseStrength, params.GOP, params.EQFreq, params.EQGain))
	a.log("info", fmt.Sprintf("源时长 %.2fs → 输出 %.2fs", info.Duration, outDur))

	if !hasAudio {
		a.log("warn", "源视频无音频流，跳过音频处理")
	}

	useCuda := !vertical
	cmd, tmpPath := a.buildDedupCmd(videoFile, outputPath, filterComplex, cleaned, params.GOP, hasAudio, useCuda, outDur)
	if tmpPath != "" {
		defer os.Remove(tmpPath)
	}
	fullCmdLine := strings.Join(cmd.Args, " ")

	start := time.Now()
	errMsg, err := a.runWithProgress(cmd, outDur)
	elapsed := time.Since(start).Seconds()

	if err != nil && elapsed < 3 && useCuda && !a.stopped.Load() {
		a.log("warn", "CUDA 解码失败，回退软解码")
		useCuda = false
		cmd, tmpPath2 := a.buildDedupCmd(videoFile, outputPath, filterComplex, cleaned, params.GOP, hasAudio, useCuda, outDur)
		if tmpPath2 != "" {
			defer os.Remove(tmpPath2)
		}
		fullCmdLine = strings.Join(cmd.Args, " ")
		start = time.Now()
		errMsg, err = a.runWithProgress(cmd, outDur)
		elapsed = time.Since(start).Seconds()
	}

	if err != nil {
		if a.stopped.Load() {
			a.emitHistory(filepath.Base(videoFile), "去重/MP4", friendlyDuration(elapsed), false)
			return false
		}
		if ok2, reason := isAcceptableTailExit(outputPath, outDur, errMsg); ok2 {
			a.log("warn", "FFmpeg 收尾异常，输出有效: "+reason)
			if fixed, ok := a.repairUnfinalizedOutput(outputPath); ok {
				outputPath = fixed
				a.log("info", "重封装修复成品索引")
			} else {
				keepSource = true
				a.log("warn", "重封装修复失败，保留源文件")
			}
		} else {
			logPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".error.log"
			a.writeFFmpegLog(logPath, fullCmdLine, errMsg, err.Error(), elapsed, true)
			a.log("error", fmt.Sprintf("失败 (%s)\n%s", friendlyDuration(elapsed), friendlyErrorMsg(errMsg, "nvenc", true)))
			a.emitHistory(filepath.Base(videoFile), "去重/MP4", friendlyDuration(elapsed), false)
			return false
		}
	}

	a.log("ok", "去重完成 "+friendlyDuration(elapsed))
	a.emitHistory(filepath.Base(videoFile), "去重/MP4", friendlyDuration(elapsed), true)

	if !a.stopped.Load() {
		a.dedupGenerateYouTubeTxt(cleaned, asrText, outputPath)
	}

	if keepSource {
		a.log("warn", "输出未正常收尾，保留源文件: "+filepath.Base(videoFile))
	} else if pathWithinDir(filepath.Dir(videoFile), opts.OutputDir) {
		a.log("info", "保留源文件 "+filepath.Base(videoFile))
	} else if st, serr := os.Stat(outputPath); serr != nil || st.Size() < 1<<20 {
		a.log("warn", "输出校验异常，保留源文件: "+filepath.Base(videoFile))
	} else {
		if err := os.Remove(videoFile); err != nil {
			a.log("warn", "删除源文件失败: "+err.Error())
		} else {
			a.log("info", "删除源文件 "+filepath.Base(videoFile))
		}
	}
	return true
}

func (a *App) dedupASRText(videoFile string, maxDurSec int) string {
	tempAudio := filepath.Join(filepath.Dir(videoFile), fmt.Sprintf("muxer_dedup_asr_%d.mp3", time.Now().UnixNano()))
	defer os.Remove(tempAudio)

	ffArgs := []string{
		"ffmpeg", "-i", videoFile,
		"-vn", "-ac", "1", "-ar", "16000", "-b:a", "128k",
	}
	if maxDurSec > 0 {
		ffArgs = append(ffArgs, "-t", fmt.Sprintf("%d", maxDurSec))
	}
	ffArgs = append(ffArgs, "-y", tempAudio)
	cmd := exec.Command(ffArgs[0], ffArgs[1:]...)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		a.log("warn", "ASR 提取音频失败: "+strings.TrimSpace(string(out)))
		return ""
	}
	audioInfo, err := os.Stat(tempAudio)
	if err != nil || audioInfo.Size() == 0 {
		a.log("warn", "ASR 提取的音频为空，跳过")
		return ""
	}

	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	safeKey := reNonASCIIKey.ReplaceAllString(stem, "_")
	if len(safeKey) > 80 {
		safeKey = safeKey[:80]
	}
	objectKey := fmt.Sprintf("temp/%d_%s.mp3", time.Now().UnixNano(), safeKey)
	if err := ossUploadFile(tempAudio, objectKey); err != nil {
		a.log("warn", "ASR 上传 OSS 失败: "+err.Error())
		return ""
	}
	defer func() {
		if derr := ossDeleteObject(objectKey); derr != nil {
			a.log("warn", fmt.Sprintf("OSS 临时音频清理失败 %s: %v", objectKey, derr))
		}
	}()

	signedURL := ossSignedURL(objectKey, 7200)
	a.log("info", "ASR 识别")

	asr, err := a.dashscopeASRTranscribe(dashscopeASRRequest{
		FileURL: signedURL,
		MaxWait: 5 * time.Minute,
	})
	if err != nil {
		switch {
		case isCancelled(err):
		case errors.Is(err, errASRNoSpeech):
			a.log("warn", "未检测到语音，发布信息仅按标题生成")
		default:
			a.log("warn", "ASR "+err.Error())
		}
		return ""
	}

	trResp, err := http.Get(asr.TranscriptionURL)
	if err != nil {
		a.log("warn", "ASR 下载结果失败")
		return ""
	}
	defer trResp.Body.Close()
	var transcription struct {
		Transcripts []struct {
			Sentences []struct {
				Text string `json:"text"`
			} `json:"sentences"`
		} `json:"transcripts"`
	}
	if err := json.NewDecoder(trResp.Body).Decode(&transcription); err != nil {
		a.log("warn", "ASR 解析结果失败")
		return ""
	}

	var sb strings.Builder
	for _, tr := range transcription.Transcripts {
		for _, s := range tr.Sentences {
			t := strings.TrimSpace(s.Text)
			if t != "" {
				sb.WriteString(t)
			}
		}
	}
	text := sb.String()
	if text == "" {
		a.log("warn", "ASR 无结果")
	} else {
		r := []rune(text)
		if len(r) > 2000 {
			text = string(r[:2000])
		}
		a.log("info", fmt.Sprintf("ASR %d 字", len([]rune(text))))
	}
	return text
}

func (a *App) dedupGenerateYouTubeTxt(title, asrText, outputPath string) {
	txtPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".txt"

	channelLine := ""
	if b := wmBrand(); b != "" {
		channelLine = "频道名：" + b + "。"
	}
	systemPrompt := "你是" + brandActor() + "的专业 YouTube 视频 SEO 优化专家。你的任务是根据视频内容生成高质量的简体中文标题和描述。" +
		"视频类型：海外夜生活/娱乐场所实地测评（SPA、浴室、GoGoBar、酒吧、夜店等）。" +
		channelLine +
		"标题规则：" +
		"1. 15-30个中文字符（40-65字节），不超过30字。" +
		"2. 前5个字必须包含地点或店名关键词（如曼谷、芭提雅、马尼拉等）。" +
		"3. 制造好奇心和点击欲望，像跟朋友分享真实体验。" +
		"4. 不要用emoji，不要用hashtag，不要用引号。" +
		"5. 不要出现'测评''推荐''攻略'等过度营销词。" +
		"描述规则：" +
		"1. 150-250个中文字符，纯文本，分2-3个自然段落，段落之间用单个\\n换行（不要空行）。" +
		"2. 第一句话必须包含地点+场所类型关键词（SEO核心，这句话在YouTube搜索结果中直接展示）。" +
		"3. 用口语化的第一人称写，像朋友之间分享经历，不要书面化。" +
		"4. 正文自然嵌入2-3个搜索关键词（地名、场所类型、体验描述）。" +
		"5. 最后一句自然收尾即可，不要用固定套路（禁止出现'评论区''你们去过吗''有没有去过''聊聊'等模板化用语）。" +
		"6. 不要出现具体地址、价格数字、hashtag、emoji。" +
		"7. 不要编造视频中没有提到的细节，宁可写得笼统也不要胡编。" +
		"返回格式：严格返回 JSON 对象 {\"t\":[\"标题\",\"描述\"]}，描述中的换行用\\n表示，不要有任何解释或markdown。"

	var userContent string
	if asrText != "" {
		userContent = fmt.Sprintf("视频标题：%s\n\n视频语音内容：\n%s", title, asrText)
	} else {
		userContent = fmt.Sprintf("视频标题：%s\n\n（视频无语音内容，请仅根据标题生成）", title)
	}

	if strings.TrimSpace(chatAPIKey()) == "" {
		a.log("warn", "未配置 AI Key，跳过发布信息")
		return
	}
	a.log("info", aiProviderLabel()+" 生成发布信息")
	var result []string
	for attempt := 1; attempt <= 3; attempt++ {
		if a.stopped.Load() {
			return
		}
		r, err := callChatTextList(chatAPIKey(), systemPrompt, userContent, 0.9, 2048)
		if err == nil && len(r) >= 2 {
			result = r
			break
		}
		if attempt < 3 {
			a.log("warn", fmt.Sprintf("%s 生成失败第 %d 次，2s 后重试", aiProviderLabel(), attempt))
			time.Sleep(2 * time.Second)
		} else {
			a.log("warn", "生成失败，写入原标题")
			fallback := title + "\n"
			os.WriteFile(txtPath, []byte(fallback), 0644)
			return
		}
	}

	ytTitle := strings.TrimSpace(result[0])
	ytDesc := strings.TrimSpace(result[1])

	content := ytTitle + "\n" + ytDesc + "\n"
	if err := os.WriteFile(txtPath, []byte(content), 0644); err != nil {
		a.log("warn", "写入 TXT 失败: "+err.Error())
		return
	}
	a.log("ok", chatProviderUsed()+" 发布信息 → "+filepath.Base(txtPath))
}

func (a *App) ProcessDedup(opts DedupOptions) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	a.log("info", "扫描视频文件")

	if !a.CheckFFmpeg() {
		a.log("error", "FFmpeg 不可用")
		return false
	}
	if !a.checkNvencAvailable() {
		a.log("error", "h264_nvenc 不可用")
		return false
	}
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败: "+err.Error())
		return false
	}

	videos := findFilesRecursive(opts.InputDir, videoExts)
	if len(videos) == 0 {
		a.log("error", "未找到视频文件")
		return false
	}
	a.log("info", fmt.Sprintf("去重 %d 个视频 · NVENC h264", len(videos)))
	a.PlaySoundStartProcess()

	var ok atomic.Int32
	for i, v := range videos {
		if a.stopped.Load() {
			a.log("warn", "已中止")
			break
		}
		if len(videos) > 1 {
			a.log("info", fmt.Sprintf("── [%d/%d] %s", i+1, len(videos), filepath.Base(v)))
		}
		if a.processDedupOne(v, opts) {
			ok.Add(1)
		}
	}

	successCnt := int(ok.Load())
	if a.stopped.Load() {
		return successCnt > 0
	}

	msg := fmt.Sprintf("完成 %d/%d", successCnt, len(videos))
	if successCnt == len(videos) {
		a.log("ok", msg)
	} else {
		a.log("warn", msg)
	}

	if successCnt > 0 {
		a.PlaySoundMuxDone()
	}
	if opts.Notify {
		a.sendNotification(msg)
	}
	if opts.AutoOpenOutput && successCnt > 0 {
		exec.Command("explorer", opts.OutputDir).Start()
	}
	return successCnt > 0
}
