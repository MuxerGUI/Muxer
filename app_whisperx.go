package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const whisperxModel = "large-v3"

func resolveWhisperXPython(pythonPath string) string {
	pythonPath = strings.TrimSpace(pythonPath)
	if pythonPath != "" {
		return pythonPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	def := filepath.Join(home, "miniconda3", "envs", "whisperx", "python.exe")
	if st, err := os.Stat(def); err == nil && !st.IsDir() {
		return def
	}
	return ""
}

func whisperxEnv(httpProxy, modelDir string) []string {
	_ = httpProxy
	src := os.Environ()
	filtered := make([]string, 0, len(src)+6)
	for _, e := range src {
		up := strings.ToUpper(e)
		if strings.HasPrefix(up, "HF_ENDPOINT=") || strings.HasPrefix(up, "HUGGINGFACE_HUB_ENDPOINT=") {
			continue
		}
		if strings.HasPrefix(up, "HTTP_PROXY=") || strings.HasPrefix(up, "HTTPS_PROXY=") || strings.HasPrefix(up, "ALL_PROXY=") {
			continue
		}
		filtered = append(filtered, e)
	}
	filtered = append(filtered, "PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True")
	filtered = append(filtered, "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	filtered = append(filtered, "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1")
	if base := whisperxModelCacheDir(modelDir); base != "" {
		if !envHasKey(filtered, "HF_HOME") {
			filtered = append(filtered, "HF_HOME="+filepath.Join(base, "huggingface"))
		}
		if !envHasKey(filtered, "TORCH_HOME") {
			filtered = append(filtered, "TORCH_HOME="+filepath.Join(base, "torch"))
		}
	}
	return filtered
}

func whisperxModelCacheDir(uiDir string) string {
	if d := strings.TrimSpace(uiDir); d != "" {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	if d := strings.TrimSpace(keys().ModelDir); d != "" {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	base := `E:\MuxerModels`
	if st, err := os.Stat(`E:\`); err == nil && st.IsDir() {
		if err := os.MkdirAll(base, 0o755); err == nil {
			return base
		}
	}
	return ""
}

func envHasKey(env []string, key string) bool {
	prefix := strings.ToUpper(key) + "="
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), prefix) {
			return true
		}
	}
	return false
}

func whisperxIsNoAlignModelErr(out []byte) bool {
	lower := strings.ToLower(decodeTextFile(out))
	return strings.Contains(lower, "no default align-model for language") ||
		strings.Contains(lower, "no default alignment model for language")
}

func whisperxIsOOM(out []byte) bool {
	lower := strings.ToLower(decodeTextFile(out))
	return strings.Contains(lower, "out of memory") ||
		strings.Contains(lower, "cuda error: out of memory")
}

func whisperxSummarizeFailure(out []byte) string {
	text := decodeTextFile(out)
	var picks []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lc := strings.ToLower(line)
		if strings.HasPrefix(lc, "transcript:") || strings.HasPrefix(lc, "progress:") {
			continue
		}
		if strings.Contains(lc, "torchcodec") ||
			strings.Contains(lc, "traceback") ||
			strings.Contains(lc, "exception") ||
			strings.Contains(lc, "error") ||
			strings.Contains(lc, "failed") ||
			strings.Contains(lc, "fix torchcodec") {
			picks = append(picks, line)
		}
	}
	if len(picks) > 0 {
		msg := strings.Join(picks, " ")
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return msg
	}
	trim := strings.TrimSpace(text)
	if len(trim) > 400 {
		trim = trim[len(trim)-400:]
	}
	if trim == "" {
		return "请检查 Python 环境、GPU 驱动及网络代理（首次需下载对齐模型）"
	}
	return trim
}

func (a *App) whisperxSaveRunLog(outputDir, stem string, out []byte, failed bool) {
	if outputDir == "" || len(out) == 0 {
		return
	}
	name := stem + ".whisperx.log"
	if failed {
		name = stem + ".whisperx.error.log"
	}
	path := filepath.Join(outputDir, name)
	if err := os.WriteFile(path, out, 0644); err != nil {
		a.log("warn", "保存运行日志失败: "+err.Error())
		return
	}
	a.log("info", "运行日志已保存: "+name)
}

func (a *App) extractWhisperxAudio(videoFile string) (wavPath string, err error) {
	wavPath = filepath.Join(filepath.Dir(videoFile), fmt.Sprintf("muxer_wx_%d.wav", time.Now().UnixNano()))
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", videoFile,
		"-vn",
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-ac", "1", "-ar", "16000",
		wavPath,
	)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(wavPath)
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(decodeGBK(out)))
	}
	info, err := os.Stat(wavPath)
	if err != nil || info.Size() == 0 {
		os.Remove(wavPath)
		return "", fmt.Errorf("提取的音频为空")
	}
	return wavPath, nil
}

func (a *App) CheckWhisperX(pythonPath, httpProxy, modelDir string) bool {
	py := resolveWhisperXPython(pythonPath)
	if py == "" {
		a.log("warn", "未配置 WhisperX Python 路径")
		return false
	}
	cmd := exec.Command(py, "-m", "whisperx", "--version")
	cmd.Env = whisperxEnv(httpProxy, modelDir)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		a.log("warn", "WhisperX 未就绪: "+strings.TrimSpace(decodeGBK(out)))
		return false
	}
	ver := strings.TrimSpace(string(out))
	if ver == "" {
		ver = "已安装"
	}
	a.log("ok", fmt.Sprintf("WhisperX %s · %s", ver, whisperxModel))
	return true
}

func (a *App) WhisperXASRTranscribe(inputPath, outputDir, language, pythonPath, httpProxy, modelDir string, aiCorrect bool) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			a.log("error", fmt.Sprintf("WhisperX 内部错误: %v", r))
			ok = false
		}
	}()

	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	py := resolveWhisperXPython(pythonPath)
	if py == "" {
		a.log("error", "未配置 WhisperX Python 路径")
		return false
	}
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
		if !a.whisperxSingle(videoFile, outputDir, language, py, httpProxy, modelDir, aiCorrect) {
			allOK = false
		}
	}
	if len(files) > 1 {
		a.log("info", fmt.Sprintf("批量转字幕完成，共 %d 个文件", len(files)))
	}
	return allOK
}

func (a *App) whisperxSingle(videoFile, outputDir, language, pythonPath, httpProxy, modelDir string, aiCorrect bool) bool {
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	outFile := filepath.Join(outputDir, stem+".srt")

	a.log("info", fmt.Sprintf("WhisperX %s · %s", whisperxModel, filepath.Base(videoFile)))
	a.log("info", "提取音频")

	tempAudio, err := a.extractWhisperxAudio(videoFile)
	if err != nil {
		a.log("error", "提取音频失败: "+err.Error())
		return false
	}
	defer os.Remove(tempAudio)

	tempStem := strings.TrimSuffix(filepath.Base(tempAudio), filepath.Ext(tempAudio))
	tempSrt := filepath.Join(outputDir, tempStem+".srt")
	defer func() {
		for _, ext := range []string{".json", ".vtt", ".tsv", ".txt"} {
			os.Remove(filepath.Join(outputDir, tempStem+ext))
		}
		if tempSrt != outFile {
			os.Remove(tempSrt)
		}
	}()

	if a.stopped.Load() {
		a.log("warn", "转字幕已取消")
		return false
	}

	a.log("info", fmt.Sprintf("识别 %s", whisperxModel))

	buildArgs := func(lang string, beamSize, batchSize int) []string {
		args := []string{
			"-m", "whisperx", tempAudio,
			"--model", whisperxModel,
			"--device", "cuda",
			"--compute_type", "float16",
			"--batch_size", strconv.Itoa(batchSize),
			"--beam_size", strconv.Itoa(beamSize),
			"--best_of", "5",
			"--patience", "2",
			"--output_format", "all",
			"--output_dir", outputDir,
			"--print_progress", "True",
		}
		if lang != "" && lang != "auto" {
			args = append(args, "--language", lang)
		}
		return args
	}

	runWhisperx := func(lang string, beamSize, batchSize int) ([]byte, error) {
		cmd := exec.Command(pythonPath, buildArgs(lang, beamSize, batchSize)...)
		cmd.Env = whisperxEnv(httpProxy, modelDir)
		hideWindow(cmd)
		return a.runCancellableCmd(cmd)
	}

	runWX := func(lang string) ([]byte, error) {
		cfgs := [][2]int{{10, 4}, {10, 2}, {5, 2}}
		var out []byte
		var err error
		for i, c := range cfgs {
			out, err = runWhisperx(lang, c[0], c[1])
			if err == nil || a.stopped.Load() || !whisperxIsOOM(out) {
				return out, err
			}
			if i < len(cfgs)-1 {
				a.log("warn", fmt.Sprintf("显存不足，降档重试 beam=%d batch=%d", cfgs[i+1][0], cfgs[i+1][1]))
			} else {
				a.log("error", "显存不足，最低档仍失败")
			}
		}
		return out, err
	}

	out, err := runWX(language)
	if err != nil && !a.stopped.Load() &&
		(language == "" || language == "auto") &&
		whisperxIsNoAlignModelErr(out) {
		a.log("warn", "语言无对齐模型，按英文重试")
		out, err = runWX("en")
	}
	if err != nil {
		a.whisperxSaveRunLog(outputDir, stem, out, true)
		if a.stopped.Load() {
			a.log("warn", "转字幕已取消")
			return false
		}
		errStr := whisperxSummarizeFailure(out)
		lower := strings.ToLower(errStr)
		switch {
		case strings.Contains(lower, "torchcodec"):
			a.log("error", "WhisperX 失败: "+errStr)
			a.log("info", "pip install torchcodec")
		case whisperxIsNoAlignModelErr(out):
			a.log("error", "WhisperX 失败: "+errStr)
			a.log("info", "指定语言后重试")
		default:
			a.log("error", "WhisperX 失败: "+errStr)
		}
		return false
	}

	resegOK := false
	if jsonData, jErr := os.ReadFile(filepath.Join(outputDir, tempStem+".json")); jErr == nil {
		if reseg := resegmentWhisperxJSON(jsonData); len(reseg) > 0 {
			if wErr := os.WriteFile(outFile, []byte(buildSRTFromEntries(reseg)), 0644); wErr == nil {
				resegOK = true
				a.log("info", fmt.Sprintf("句子重组 %d 条", len(reseg)))
			}
		}
	}
	if !resegOK && tempSrt != outFile {
		if _, statErr := os.Stat(tempSrt); statErr == nil {
			_ = os.Remove(outFile)
			if renameErr := os.Rename(tempSrt, outFile); renameErr != nil {
				a.log("error", "重命名输出字幕失败: "+renameErr.Error())
				return false
			}
		}
	}

	if _, err := os.Stat(outFile); err != nil {
		a.log("error", "未找到输出字幕: "+filepath.Base(outFile))
		return false
	}
	raw, err := os.ReadFile(outFile)
	if err != nil || len(raw) == 0 {
		a.log("error", "字幕文件为空或格式异常")
		return false
	}
	entries := parseSRTEntries(decodeTextFile(raw))
	if len(entries) == 0 {
		a.log("error", "字幕文件为空或格式异常")
		return false
	}
	a.log("ok", fmt.Sprintf("字幕 %d 条 → %s", len(entries), filepath.Base(outFile)))
	if aiCorrect {
		a.asrPostCorrect(outFile)
	} else {
		a.log("info", "跳过 AI 修正")
	}
	return true
}
