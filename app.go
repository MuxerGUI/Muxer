package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

type App struct {
	ctx     context.Context
	cmd     *exec.Cmd
	cmdLock sync.Mutex
	stopped atomic.Bool

	processing atomic.Bool

	keepSourceFlag atomic.Bool

	queueMu         sync.Mutex
	queue           []*QueueTask
	queueRunning    atomic.Bool
	queueStop       chan struct{}
	queueRestoreMsg string
}

type QueueTask struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Opts       ProcessOptions `json:"opts"`
	Status     string         `json:"status"`
	AddedAt    string         `json:"addedAt"`
	StartedAt  string         `json:"startedAt"`
	FinishedAt string         `json:"finishedAt"`
	Err        string         `json:"err"`
	Attempts   int            `json:"attempts"`
}

const queueMaxAttempts = 2

func NewApp() *App {
	a := &App{}
	a.queueRestoreMsg = a.loadQueueSnapshot()
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.queueRestoreMsg != "" {
		a.log("info", a.queueRestoreMsg)
		a.queueRestoreMsg = ""
		a.emitQueue()
	}
	runtime.OnFileDrop(ctx, func(x, y int, paths []string) {
		for _, p := range paths {
			if info, err := os.Stat(p); err == nil && info.IsDir() {
				runtime.EventsEmit(ctx, "folder-dropped", p)
				return
			}
		}
	})
}

func (a *App) shutdown(ctx context.Context) {
	a.stopped.Store(true)
	a.killActiveCmdLocked()
}

func (a *App) emit(event string, data interface{}) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, event, data)
}

func (a *App) log(level, msg string) {
	a.emit("log", map[string]string{
		"time":  time.Now().Format("15:04:05"),
		"level": level,
		"msg":   msg,
	})
}

func decodeGBK(b []byte) string {
	dec := simplifiedchinese.GBK.NewDecoder()
	out, _, err := transform.Bytes(dec, b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

func (a *App) SelectSaveFile(title, defaultName string) string {
	path, _ := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           title,
		DefaultFilename: defaultName,
		Filters: []runtime.FileFilter{
			{DisplayName: "字幕文件", Pattern: "*.srt;*.ass;*.ssa;*.sup"},
		},
	})
	return path
}

func (a *App) SelectDirectory(title string) string {
	dir, _ := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	return dir
}

func (a *App) SelectFile(title string) string {
	file, _ := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
		Filters: []runtime.FileFilter{
			{DisplayName: "视频/字幕文件", Pattern: "*.mkv;*.mp4;*.avi;*.mov;*.srt;*.ass;*.ssa;*.sup;*.sub;*.idx;*.vtt;*.ttml"},
		},
	})
	return file
}

func (a *App) SelectVideoFile(title string) string {
	file, _ := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
		Filters: []runtime.FileFilter{
			{DisplayName: "视频文件", Pattern: "*.mkv;*.mp4;*.avi;*.mov;*.wmv;*.flv"},
		},
	})
	return file
}

func (a *App) SelectSubtitleFile(title string) string {
	file, _ := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title,
		Filters: []runtime.FileFilter{
			{DisplayName: "字幕文件", Pattern: "*.srt;*.ass;*.ssa;*.sup;*.sub;*.idx;*.vtt;*.ttml"},
		},
	})
	return file
}

func (a *App) OpenDirectory(dir string) {
	exec.Command("explorer", dir).Start()
}

func (a *App) CheckFFmpeg() bool {
	cmd := exec.Command("ffmpeg", "-version")
	hideWindow(cmd)
	return cmd.Run() == nil
}

func (a *App) StopProcess() {
	a.stopped.Store(true)
	a.killActiveCmdLocked()
	a.log("warn", "已停止")
	if a.queueRunning.Load() {
		a.StopQueue()
	}
}

func (a *App) beginJob() bool {
	if !a.processing.CompareAndSwap(false, true) {
		a.log("warn", "已有任务在执行")
		return false
	}
	a.stopped.Store(false)
	chatCallCount.Store(0)
	chatCallProvider.Store("")
	return true
}

func (a *App) endJob() {
	if n := chatCallCount.Load(); n > 0 {
		a.log("info", fmt.Sprintf("本次任务共调用 %s %d 次", chatProviderUsed(), n))
	}
	a.processing.Store(false)
}

var errCancelled = errors.New("已取消")

func isCancelled(err error) bool { return errors.Is(err, errCancelled) }

func (a *App) sleepOrStop(d time.Duration) bool {
	const tick = 200 * time.Millisecond
	for remaining := d; remaining > 0; remaining -= tick {
		if a.stopped.Load() {
			return false
		}
		step := tick
		if remaining < step {
			step = remaining
		}
		time.Sleep(step)
	}
	return !a.stopped.Load()
}

func (a *App) killActiveCmdLocked() {
	a.cmdLock.Lock()
	cmd := a.cmd
	a.cmdLock.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid > 0 {
		killCmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
		hideWindow(killCmd)
		_ = killCmd.Run()
	}
	_ = cmd.Process.Kill()
}

type TrackInfo struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
}

type ScanResult struct {
	VideoFile     string      `json:"videoFile"`
	Width         int         `json:"width"`
	Height        int         `json:"height"`
	Duration      float64     `json:"duration"`
	DurationStr   string      `json:"durationStr"`
	VideoCodec    string      `json:"videoCodec"`
	FileSize      string      `json:"fileSize"`
	FileSizeBytes int64       `json:"fileSizeBytes"`
	VideoBitrate  int         `json:"videoBitrate"`
	IsHDR         bool        `json:"isHdr"`
	IsDV          bool        `json:"isDv"`
	DVProfile     int         `json:"dvProfile"`
	AudioTracks   []TrackInfo `json:"audioTracks"`
	SubTracks     []TrackInfo `json:"subTracks"`
	SubFile       string      `json:"subFile"`
}

func (a *App) ScanDirectory(inputDir string) *ScanResult {
	videoFiles := findFiles(inputDir, videoExts)
	if len(videoFiles) == 0 {
		return nil
	}
	videoFile := videoFiles[0]

	cmd := exec.Command("ffprobe", "-v", "quiet",
		"-print_format", "json",
		"-show_format", "-show_streams", "-show_side_data",
		videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil
	}

	result := &ScanResult{VideoFile: filepath.Base(videoFile)}

	if fi, err := os.Stat(videoFile); err == nil {
		result.FileSize = friendlyFileSize(fi.Size())
		result.FileSizeBytes = fi.Size()
	}

	if fmtMap, ok := raw["format"].(map[string]interface{}); ok {
		if dur, ok := fmtMap["duration"].(string); ok {
			fmt.Sscanf(dur, "%f", &result.Duration)
			result.DurationStr = friendlyDuration(result.Duration)
		}
	}

	if streams, ok := raw["streams"].([]interface{}); ok {
		aIdx, sIdx := 0, 0
		for _, s := range streams {
			stream, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			ct := fmt.Sprintf("%v", stream["codec_type"])
			codec := fmt.Sprintf("%v", stream["codec_name"])
			lang, title := "", ""
			if tags, ok := stream["tags"].(map[string]interface{}); ok {
				if v := fmt.Sprintf("%v", tags["language"]); v != "<nil>" {
					lang = v
				}
				if v := fmt.Sprintf("%v", tags["title"]); v != "<nil>" {
					title = v
				}
			}
			switch ct {
			case "video":
				if fmt.Sprintf("%v", stream["disposition"]) != "<nil>" {
					if disp, ok := stream["disposition"].(map[string]interface{}); ok {
						if ap, _ := disp["attached_pic"].(float64); ap == 1 {
							continue
						}
					}
				}
				result.VideoCodec = codec
				if w, ok := stream["width"].(float64); ok {
					result.Width = int(w)
				}
				if h, ok := stream["height"].(float64); ok {
					result.Height = int(h)
				}
				if br, ok := stream["bit_rate"].(string); ok {
					var bps int
					fmt.Sscanf(br, "%d", &bps)
					result.VideoBitrate = bps / 1000
				}
				if result.VideoBitrate == 0 {
					if fmtMap, ok := raw["format"].(map[string]interface{}); ok {
						if br, ok := fmtMap["bit_rate"].(string); ok {
							var bps int
							fmt.Sscanf(br, "%d", &bps)
							result.VideoBitrate = bps / 1000
						}
					}
				}
				if ct2, ok := stream["color_transfer"].(string); ok {
					result.IsHDR = ct2 == "smpte2084" || ct2 == "arib-std-b67"
				}
				if !result.IsHDR {
					if cp, ok := stream["color_primaries"].(string); ok {
						result.IsHDR = cp == "bt2020"
					}
				}
				if sdList, ok := stream["side_data_list"].([]interface{}); ok {
					for _, sd := range sdList {
						if sdMap, ok := sd.(map[string]interface{}); ok {
							if sdType, ok := sdMap["side_data_type"].(string); ok && sdType == "DOVI" {
								result.IsDV = true
								if dvP, ok := sdMap["dv_profile"].(float64); ok {
									result.DVProfile = int(dvP)
								}
								break
							}
						}
					}
				}
			case "audio":
				result.AudioTracks = append(result.AudioTracks, TrackInfo{aIdx, codec, lang, title})
				aIdx++
			case "subtitle":
				result.SubTracks = append(result.SubTracks, TrackInfo{sIdx, codec, lang, title})
				sIdx++
			}
		}
	}

	if subs := findFiles(inputDir, subtitleExts); len(subs) > 0 {
		result.SubFile = filepath.Base(subs[0])
	}

	return result
}

type ProcessOptions struct {
	InputDir         string   `json:"inputDir"`
	OutputDir        string   `json:"outputDir"`
	Hardsub          bool     `json:"hardsub"`
	CRF              int      `json:"crf"`
	Preset           string   `json:"preset"`
	Encoder          string   `json:"encoder"`
	Codec            string   `json:"codec"`
	ScaleHeight      int      `json:"scaleHeight"`
	NvencAutoScale   bool     `json:"nvencAutoScale"`
	NvencAutoCQ      bool     `json:"nvencAutoCQ"`
	NvencAutoCrop    bool     `json:"nvencAutoCrop"`
	CropExpr         string   `json:"-"`
	Format           string   `json:"format"`
	KeepOriginalSubs bool     `json:"keepOriginalSubs"`
	EmbedFonts       bool     `json:"embedFonts"`
	NormalizeAudio   bool     `json:"normalizeAudio"`
	SubtitleOffset   float64  `json:"subtitleOffset"`
	AudioTrackIndex  int      `json:"audioTrackIndex"`
	AudioDelay       int      `json:"audioDelay"`
	TenBit           bool     `json:"tenBit"`
	HDRPassthrough   bool     `json:"hdrPassthrough"`
	HDR2SDR          bool     `json:"hdr2sdr"`
	AutoCrop         bool     `json:"autoCrop"`
	StripDV          bool     `json:"stripDv"`
	AudioBitrate     string   `json:"audioBitrate"`
	MultiSub         bool     `json:"multiSub"`
	ExtraArgs        string   `json:"extraArgs"`
	FilenameTmpl     string   `json:"filenameTmpl"`
	Notify           bool     `json:"notify"`
	AutoOpenOutput   bool     `json:"autoOpenOutput"`
	KeepSubStyle     bool     `json:"keepSubStyle"`
	DynAdBumperPath  string   `json:"-"`
	DynAdHeadPad     float64  `json:"-"`
	DynAdTailPad     float64  `json:"-"`
	DynAdChapterMeta string   `json:"-"`
	ShutdownAfter    bool     `json:"shutdownAfter"`
	WebVersion       bool     `json:"webVersion"`
	WebBitrateKbps   int      `json:"webBitrateKbps"`
	WebCodec         string   `json:"webCodec"`
	web              *webSpec `json:"-"`
}

func autoNvencScaleHeight(srcHeight int) int {
	switch {
	case srcHeight >= 2160:
		return 0
	case srcHeight > 1080:
		return 1080
	case srcHeight == 1080:
		return 1080
	case srcHeight >= 720:
		return 0
	case srcHeight > 0:
		return 0
	default:
		return 1080
	}
}

func autoNvencCQ(videoBitrateKbps int, srcHeight int) int {
	cq := 30
	switch {
	case videoBitrateKbps > 15000:
		cq = 28
	case videoBitrateKbps > 8000:
		cq = 30
	case videoBitrateKbps > 3000:
		cq = 31
	case videoBitrateKbps > 0:
		cq = 32
	}
	if srcHeight > 1440 {
		if cq > 26 {
			cq -= 2
		}
		if cq < 26 {
			cq = 26
		}
	}
	return cq
}

func shouldEnableAutoCrop(cropExpr string, srcH int) bool {
	m := reCropExpr.FindStringSubmatch(cropExpr)
	if len(m) != 5 || srcH <= 0 {
		return false
	}
	ch, _ := strconv.Atoi(m[2])
	return srcH-ch >= 120
}

func (a *App) ProcessFiles(opts ProcessOptions) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	a.cleanupTempArtifacts(opts.InputDir)
	a.log("info", "扫描文件")

	if !a.CheckFFmpeg() {
		a.log("error", "FFmpeg 不可用")
		return false
	}
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败: "+err.Error())
		return false
	}
	if opts.WebVersion && opts.Hardsub {
		if c := a.resolveWebCodec(opts.WebCodec); c == "" {
			opts.WebVersion = false
		} else {
			opts.WebCodec = c
		}
	}

	videoFiles := findFilesRecursive(opts.InputDir, videoExts)
	subtitleFiles := findFilesRecursive(opts.InputDir, subtitleExts)
	if kept, n := dropWebRenditions(videoFiles); n > 0 {
		videoFiles = kept
		a.log("warn", fmt.Sprintf("跳过 %d 个在线版", n))
	}

	if len(videoFiles) == 0 {
		a.log("error", "未找到视频文件")
		return false
	}
	if len(subtitleFiles) == 0 {
		a.log("error", "未找到字幕文件")
		return false
	}

	videoFiles = dedupBasenames(videoFiles, func(kept, dropped string) {
		a.log("warn", "同名跳过: "+filepath.Base(dropped))
	})

	pairs := pairVideosAndSubsByDir(videoFiles, subtitleFiles, opts.MultiSub && !opts.Hardsub, func(vf string) {
		a.log("warn", "无匹配字幕: "+filepath.Base(vf))
	})
	if len(pairs) == 0 {
		a.log("error", "无法配对视频和字幕")
		return false
	}

	total := len(pairs)
	a.log("info", fmt.Sprintf("共 %d 组", total))
	a.PlaySoundStartProcess()

	successCount := 0
	absOutput, _ := filepath.Abs(opts.OutputDir)
	inputUseCounts := buildInputUseCounts(pairs)
	for i, pair := range pairs {
		if a.stopped.Load() {
			a.log("warn", fmt.Sprintf("已中止 %d/%d", successCount, total))
			break
		}
		if total > 1 {
			a.log("info", fmt.Sprintf("── [%d/%d] %s", i+1, total, filepath.Base(pair.video)))
		}
		if a.processPair(pair.video, pair.subtitles, opts) {
			successCount++
			if a.keepSourceFlag.Load() {
				a.log("info", "保留源文件")
			} else {
				a.releaseInputUseCounts(inputUseCounts, pair.video, pair.subtitles)
				a.removeProcessedInputs(absOutput, inputUseCounts, pair.video, pair.subtitles)
			}
		}
	}

	if a.stopped.Load() {
		return successCount > 0
	}

	msg := fmt.Sprintf("完成 %d/%d", successCount, total)
	if successCount == total {
		a.log("ok", msg)
	} else {
		a.log("warn", msg)
	}

	if opts.Notify {
		a.sendNotification(msg)
	}
	if successCount > 0 {
		a.PlaySoundMuxDone()
	}
	if opts.AutoOpenOutput && successCount > 0 {
		exec.Command("explorer", opts.OutputDir).Start()
	}
	if opts.ShutdownAfter && successCount == total {
		a.log("info", "60 秒后关机")
		shutdownCmd := exec.Command("shutdown", "/s", "/t", "60")
		hideWindow(shutdownCmd)
		shutdownCmd.Start()
	}

	return successCount > 0
}

func buildInputUseCounts(pairs []filePair) map[string]int {
	counts := make(map[string]int)
	for _, pair := range pairs {
		counts[fileKey(pair.video)]++
		for _, s := range pair.subtitles {
			counts[fileKey(s)]++
		}
	}
	return counts
}

func fileKey(path string) string {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	return strings.ToLower(filepath.Clean(absPath))
}

func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func (a *App) checkDiskSpace(videoFile, outputDir string, info *videoInfo, hardsub bool, webKbps int) {
	free := freeDiskBytes(outputDir)
	if free == 0 {
		return
	}
	fi, err := os.Stat(videoFile)
	if err != nil {
		return
	}
	src := uint64(fi.Size())
	need := src
	if hardsub {
		need = src * 6 / 10
	}
	if shouldPreRemux(videoFile, info) {
		need += src
	}
	if webKbps > 0 && info != nil && info.Duration > 0 {
		need += uint64(webKbps) * 1000 / 8 * uint64(info.Duration)
	}
	if free < need {
		a.log("warn", fmt.Sprintf("磁盘空间 %s < 需要 %s",
			friendlyFileSize(int64(free)), friendlyFileSize(int64(need))))
	}
}

func uniqueTempPath(prefix, ext string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s_%d_%d%s", prefix, os.Getpid(), time.Now().UnixNano(), ext))
}

func pathWithinDir(path, dir string) bool {
	p := strings.ToLower(filepath.Clean(path))
	d := strings.ToLower(filepath.Clean(dir))
	return p == d || strings.HasPrefix(p, d+string(filepath.Separator))
}

func (a *App) safeOutputPath(outputPath string, inputs []string) string {
	conflictsInput := func(p string) bool {
		key := fileKey(p)
		for _, in := range inputs {
			if fileKey(in) == key {
				return true
			}
		}
		return false
	}
	switch {
	case conflictsInput(outputPath):
		a.log("warn", "输出与输入同名，已改名: "+filepath.Base(outputPath))
	default:
		if _, err := os.Stat(outputPath); err != nil {
			return outputPath
		}
		a.log("warn", "输出已存在，已改名: "+filepath.Base(outputPath))
	}
	ext := filepath.Ext(outputPath)
	stem := strings.TrimSuffix(outputPath, ext)
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if conflictsInput(cand) {
			continue
		}
		if _, err := os.Stat(cand); err == nil {
			continue
		}
		a.log("info", "本次输出: "+filepath.Base(cand))
		return cand
	}
	return fmt.Sprintf("%s.%d%s", stem, time.Now().Unix(), ext)
}

func (a *App) verifyOutputBeforeSourceDelete(outputPath string, srcDuration float64) (bool, string) {
	st, err := os.Stat(outputPath)
	if err != nil {
		return false, "输出文件不存在"
	}
	minSize := int64(1) << 20
	if srcDuration > 0 {
		if byDur := int64(srcDuration * 20 * 1024); byDur < minSize {
			minSize = byDur
		}
	}
	if minSize < 4096 {
		minSize = 4096
	}
	if st.Size() < minSize {
		return false, fmt.Sprintf("输出体积异常（%s）", friendlyFileSize(st.Size()))
	}
	if srcDuration > 0 {
		if outInfo, perr := getVideoInfo(outputPath); perr == nil && outInfo != nil && outInfo.Duration > 0 {
			tol := srcDuration * 0.03
			if tol < 5 {
				tol = 5
			}
			if outInfo.Duration < srcDuration-tol {
				return false, fmt.Sprintf("输出时长 %s 明显短于源 %s", friendlyDuration(outInfo.Duration), friendlyDuration(srcDuration))
			}
		}
	}
	return true, ""
}

func containerHasDuration(path string) bool {
	info, err := getVideoInfo(path)
	return err == nil && info != nil && info.Duration > 0
}

func (a *App) repairUnfinalizedOutput(outputPath string) (string, bool) {
	dir := filepath.Dir(outputPath)
	ext := filepath.Ext(outputPath)
	fixedPath := filepath.Join(dir, strings.TrimSuffix(filepath.Base(outputPath), ext)+".muxer-fix"+ext)
	os.Remove(fixedPath)

	fixArgs := []string{"-y", "-hide_banner", "-loglevel", "error",
		"-i", outputPath,
		"-c", "copy",
		"-map", "0",
		"-map_metadata", "0",
		"-map_chapters", "0"}
	fixArgs = append(fixArgs, mp4FaststartArgs(fixedPath)...)
	cmd := exec.Command("ffmpeg", append(fixArgs, fixedPath)...)
	hideWindow(cmd)

	a.cmdLock.Lock()
	a.cmd = cmd
	a.cmdLock.Unlock()
	defer func() {
		a.cmdLock.Lock()
		a.cmd = nil
		a.cmdLock.Unlock()
	}()

	if err := cmd.Run(); err != nil {
		a.log("warn", "重封装失败，保留原成品: "+err.Error())
		os.Remove(fixedPath)
		return outputPath, false
	}
	oldSt, err1 := os.Stat(outputPath)
	newSt, err2 := os.Stat(fixedPath)
	if err1 != nil || err2 != nil || newSt.Size() < oldSt.Size()*9/10 || !containerHasDuration(fixedPath) {
		a.log("warn", "重封装结果异常，保留原成品")
		os.Remove(fixedPath)
		return outputPath, false
	}
	if err := os.Rename(fixedPath, outputPath); err != nil {
		a.log("warn", "重封装替换失败: "+err.Error())
		os.Remove(fixedPath)
		return outputPath, false
	}
	return outputPath, true
}

func (a *App) finalizeSuccessfulOutput(outputPath string, info *videoInfo, opts ProcessOptions) {
	if opts.Hardsub {
		if frozen := detectFrozenOutput(outputPath, info.Duration, opts.DynAdHeadPad); frozen {
			a.keepSourceFlag.Store(true)
			a.log("warn", "输出疑似画面冻结，已保留源文件")
		}
	}
	if safe, why := a.verifyOutputBeforeSourceDelete(outputPath, info.Duration); !safe {
		a.keepSourceFlag.Store(true)
		a.log("warn", "输出校验未通过: "+why+"，保留源文件")
	}
}

func (a *App) acceptTailExitOutput(outputPath string, info *videoInfo, opts ProcessOptions) string {
	if fixed, ok := a.repairUnfinalizedOutput(outputPath); ok {
		outputPath = fixed
		a.log("info", "重封装修复索引")
	} else {
		a.keepSourceFlag.Store(true)
		a.log("warn", "成品未收尾，重封装失败，保留源文件")
		return outputPath
	}
	a.finalizeSuccessfulOutput(outputPath, info, opts)
	return outputPath
}

func (a *App) releaseInputUseCounts(inputUseCounts map[string]int, videoFile string, subtitleFiles []string) {
	files := append([]string{videoFile}, subtitleFiles...)
	removed := make(map[string]bool)
	for _, f := range files {
		key := fileKey(f)
		if removed[key] {
			continue
		}
		removed[key] = true
		inputUseCounts[key]--
	}
}

func isBitmapSubPath(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".sup" || ext == ".idx" || ext == ".mks"
}

func (a *App) removeProcessedInputs(absOutput string, inputUseCounts map[string]int, videoFile string, subtitleFiles []string) {
	files := append([]string{videoFile}, subtitleFiles...)
	removed := make(map[string]bool)
	for _, f := range files {
		key := fileKey(f)
		if inputUseCounts[key] > 0 || removed[key] {
			continue
		}
		removed[key] = true
		absPath, err := filepath.Abs(f)
		if err == nil && sameDir(filepath.Dir(absPath), absOutput) {
			continue
		}
		if err := os.Remove(f); err != nil {
			if !os.IsNotExist(err) {
				a.log("warn", "源文件删除失败: "+filepath.Base(f)+" - "+err.Error())
			}
		} else {
			a.log("info", "删除源文件 "+filepath.Base(f))
		}
	}
}

func (a *App) processPair(videoFile string, subtitleFiles []string, opts ProcessOptions) (ok bool) {
	a.keepSourceFlag.Store(false)
	originalVideoFile := videoFile
	a.log("info", "视频: "+filepath.Base(videoFile))

	if detectHardcodedSubtitle(videoFile) {
		a.log("warn", "跳过: 文件名含硬压关键词")
		return false
	}

	info, err := getVideoInfo(videoFile)
	if err != nil {
		a.log("error", "无法读取视频信息: "+err.Error())
		return false
	}
	if opts.Hardsub && opts.NvencAutoScale && opts.Encoder == "nvenc" {
		scale := autoNvencScaleHeight(info.Height)
		opts.ScaleHeight = scale
		if scale > 0 && scale != info.Height {
			a.log("info", fmt.Sprintf("缩放 %dp → %dp", info.Height, scale))
		}
	}
	if opts.Hardsub && opts.NvencAutoCQ && opts.Encoder == "nvenc" {
		cq := autoNvencCQ(info.VideoBitrateKbps, info.Height)
		opts.CRF = cq
		if info.VideoBitrateKbps > 0 {
			a.log("info", fmt.Sprintf("CQ %d · 源 %d kbps", cq, info.VideoBitrateKbps))
		} else {
			a.log("info", fmt.Sprintf("CQ %d", cq))
		}
	}
	if opts.Hardsub && opts.NvencAutoCrop && opts.Encoder == "nvenc" && !opts.AutoCrop {
		cropExpr := detectCrop(videoFile, info.Duration, info.Width, info.Height)
		if cropExpr != "" && shouldEnableAutoCrop(cropExpr, info.Height) {
			opts.AutoCrop = true
			opts.CropExpr = cropExpr
			a.log("info", "裁边 "+cropExpr)
		}
	}

	subtitleFile := subtitleFiles[0]
	if len(subtitleFiles) == 1 {
		a.log("info", "字幕: "+filepath.Base(subtitleFile))
	} else {
		names := make([]string, len(subtitleFiles))
		for i, s := range subtitleFiles {
			names[i] = filepath.Base(s)
		}
		a.log("info", fmt.Sprintf("字幕 ×%d: %s", len(subtitleFiles), strings.Join(names, " | ")))
	}

	if filtered := a.filterToChineseSubtitles(subtitleFiles); len(filtered) < len(subtitleFiles) {
		subtitleFiles = filtered
		subtitleFile = subtitleFiles[0]
	}

	if !opts.KeepSubStyle {
		cleanedSubs := make([]string, len(subtitleFiles))
		var cleanTmpPaths []string
		for i, sf := range subtitleFiles {
			if tp := a.preprocessSubtitleForMux(sf); tp != "" {
				cleanedSubs[i] = tp
				cleanTmpPaths = append(cleanTmpPaths, tp)
			} else {
				cleanedSubs[i] = sf
			}
		}
		if len(cleanTmpPaths) > 0 {
			defer func() {
				for _, p := range cleanTmpPaths {
					os.Remove(p)
				}
			}()
			subtitleFiles = cleanedSubs
			subtitleFile = subtitleFiles[0]
		}
	}

	webKbps := 0
	if opts.WebVersion && opts.Hardsub {
		webKbps = chooseWebBitrate(info, opts.WebBitrateKbps)
	}
	a.checkDiskSpace(videoFile, opts.OutputDir, info, opts.Hardsub, webKbps)

	if opts.Hardsub && info.Duration > 10 {
		if bumperPath, segDur, err := writeDynAdBumper(); err != nil {
			a.log("warn", "厂标跳过: "+err.Error())
		} else {
			headPad, tailPad := dynAdPads(true, true, segDur)
			opts.DynAdBumperPath = bumperPath
			opts.DynAdHeadPad, opts.DynAdTailPad = headPad, tailPad
			defer os.Remove(bumperPath)
			a.log("info", fmt.Sprintf("厂标 片头+片尾 %.1fs", headPad+tailPad))
			if meta := a.writeShiftedChapterMeta(videoFile, headPad); meta != "" {
				opts.DynAdChapterMeta = meta
				defer os.Remove(meta)
			}
			if len(info.AudioCodecs) == 0 {
				a.log("warn", "厂标 无声")
			}
		}
	}

	format := opts.Format
	if format == "" {
		format = "mp4"
	}
	if opts.Hardsub && format == "mp4" && shouldPreRemux(videoFile, info) {
		format = "mkv"
		a.log("warn", "容器 MKV: 高码率预处理")
	}
	if format == "mp4" && hasMP4IncompatAudio(info) {
		format = "mkv"
		a.log("warn", "容器 MKV: 音轨不兼容 MP4")
	}
	if !opts.Hardsub && format == "mp4" {
		for _, sf := range subtitleFiles {
			if isBitmapSubPath(sf) {
				format = "mkv"
				a.log("warn", "容器 MKV: 图形字幕")
				break
			}
		}
	}
	isMKV := format == "mkv"

	baseName := cleanFilename(strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile)))

	cleaned := reEncoderTag.ReplaceAllString(baseName, "$2")
	cleaned = strings.TrimSpace(strings.Trim(cleaned, ".-_"))
	if cleaned != "" {
		baseName = cleaned
	}

	tagsToStrip := []string{"硬压中英双语", "内封中英双语", "硬压中字", "内封中字"}
	for _, tag := range subtitleTagsToStrip() {
		if t := brandTag(); t != "" {
			tagsToStrip = append(tagsToStrip, t+"."+tag)
		}
		if defaultBrandTag != brandTag() {
			tagsToStrip = append(tagsToStrip, defaultBrandTag+"."+tag)
		}
		tagsToStrip = append(tagsToStrip, tag)
	}
	for _, tag := range tagsToStrip {
		baseName = strings.ReplaceAll(baseName, "."+tag, "")
		baseName = strings.ReplaceAll(baseName, "-"+tag, "")
		baseName = strings.ReplaceAll(baseName, "_"+tag, "")
		baseName = strings.ReplaceAll(baseName, " "+tag, "")
		baseName = strings.ReplaceAll(baseName, tag, "")
	}
	baseName = strings.TrimSpace(strings.Trim(baseName, ".-_ "))

	tmpl := opts.FilenameTmpl
	if tmpl == "" {
		tmpl = "{name}"
		if t := brandTag(); t != "" {
			tmpl += "." + t
		}
		if tag := subtitleFilenameTag(subtitleFiles); tag != "" {
			tmpl += "." + tag
		}
	}
	outputPath := filepath.Join(opts.OutputDir, strings.ReplaceAll(tmpl, "{name}", baseName)+"."+format)
	outputPath = a.safeOutputPath(outputPath, append([]string{videoFile}, subtitleFiles...))
	if opts.WebVersion && opts.Hardsub {
		outputPath = a.avoidWebNameCollision(outputPath)
	}
	if opts.Hardsub && opts.AutoCrop && opts.CropExpr == "" && !isBitmapSubPath(subtitleFile) {
		if cropVal := detectCrop(videoFile, info.Duration, info.Width, info.Height); cropVal != "" {
			opts.CropExpr = cropVal
		}
	}
	a.attachWebSpec(&opts, outputPath, info, subtitleFile)
	webPath := ""
	if opts.web != nil {
		webPath = opts.web.Path
	}
	cleanupOutputs := func() {
		os.Remove(outputPath)
		if webPath != "" {
			os.Remove(webPath)
		}
	}

	var cmd *exec.Cmd
	var historyMode string
	var subEmbedded bool

	if opts.Hardsub {
		encName := map[string]string{"nvenc": "NVENC", "amf": "AMF", "qsv": "QSV", "cpu": "x264", "": "x264"}[opts.Encoder]
		codecLabel := map[string]string{"h265": "H.265", "h264": "H.264", "": "H.264"}[opts.Codec]
		extra := ""
		if opts.ScaleHeight > 0 {
			extra += fmt.Sprintf(" · %dp", opts.ScaleHeight)
		}
		if opts.NormalizeAudio {
			extra += " · 音量标准化"
		}
		if opts.web != nil {
			extra += " · 在线版"
		}
		a.log("info", fmt.Sprintf("硬压 %s %s Q%d %s%s", encName, codecLabel, opts.CRF, opts.Preset, extra))

		validatedPath, fatalErr := a.validateSubtitleForHardsub(subtitleFile, info.Duration)
		if fatalErr != "" {
			a.log("error", "字幕预检失败: "+fatalErr)
			return false
		}
		if validatedPath != "" {
			defer os.Remove(validatedPath)
			subtitleFile = validatedPath
		}

		subExtLower := strings.ToLower(filepath.Ext(subtitleFile))
		isBitmapSubLocal := subExtLower == ".sup" || subExtLower == ".idx" || subExtLower == ".mks"

		if !isBitmapSubLocal {
			if remuxed := a.preRemuxIfNeeded(videoFile, info); remuxed != "" {
				origPath := videoFile
				originalDeleted := false
				if err := os.Remove(videoFile); err != nil && !os.IsNotExist(err) {
					a.log("warn", "原片删除失败: "+filepath.Base(videoFile)+" - "+err.Error())
				} else {
					originalDeleted = true
					a.log("info", "原片已删除: "+filepath.Base(videoFile))
				}
				defer func() {
					if ok {
						os.Remove(remuxed)
						return
					}
					if originalDeleted {
						if rerr := os.Rename(remuxed, origPath); rerr == nil {
							a.log("warn", "还原原片 "+filepath.Base(origPath))
						} else {
							a.log("warn", "预处理文件保留: "+filepath.Base(remuxed))
						}
					} else {
						os.Remove(remuxed)
					}
				}()
				videoFile = remuxed
			}
		}
		if a.stopped.Load() {
			return false
		}

		subEmbedded = false
		if isBitmapSubLocal {
			a.log("info", "图形字幕预合成")
			premuxed, perr := a.premuxBitmapSubWithVideo(videoFile, subtitleFile, info, opts.SubtitleOffset)
			if perr != nil {
				if a.stopped.Load() {
					return false
				}
				a.log("warn", "图形字幕预合成失败，改用独立输入: "+perr.Error())
			} else {
				probeCmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "s",
					"-show_entries", "stream=codec_type", "-of", "csv=p=0", premuxed)
				hideWindow(probeCmd)
				probeOut, _ := probeCmd.Output()
				if !strings.Contains(string(probeOut), "subtitle") {
					os.Remove(premuxed)
					a.log("error", "图形字幕无字幕流，无法压制")
					return false
				}
				defer os.Remove(premuxed)
				videoFile = premuxed
				subEmbedded = true
			}
		}
		if a.stopped.Load() {
			return false
		}

		if !subEmbedded {
			if renderErr := a.testSubtitleRender(videoFile, subtitleFile, info.Duration); renderErr != "" {
				a.log("error", renderErr)
				return false
			}
		}

		if opts.Encoder == "nvenc" && info.VideoBitrateKbps > 0 {
			isH265 := opts.Codec == "h265"
			outH := opts.ScaleHeight
			if outH == 0 {
				outH = info.Height
			}
			maxrate, _ := nvencMaxrate(outH, info.FPS, isH265, info.VideoBitrateKbps)
			a.log("info", "maxrate "+maxrate)
		}
		var hardsubTempFile string
		cmd, hardsubTempFile = a.buildHardsubCmd(videoFile, subtitleFile, outputPath, opts, info, subEmbedded)
		if hardsubTempFile != "" {
			defer os.Remove(hardsubTempFile)
		}
		historyMode = "硬压/" + strings.ToUpper(format)
	} else {
		extra := ""
		if opts.KeepOriginalSubs {
			extra += " · 保留原片字幕"
		}
		if opts.SubtitleOffset != 0 {
			extra += fmt.Sprintf(" · 偏移 %.1fs", opts.SubtitleOffset)
		}
		if opts.NormalizeAudio {
			extra += " · 音量标准化"
		}
		if opts.EmbedFonts && isMKV {
			fonts := findFiles(filepath.Dir(videoFile), fontExts)
			if len(fonts) > 0 {
				extra += fmt.Sprintf(" · 字体×%d", len(fonts))
			} else {
				a.log("warn", "无字体可嵌入")
			}
		}
		if !isMKV && len(subtitleFiles) > 1 {
			a.log("warn", "MP4 不支持多字幕轨")
		}
		for _, s := range subtitleFiles {
			ext := strings.ToLower(filepath.Ext(s))
			if !isMKV && (ext == ".ass" || ext == ".ssa") {
				a.log("warn", "ASS 封入 MP4 会转 mov_text")
				break
			}
		}
		a.log("info", fmt.Sprintf("软封 %s%s", strings.ToUpper(format), extra))
		cmd = a.buildSoftMuxCmd(videoFile, subtitleFiles, outputPath, opts, info)
		historyMode = "软封/" + strings.ToUpper(format)
	}

	a.log("info", "输出: "+filepath.Base(outputPath))

	fullCmdLine := strings.Join(cmd.Args, " ")

	var freezeDetected atomic.Bool
	var freezeWg sync.WaitGroup
	encodeDone := make(chan struct{})
	if opts.Hardsub && info.Duration > 60 {
		freezeWg.Add(1)
		go func() {
			defer freezeWg.Done()
			checkAt5 := info.Duration * 0.05
			checkAt10 := info.Duration * 0.10
			var hash5 string

			for i := 0; i < 120; i++ {
				select {
				case <-encodeDone:
					return
				default:
				}
				if a.stopped.Load() {
					return
				}
				time.Sleep(1 * time.Second)
				h := grabFrameHash(outputPath, checkAt5)
				if h != "" {
					hash5 = h
					break
				}
			}
			if hash5 == "" {
				return
			}

			for i := 0; i < 120; i++ {
				select {
				case <-encodeDone:
					return
				default:
				}
				if a.stopped.Load() {
					return
				}
				time.Sleep(1 * time.Second)
				h := grabFrameHash(outputPath, checkAt10)
				if h != "" && h != hash5 {
					return
				}
				if h != "" && h == hash5 {
					time.Sleep(2 * time.Second)
					h15 := grabFrameHash(outputPath, info.Duration*0.15)
					if h15 == hash5 {
						freezeDetected.Store(true)
						a.killActiveCmdLocked()
						return
					}
					return
				}
			}
		}()
	}

	start := time.Now()
	errMsg, err := a.runWithProgress(cmd, info.Duration+opts.DynAdHeadPad+opts.DynAdTailPad)
	elapsed := time.Since(start).Seconds()

	close(encodeDone)

	freezeWg.Wait()

	if freezeDetected.Load() {
		a.log("error", "画面冻结，已终止")
		cleanupOutputs()
		a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), false)
		return false
	}

	if err != nil {
		if a.stopped.Load() {
			cleanupOutputs()
			a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), false)
			return false
		}

		expectDuration := info.Duration + opts.DynAdHeadPad + opts.DynAdTailPad
		if ok, reason := isAcceptableTailExit(outputPath, expectDuration, errMsg); ok {
			a.log("warn", "收尾非 0，输出完整: "+reason)
			outputPath = a.acceptTailExitOutput(outputPath, info, opts)
			a.finalizeWebOutput(opts.WebVersion, webPath, expectDuration)
			a.log("ok", "成功 "+friendlyDuration(elapsed))
			a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), true)
			return true
		}

		if opts.Hardsub && isGPUEncoderFailure(errMsg, opts.Encoder) {
			retryHardsub := func(note string) bool {
				a.log("warn", note)
				cleanupOutputs()
				retryCmd, retryTemp := a.buildHardsubCmd(videoFile, subtitleFile, outputPath, opts, info, subEmbedded)
				if retryTemp != "" {
					defer os.Remove(retryTemp)
				}
				fullCmdLine = strings.Join(retryCmd.Args, " ")
				start = time.Now()
				errMsg, err = a.runWithProgress(retryCmd, expectDuration)
				elapsed = time.Since(start).Seconds()
				if err == nil {
					a.finalizeSuccessfulOutput(outputPath, info, opts)
					a.finalizeWebOutput(opts.WebVersion, webPath, expectDuration)
					a.log("ok", "重试成功 "+friendlyDuration(elapsed))
					a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), true)
					return true
				}
				if ok, reason := isAcceptableTailExit(outputPath, expectDuration, errMsg); ok {
					a.log("warn", "收尾非 0，输出完整: "+reason)
					outputPath = a.acceptTailExitOutput(outputPath, info, opts)
					a.finalizeWebOutput(opts.WebVersion, webPath, expectDuration)
					a.log("ok", "成功 "+friendlyDuration(elapsed))
					a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), true)
					return true
				}
				return false
			}

			if opts.web != nil {
				opts.web = nil
				if retryHardsub("双路输出失败，改为只出母版") {
					return true
				}
				if a.stopped.Load() {
					cleanupOutputs()
					a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), false)
					return false
				}
			}

			gpuName := strings.ToUpper(opts.Encoder)
			opts.Encoder = "cpu"
			opts.web = nil
			if retryHardsub(gpuName + " 失败，改用 CPU") {
				return true
			}
			if a.stopped.Load() {
				cleanupOutputs()
				a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), false)
				return false
			}
			a.log("warn", "CPU 编码重试仍失败")
		}

		logPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".error.log"
		a.writeFFmpegLog(logPath, fullCmdLine, errMsg, err.Error(), elapsed, true)

		cleanupOutputs()

		userMsg := friendlyErrorMsg(errMsg, opts.Encoder, opts.Hardsub)
		a.log("error", fmt.Sprintf("失败 %s\n%s", friendlyDuration(elapsed), userMsg))
		a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), false)
		return false
	}

	a.finalizeSuccessfulOutput(outputPath, info, opts)

	a.finalizeWebOutput(opts.WebVersion, webPath, info.Duration+opts.DynAdHeadPad+opts.DynAdTailPad)
	a.log("ok", "成功 "+friendlyDuration(elapsed))

	a.emitHistory(filepath.Base(originalVideoFile), historyMode, friendlyDuration(elapsed), true)
	return true
}

func (a *App) writeFFmpegLog(logPath, cmdLine, stderrOutput, processErr string, elapsedSec float64, failed bool) {
	var sb strings.Builder
	sb.WriteString("========== Muxer FFmpeg Log ==========\n")
	sb.WriteString(fmt.Sprintf("时间: %s\n", time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString(fmt.Sprintf("耗时: %s\n", friendlyDuration(elapsedSec)))
	if failed {
		sb.WriteString("状态: 失败\n")
	} else {
		sb.WriteString("状态: 成功\n")
	}
	sb.WriteString("\n========== FFmpeg 命令行 ==========\n")
	sb.WriteString(cmdLine + "\n")
	if strings.TrimSpace(processErr) != "" {
		sb.WriteString("\n========== 进程退出错误 ==========\n")
		sb.WriteString(processErr + "\n")
	}

	sb.WriteString("\n========== FFmpeg 完整输出 ==========\n")
	sb.WriteString(stderrOutput)

	lines := strings.Split(strings.TrimSpace(stderrOutput), "\n")
	tailStart := 0
	if len(lines) > 50 {
		tailStart = len(lines) - 50
	}
	sb.WriteString("\n========== 最后 50 行 ==========\n")
	for _, l := range lines[tailStart:] {
		sb.WriteString(l + "\n")
	}

	if err := os.WriteFile(logPath, []byte(sb.String()), 0644); err != nil {
		a.log("warn", "写入日志文件失败: "+err.Error())
	}
}

func (a *App) emitHistory(file, mode, elapsed string, ok bool) {
	a.emit("history", map[string]interface{}{
		"time":    time.Now().Format("2006-01-02 15:04"),
		"file":    file,
		"mode":    mode,
		"elapsed": elapsed,
		"ok":      ok,
	})
}

func isAcceptableTailExit(outputPath string, sourceDuration float64, stderrOutput string) (bool, string) {
	if sourceDuration <= 0 {
		return false, ""
	}
	fi, err := os.Stat(outputPath)
	if err != nil || fi.Size() < 1024*1024 {
		return false, ""
	}

	tail := strings.ToLower(stderrOutput)
	if len(tail) > 8000 {
		tail = tail[len(tail)-8000:]
	}
	for _, marker := range []string{
		"error while",
		"no space left",
		"permission denied",
		"cannot write",
		"could not write",
		"invalid data found",
		"failed to inject frame",
		"device setup failed",
		"encoder initialization failed",
	} {
		if strings.Contains(tail, marker) {
			return false, ""
		}
	}

	outInfo, err := getVideoInfo(outputPath)
	var outDuration float64
	if err == nil && outInfo.Duration > 0 {
		outDuration = outInfo.Duration
	} else {
		outDuration = lastProgressTime(stderrOutput)
	}
	if outDuration <= 0 {
		return false, ""
	}
	diff := math.Abs(outDuration - sourceDuration)
	allow := math.Max(5, sourceDuration*0.03)
	completion := outDuration / sourceDuration
	completionOK := sourceDuration >= 60 && completion >= 0.95 && completion <= 1.05
	if diff > allow && !completionOK {
		return false, ""
	}
	return true, fmt.Sprintf("输出时长 %s，源时长 %s，差异 %.1f 秒（%.1f%%）", friendlyDuration(outDuration), friendlyDuration(sourceDuration), diff, completion*100)
}

var reProgressTime = regexp.MustCompile(`time=(\d+):(\d+):(\d+(?:\.\d+)?)`)

func lastProgressTime(stderrOutput string) float64 {
	tail := stderrOutput
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	matches := reProgressTime.FindAllStringSubmatch(tail, -1)
	if len(matches) == 0 {
		return 0
	}
	last := matches[len(matches)-1]
	h, _ := strconv.Atoi(last[1])
	m, _ := strconv.Atoi(last[2])
	s, _ := strconv.ParseFloat(last[3], 64)
	return float64(h*3600+m*60) + s
}

func (a *App) buildSoftMuxCmd(videoFile string, subtitleFiles []string, outputPath string, opts ProcessOptions, info *videoInfo) *exec.Cmd {
	isMKV := strings.ToLower(filepath.Ext(outputPath)) == ".mkv"
	bitmapSub := false
	for _, sub := range subtitleFiles {
		if isBitmapSubPath(sub) {
			bitmapSub = true
			break
		}
	}

	args := []string{"ffmpeg", "-y"}
	if bitmapSub {
		args = append(args, "-copyts")
	}
	args = append(args, "-i", videoFile)

	for _, sub := range subtitleFiles {
		if opts.SubtitleOffset != 0 {
			args = append(args, "-itsoffset", fmt.Sprintf("%.3f", opts.SubtitleOffset))
		}
		args = append(args, "-i", sub)
	}

	args = append(args, "-c:v", "copy")

	isDVSoft := detectDolbyVision(videoFile)
	if isDVSoft && (opts.StripDV || !opts.HDRPassthrough) {
		bsf := ""
		if strings.Contains(strings.ToLower(info.VideoCodec), "hevc") || strings.Contains(strings.ToLower(info.VideoCodec), "h265") {
			bsf = "hevc_mp4toannexb"
		} else if strings.Contains(strings.ToLower(info.VideoCodec), "h264") || strings.Contains(strings.ToLower(info.VideoCodec), "avc") {
			bsf = "h264_mp4toannexb"
		}
		if bsf != "" {
			args = append(args, "-bsf:v", bsf)
			a.log("info", "DV 层已剥离")
		}
	}

	abr := opts.AudioBitrate
	if abr == "" {
		abr = "192k"
	}
	if opts.NormalizeAudio || opts.AudioDelay != 0 {
		var af []string
		if opts.AudioDelay > 0 {
			af = append(af, fmt.Sprintf("adelay=%d|%d", opts.AudioDelay, opts.AudioDelay))
		} else if opts.AudioDelay < 0 {
			af = append(af, fmt.Sprintf("atrim=start=%.3f", float64(-opts.AudioDelay)/1000))
		}
		if opts.NormalizeAudio {
			af = append(af, "loudnorm=I=-23:TP=-2:LRA=11")
		}
		args = append(args, "-c:a", "aac", "-b:a", abr, "-af", strings.Join(af, ","))
	} else {
		args = append(args, "-c:a", "copy")
	}

	if isMKV {
		args = append(args, "-c:s", "copy")
	} else {
		args = append(args, "-c:s", "mov_text")
	}

	args = append(args, "-map", fmt.Sprintf("0:%d", info.VideoStreamIndex))

	if opts.AudioTrackIndex < 0 {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-map", fmt.Sprintf("0:a:%d?", opts.AudioTrackIndex))
	}

	for i := range subtitleFiles {
		args = append(args, "-map", fmt.Sprintf("%d:s", i+1))
	}
	if opts.KeepOriginalSubs {
		args = append(args, "-map", "0:s?")
	}

	args = append(args, "-map_chapters", "0")
	args = append(args, "-map_metadata", "-1")
	args = append(args, brandMetadataArgs()...)
	args = append(args, "-disposition:s:0", "default")

	defaultSubTitle := "字幕"
	if n := brandName(); n != "" {
		defaultSubTitle = n + "专用字幕"
	}
	for i, sub := range subtitleFiles {
		lang, title := detectSubLang(sub)
		if lang != "" {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "language="+lang)
		}
		if title != "" {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "title="+title)
		} else {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "title="+defaultSubTitle)
		}
	}

	attached := 0
	if isMKV && opts.EmbedFonts {
		fontFiles := findFiles(filepath.Dir(videoFile), fontExts)
		for _, font := range fontFiles {
			args = append(args, "-attach", font)
			args = append(args, fmt.Sprintf("-metadata:s:t:%d", attached), "mimetype=application/x-truetype-font")
			attached++
		}
	}
	if isMKV {
		for _, sub := range subtitleFiles {
			if !isBilingualCleanASS(sub) {
				continue
			}
			for _, font := range bundledSubFontFiles() {
				args = append(args, "-attach", font)
				args = append(args, fmt.Sprintf("-metadata:s:t:%d", attached), "mimetype=application/x-truetype-font")
				attached++
			}
			break
		}
	}

	if opts.ExtraArgs != "" {
		args = append(args, parseShellArgs(opts.ExtraArgs)...)
	}
	if bitmapSub {
		args = append(args, "-avoid_negative_ts", "make_zero")
	}
	args = append(args, mp4FaststartArgs(outputPath)...)
	args = append(args, outputPath)
	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	return cmd
}

func mp4FaststartArgs(outputPath string) []string {
	switch strings.ToLower(filepath.Ext(outputPath)) {
	case ".mp4", ".m4v", ".mov":
		return []string{"-movflags", "+faststart"}
	}
	return nil
}

func escapeFFmpegPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ReplaceAll(p, ":", "\\:")
	p = strings.ReplaceAll(p, "'", "\\'")
	p = strings.ReplaceAll(p, "[", "\\[")
	p = strings.ReplaceAll(p, "]", "\\]")
	return p
}

var unsafeTmpNameReplacer = strings.NewReplacer(
	"[", "_", "]", "_", ",", "_", ";", "_", "'", "_", "\"", "_",
)

func sanitizeTmpName(name string) string {
	return unsafeTmpNameReplacer.Replace(name)
}

func (a *App) buildHardsubCmd(videoFile, subtitleFile, outputPath string, opts ProcessOptions, info *videoInfo, subEmbedded bool) (*exec.Cmd, string) {
	subExt := strings.ToLower(filepath.Ext(subtitleFile))
	isBitmapSub := (subExt == ".sup" || subExt == ".idx" || subExt == ".mks")
	tempFile := ""

	if data, readErr := os.ReadFile(subtitleFile); readErr == nil && !isBitmapSub {
		needConvert := false
		rawData := data
		if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
			rawData = data[3:]
		}
		if !utf8.Valid(rawData) {
			needConvert = true
		}

		if needConvert {
			dec := simplifiedchinese.GBK.NewDecoder()
			utf8Data, _, decErr := transform.Bytes(dec, data)
			if decErr != nil || !utf8.Valid(utf8Data) {
				converted := decodeTextFile(data)
				if utf8.ValidString(converted) {
					utf8Data = []byte(converted)
				} else {
					a.log("warn", "字幕编码未知，可能乱码")
					utf8Data = nil
				}
			}
			if utf8Data != nil {
				dir := filepath.Dir(subtitleFile)
				stem := strings.TrimSuffix(filepath.Base(subtitleFile), filepath.Ext(subtitleFile))
				tmpPath := filepath.Join(dir, sanitizeTmpName(stem)+".muxer-utf8"+subExt)
				if werr := os.WriteFile(tmpPath, utf8Data, 0644); werr == nil {
					a.log("info", "字幕转 UTF-8")
					subtitleFile = tmpPath
					tempFile = tmpPath
				} else {
					a.log("warn", "UTF-8 临时文件写入失败: "+werr.Error())
				}
			}
		}

		if subExt == ".ass" || subExt == ".ssa" {
			checkData := data
			if tempFile != "" {
				if td, re := os.ReadFile(tempFile); re == nil {
					checkData = td
				}
			}
			content := decodeTextFile(checkData)
			if !strings.Contains(content, "[Events]") {
				a.log("warn", "ASS 缺少 [Events]")
			}
			if !strings.Contains(content, "Dialogue:") {
				a.log("warn", "ASS 无 Dialogue 行")
			}
		}
	}

	if !isBitmapSub && opts.SubtitleOffset != 0 {
		if shifted, serr := shiftTextSubtitleFile(subtitleFile, opts.SubtitleOffset); serr == nil {
			if tempFile != "" {
				os.Remove(tempFile)
			}
			subtitleFile = shifted
			tempFile = shifted
			a.log("info", fmt.Sprintf("字幕偏移 %.1fs", opts.SubtitleOffset))
		} else {
			a.log("warn", "字幕偏移失败: "+serr.Error())
		}
	}

	var subFilter string
	if !isBitmapSub {
		safeName := "'" + escapeFFmpegPath(filepath.Base(subtitleFile)) + "'"
		subFilter = "subtitles=" + safeName + ":force_style='FontName=Microsoft YaHei,FontSize=16,PrimaryColour=&H00FFFFFF,OutlineColour=&H40000000,Outline=1,Shadow=0,MarginV=20,BorderStyle=1'"
		if subExt == ".ass" || subExt == ".ssa" {
			subFilter = "ass=" + safeName + assFontsDirOpt()
		}
	}

	var preSubParts []string
	var postSubParts []string

	if isBitmapSub {
		postSubParts = append(postSubParts, "setpts=PTS-STARTPTS")
	}

	if opts.AutoCrop {
		cropVal := opts.CropExpr
		if cropVal == "" {
			cropVal = detectCrop(videoFile, info.Duration, info.Width, info.Height)
		}
		if cropVal != "" && !isBitmapSub {
			preSubParts = append(preSubParts, cropVal)
			a.log("info", "裁边 "+cropVal)
		}
	}

	needHDR2SDR := opts.HDR2SDR
	if !needHDR2SDR && opts.Encoder == "qsv" {
		primaries, transfer, _ := hdrMetaOf(info, videoFile)
		if primaries == "bt2020" || transfer == "smpte2084" || transfer == "arib-std-b67" {
			needHDR2SDR = true
		}
	}
	isDVSource := detectDolbyVision(videoFile)
	if isDVSource {
		if !opts.HDRPassthrough {
			needHDR2SDR = true
		}
		if opts.StripDV || opts.HDR2SDR {
			a.log("info", "杜比视界 → SDR")
		} else {
			a.log("warn", "杜比视界 自动开启色调映射")
		}
	}
	qsvHDR := false
	if needHDR2SDR {
		if tm := wmTonemapFilter(info, videoFile); tm != "" {
			preSubParts = append(preSubParts, tm)
		} else {
			a.log("warn", "HDR→SDR 跳过: 传输曲线未知")
		}
		if opts.Encoder == "qsv" {
			qsvHDR = true
		}
	}

	if opts.ScaleHeight > 0 {
		if isBitmapSub {
			postSubParts = append(postSubParts, fmt.Sprintf("scale=-2:%d", opts.ScaleHeight))
		} else {
			preSubParts = append(preSubParts, fmt.Sprintf("scale=-2:%d", opts.ScaleHeight))
		}
	}

	if wmText := brandURL(); wmText != "" && info != nil && info.Duration > 30 {
		wmCount := int(info.Duration/1500) + 1
		if wmCount < 2 {
			wmCount = 2
		}
		if wmCount > 4 {
			wmCount = 4
		}
		segTotal := info.Duration * 0.8
		segLen := segTotal / float64(wmCount)
		wmFont := drawtextFontOpt(wmText)
		wmSize := scaledFontSize(38, outputHeightOf(opts.ScaleHeight, info))
		wmEsc := escapeDrawtext(wmText)
		for i := 0; i < wmCount; i++ {
			wmDuration := float64(6 + rand.Intn(5))
			segStart := info.Duration*0.1 + float64(i)*segLen
			segEnd := segStart + segLen - wmDuration
			if segEnd < segStart {
				segEnd = segStart
			}
			wmStart := segStart + float64(rand.Intn(int(segEnd-segStart)+1))
			wmEnd := wmStart + wmDuration
			a.log("info", fmt.Sprintf("水印 %d/%d %s-%s", i+1, wmCount, friendlyDuration(wmStart), friendlyDuration(wmEnd)))
			drawtext := fmt.Sprintf("drawtext=text='%s':fontcolor=white@0.48:fontsize=%d:x=w-tw-42:y=36:shadowcolor=black@0.35:shadowx=1:shadowy=1:enable='between(t\\,%.1f\\,%.1f)'%s",
				wmEsc, wmSize, wmStart, wmEnd, wmFont)
			postSubParts = append(postSubParts, drawtext)
		}
	}

	if opts.DynAdBumperPath != "" {
		if pad := dynAdPadFilter(opts.DynAdHeadPad, opts.DynAdTailPad); pad != "" {
			postSubParts = append(postSubParts, pad)
		}
	}

	if opts.Encoder == "nvenc" && opts.Codec == "h265" {
		postSubParts = append(postSubParts, "format=p010le")
	} else if opts.Encoder == "nvenc" || opts.Encoder == "amf" {
		postSubParts = append(postSubParts, "format=yuv420p")
	} else if opts.Encoder == "qsv" && !qsvHDR {
		if opts.TenBit {
			postSubParts = append(postSubParts, "format=p010le")
		} else {
			postSubParts = append(postSubParts, "format=yuv420p")
		}
	}

	contentDur := 0.0
	if info != nil {
		contentDur = info.Duration
	}
	useBumper := opts.DynAdBumperPath != "" && (opts.DynAdHeadPad > 0 || opts.DynAdTailPad > 0) && contentDur > 0
	bumperIdx := 0
	bumperFix := ""
	if useBumper {
		bumperIdx = dynAdBumperIndex(opts, isBitmapSub, subEmbedded)
		if opts.HDRPassthrough && !needHDR2SDR {
			bumperFix = dynAdBumperHDRFix(info, videoFile)
			if bumperFix == "" {
				a.log("warn", "厂标 SDR 叠加: 传输曲线未知")
			}
		}
	}

	baseLabel := "vout"
	if useBumper {
		baseLabel = "_dynbase"
	}
	var vf string
	var filterComplex string
	if isBitmapSub {
		pre := strings.Join(preSubParts, ",")
		post := strings.Join(postSubParts, ",")
		subRef := "[1:s:0]"
		if subEmbedded {
			subRef = "[0:s:0]"
		}
		switch {
		case pre != "" && post != "":
			filterComplex = fmt.Sprintf("[0:v]%s[_pre];[_pre]%soverlay[_ovr];[_ovr]%s[%s]", pre, subRef, post, baseLabel)
		case pre != "":
			filterComplex = fmt.Sprintf("[0:v]%s[_pre];[_pre]%soverlay[%s]", pre, subRef, baseLabel)
		case post != "":
			filterComplex = fmt.Sprintf("[0:v]%soverlay[_ovr];[_ovr]%s[%s]", subRef, post, baseLabel)
		default:
			filterComplex = fmt.Sprintf("[0:v]%soverlay[%s]", subRef, baseLabel)
		}
	} else {
		allParts := append(preSubParts, subFilter)
		allParts = append(allParts, postSubParts...)
		vf = strings.Join(allParts, ",")
	}

	if useBumper {
		if filterComplex == "" {
			if vf == "" {
				vf = "null"
			}
			filterComplex = fmt.Sprintf("[%s]%s[%s]", videoStreamLabel(info), vf, baseLabel)
			vf = ""
		}
		stage := dynAdOverlayStage("["+baseLabel+"]", "vout", bumperIdx, opts.DynAdHeadPad, opts.DynAdTailPad, contentDur, outputHeightOf(opts.ScaleHeight, info), bumperFix)
		filterComplex += ";" + stage
	}
	webTonemap := ""
	if opts.web != nil && !needHDR2SDR {
		webTonemap = wmTonemapFilter(info, videoFile)
		if webTonemap == "" && isDVSource {
			webTonemap = "zscale=t=linear:npl=100:tin=smpte2084:pin=bt2020:min=bt2020nc,format=gbrpf32le,zscale=p=bt709:t=bt709:m=bt709,format=yuv420p"
		}
		if webTonemap != "" {
			a.log("info", "在线版 HDR→SDR")
		}
	}
	if opts.web != nil {
		if filterComplex != "" {
			filterComplex = webSplitFilterEx(filterComplex, "", "vout", opts.web, webTonemap)
		} else {
			if vf == "" {
				vf = "null"
			}
			filterComplex = webSplitFilterEx(vf, videoStreamLabel(info), "", opts.web, webTonemap)
			vf = ""
		}
	}

	audioMap := "0:a?"
	if opts.AudioTrackIndex >= 0 {
		audioMap = fmt.Sprintf("0:a:%d?", opts.AudioTrackIndex)
	}
	webAudioMap := "0:a:0?"
	if opts.AudioTrackIndex >= 0 {
		webAudioMap = fmt.Sprintf("0:a:%d?", opts.AudioTrackIndex)
	}

	audioPads := dynAdAudioPadFilters(opts.DynAdHeadPad, opts.DynAdTailPad)
	var af []string
	if opts.AudioDelay > 0 {
		af = append(af, fmt.Sprintf("adelay=%d|%d", opts.AudioDelay, opts.AudioDelay))
	} else if opts.AudioDelay < 0 {
		af = append(af, fmt.Sprintf("atrim=start=%.3f", float64(-opts.AudioDelay)/1000))
	}
	if opts.NormalizeAudio {
		af = append(af, "loudnorm=I=-23:TP=-2:LRA=11")
	}
	af = append(af, audioPads...)
	afJoined := strings.Join(af, ",")

	useBumperAudio := useBumper && info != nil && len(info.AudioCodecs) > 0
	var extraAudioMaps []string
	if useBumperAudio {
		graph, aMaster, aWeb := dynAdBumperAudioGraph(dynAdMainAudioLabel(opts), bumperIdx, afJoined, opts.DynAdHeadPad, opts.DynAdTailPad, contentDur, opts.web != nil)
		if extraGraph, extraMaps := dynAdPadOtherAudio(info, opts.AudioTrackIndex, afJoined); extraGraph != "" {
			graph += ";" + extraGraph
			extraAudioMaps = extraMaps
		}
		filterComplex += ";" + graph
		audioMap = aMaster
		webAudioMap = aWeb
		afJoined = ""
	}

	crf := fmt.Sprintf("%d", opts.CRF)
	isH265 := opts.Codec == "h265"
	isAV1 := opts.Codec == "av1"

	var encArgs []string
	switch opts.Encoder {
	case "nvenc":
		cv := "h264_nvenc"
		if isH265 {
			cv = "hevc_nvenc"
		} else if isAV1 {
			cv = "av1_nvenc"
		}
		srcBitrate := 0
		outHeight := opts.ScaleHeight
		srcFPS := 0.0
		if info != nil {
			srcBitrate = info.VideoBitrateKbps
			srcFPS = info.FPS
			if outHeight == 0 {
				outHeight = info.Height
			}
		}
		maxrate, bufsize := nvencMaxrate(outHeight, srcFPS, isH265, srcBitrate)
		encArgs = []string{"-c:v", cv, "-rc:v", "vbr"}
		encArgs = append(encArgs, nvencTuneArgs(cv)...)
		encArgs = append(encArgs,
			"-cq:v", crf,
			"-b:v", "0",
			"-maxrate", maxrate,
			"-bufsize", bufsize,
			"-spatial-aq", "1",
			"-aq-strength", "8",
			"-rc-lookahead", "32",
			"-preset", nvencPreset(opts.Preset),
		)
		if isH265 {
			encArgs = append(encArgs, "-pix_fmt", "p010le")
		}
	case "amf":
		cv := "h264_amf"
		if isH265 {
			cv = "hevc_amf"
		} else if isAV1 {
			cv = "av1_amf"
		}
		encArgs = []string{"-c:v", cv, "-qp_i", crf, "-qp_p", crf, "-qp_b", crf, "-quality", amfQuality(opts.Preset)}
	case "qsv":
		cv := "h264_qsv"
		if isH265 {
			cv = "hevc_qsv"
		} else if isAV1 {
			cv = "av1_qsv"
		}
		encArgs = []string{"-c:v", cv, "-global_quality", crf, "-preset", qsvPreset(opts.Preset)}
	default:
		if isAV1 {
			encArgs = []string{"-c:v", "libsvtav1", "-crf", crf, "-preset", svtav1Preset(opts.Preset)}
		} else {
			cv := "libx264"
			if isH265 {
				cv = "libx265"
			}
			encArgs = []string{"-c:v", cv, "-crf", crf, "-preset", opts.Preset}
		}
	}

	if info != nil {
		encArgs = append(encArgs, "-g", fmt.Sprintf("%d", gopForFPS(info.FPS)))
	}

	args := []string{"ffmpeg", "-y", "-err_detect", "ignore_err"}
	if isBitmapSub {
		args = append(args, "-copyts")
	}
	if opts.Encoder == "nvenc" && !isBitmapSub {
		args = append(args, "-hwaccel", "cuda")
	}
	if isBitmapSub && subEmbedded {
		args = append(args, embeddedSubCanvasArgs(info)...)
	}
	args = append(args, "-i", videoFile)
	inputCount := 1
	if isBitmapSub && !subEmbedded {
		args = append(args, externalSubCanvasArgs(info, subtitleFile)...)
		if opts.SubtitleOffset != 0 {
			args = append(args, "-itsoffset", fmt.Sprintf("%.3f", opts.SubtitleOffset))
		}
		args = append(args, "-i", subtitleFile)
		inputCount++
	}
	chapterMetaIdx := -1
	if opts.DynAdChapterMeta != "" {
		args = append(args, "-i", opts.DynAdChapterMeta)
		chapterMetaIdx = inputCount
		inputCount++
	}
	if useBumper {
		args = append(args, "-i", opts.DynAdBumperPath)
	}
	if isBitmapSub || opts.web != nil || useBumper || useBumperAudio {
		args = append(args, "-filter_complex", filterComplex)
	} else {
		args = append(args, "-vf", vf)
	}
	args = append(args, encArgs...)

	abr2 := opts.AudioBitrate
	if abr2 == "" {
		abr2 = "192k"
	}
	if opts.NormalizeAudio || opts.AudioDelay != 0 || len(audioPads) > 0 {
		allowLossless := len(audioPads) > 0 && isMKVPath(outputPath)
		args = append(args, audioReencodeArgs(info, opts.AudioTrackIndex, allowLossless, abr2)...)
		if afJoined != "" {
			args = append(args, "-af", afJoined)
		}
	} else {
		args = append(args, "-c:a", "copy")
	}

	if opts.TenBit && !qsvHDR && opts.Encoder != "nvenc" && opts.Encoder != "amf" {
		args = append(args, "-pix_fmt", "yuv420p10le")
	}

	if opts.HDRPassthrough && !qsvHDR {
		primaries, transfer, space := hdrMetaOf(info, videoFile)
		if primaries != "" {
			args = append(args, "-color_primaries", primaries, "-color_trc", transfer, "-colorspace", space)
		}
		if isH265 && opts.Encoder == "cpu" && primaries == "bt2020" {
			args = append(args, "-x265-params",
				"hdr-opt=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc")
		}
	}

	chapterMap := "0"
	switch {
	case chapterMetaIdx >= 0:
		chapterMap = strconv.Itoa(chapterMetaIdx)
	case opts.DynAdHeadPad > 0:
		chapterMap = "-1"
	}

	videoMap := videoStreamLabel(info)
	if isBitmapSub || useBumper || (useBumperAudio && opts.web == nil) {
		videoMap = "[vout]"
	}
	if opts.web != nil {
		videoMap = "[vmaster]"
	}
	args = append(args, "-map", videoMap, "-map", audioMap)
	for _, m := range extraAudioMaps {
		args = append(args, "-map", m)
	}
	args = append(args, "-map_chapters", chapterMap)
	if isBitmapSub {
		if info != nil && info.Duration > 0 {
			args = append(args, "-t", fmt.Sprintf("%.3f", info.Duration+opts.DynAdHeadPad+opts.DynAdTailPad))
		}
		args = append(args, "-fps_mode", "passthrough")
	}
	args = append(args, "-map_metadata", "-1")
	args = append(args, brandMetadataArgs()...)
	if opts.ExtraArgs != "" {
		args = append(args, parseShellArgs(opts.ExtraArgs)...)
	}
	args = append(args, mp4FaststartArgs(outputPath)...)
	args = append(args, outputPath)

	if opts.web != nil {
		bitmapDur := 0.0
		if isBitmapSub && info != nil && info.Duration > 0 {
			bitmapDur = info.Duration + opts.DynAdHeadPad + opts.DynAdTailPad
		}
		args = appendWebOutput(args, opts.web, webAudioMap, afJoined, chapterMap, bitmapDur, webTonemap != "" || needHDR2SDR)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = filepath.Dir(subtitleFile)
	hideWindow(cmd)
	return cmd, tempFile
}

func parseShellArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQ := false
	var qc byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inQ {
			if c == qc {
				inQ = false
			} else {
				cur.WriteByte(c)
			}
		} else if c == '"' || c == '\'' {
			inQ = true
			qc = c
		} else if c == ' ' || c == '\t' {
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		} else {
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

func nvencMaxrate(outputHeight int, outputFPS float64, isH265 bool, sourceBitrateKbps int) (string, string) {
	var resCap int
	switch {
	case outputHeight >= 2160:
		resCap = 14000
	case outputHeight >= 1440:
		resCap = 10000
	case outputHeight >= 1080 || outputHeight == 0:
		resCap = 8000
	case outputHeight >= 720:
		resCap = 5000
	default:
		resCap = 3000
	}

	switch {
	case outputFPS > 45:
		resCap = resCap * 7 / 5
	case outputFPS > 34:
		resCap = resCap * 6 / 5
	}

	if !isH265 {
		resCap = resCap * 3 / 2
	}

	maxKbps := resCap

	if sourceBitrateKbps > 0 {
		factor := 0.55
		if !isH265 {
			factor = 0.75
		}
		if srcCap := int(float64(sourceBitrateKbps) * factor); srcCap < maxKbps {
			maxKbps = srcCap
		}
		floor := resCap / 4
		if hardFloor := sourceBitrateKbps * 9 / 10; floor > hardFloor {
			floor = hardFloor
		}
		if maxKbps < floor {
			maxKbps = floor
		}
	}

	maxrate := fmt.Sprintf("%dk", maxKbps)
	bufsize := fmt.Sprintf("%dk", maxKbps*2)
	return maxrate, bufsize
}

var (
	nvencTuneMu    sync.Mutex
	nvencTuneCache = map[string]bool{}
)

func nvencSupportsTune(encoder, tune string) bool {
	key := encoder + "/" + tune
	nvencTuneMu.Lock()
	if v, ok := nvencTuneCache[key]; ok {
		nvencTuneMu.Unlock()
		return v
	}
	nvencTuneMu.Unlock()

	cmd := exec.Command("ffmpeg", "-hide_banner", "-h", "encoder="+encoder)
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput()

	found := false
	inTune := false
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-tune") {
			inTune = true
			continue
		}
		if !inTune {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "-") {
			break
		}
		if fields := strings.Fields(trimmed); len(fields) > 0 && fields[0] == tune {
			found = true
			break
		}
	}

	nvencTuneMu.Lock()
	nvencTuneCache[key] = found
	nvencTuneMu.Unlock()
	return found
}

func nvencTuneArgs(encoder string) []string {
	if nvencSupportsTune(encoder, "uhq") {
		return []string{"-tune", "uhq"}
	}
	return []string{"-tune", "hq"}
}

func gopForFPS(fps float64) int {
	if fps <= 0 || fps > 240 {
		return 48
	}
	g := int(fps*2 + 0.5)
	if g < 24 {
		g = 24
	}
	if g > 120 {
		g = 120
	}
	return g
}

func nvencPreset(p string) string {
	switch p {
	case "faster":
		return "p2"
	case "fast":
		return "p3"
	case "medium":
		return "p4"
	case "slow":
		return "p5"
	case "slower":
		return "p6"
	case "veryslow":
		return "p7"
	default:
		return "p5"
	}
}

func amfQuality(p string) string {
	switch p {
	case "faster", "fast":
		return "speed"
	case "slower", "veryslow":
		return "quality"
	default:
		return "balanced"
	}
}

func qsvPreset(p string) string {
	switch p {
	case "faster", "fast":
		return "fast"
	case "slower", "veryslow":
		return "slow"
	default:
		return "medium"
	}
}

func svtav1Preset(p string) string {
	switch p {
	case "veryslow":
		return "2"
	case "slower":
		return "3"
	case "slow":
		return "4"
	case "medium":
		return "6"
	case "fast":
		return "8"
	case "faster":
		return "10"
	default:
		return "6"
	}
}

func (a *App) ConvertSubtitle(inputFile, outputDir string) bool {
	if inputFile == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	ext := strings.ToLower(filepath.Ext(inputFile))
	stem := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))

	var outExt string
	switch ext {
	case ".ass", ".ssa":
		outExt = ".srt"
	case ".srt":
		outExt = ".ass"
	case ".vtt":
		outExt = ".srt"
	case ".sub":
		outExt = ".srt"
	case ".ttml":
		outExt = ".srt"
	default:
		a.log("error", "字幕格式不支持: "+ext)
		return false
	}

	outputFile := filepath.Join(outputDir, stem+outExt)
	a.log("info", fmt.Sprintf("字幕转换: %s → %s", ext, outExt))

	cmd := exec.Command("ffmpeg", "-y", "-i", inputFile, outputFile)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		a.log("error", "转换失败: "+friendlyErrorMsg(decodeGBK(out), "", false))
		return false
	}

	if outExt == ".srt" {
		cleanSRTFile(outputFile)
	}

	a.log("ok", "转换 → "+filepath.Base(outputFile))
	return true
}

func (a *App) ExtractAudio(inputDir, outputDir, format string) bool {
	videoFiles := findFiles(inputDir, videoExts)
	if len(videoFiles) == 0 {
		a.log("error", "未找到视频文件")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	videoFile := videoFiles[0]
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))

	extMap := map[string]string{"flac": ".flac", "aac": ".aac", "mp3": ".mp3"}
	outExt, ok := extMap[strings.ToLower(format)]
	if !ok {
		outExt = ".aac"
		format = "aac"
	}

	outputFile := filepath.Join(outputDir, stem+outExt)
	a.log("info", fmt.Sprintf("提取音频: %s → %s", filepath.Base(videoFile), format))

	var cmd *exec.Cmd
	if format == "flac" {
		cmd = exec.Command("ffmpeg", "-y", "-i", videoFile, "-map", "0:a:0", "-c:a", "flac", outputFile)
	} else if format == "mp3" {
		cmd = exec.Command("ffmpeg", "-y", "-i", videoFile, "-map", "0:a:0", "-c:a", "libmp3lame", "-q:a", "2", outputFile)
	} else {
		cmd = exec.Command("ffmpeg", "-y", "-i", videoFile, "-map", "0:a:0", "-c:a", "copy", outputFile)
	}
	hideWindow(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		errStr := decodeGBK(out)
		if len(errStr) > 300 {
			errStr = errStr[len(errStr)-300:]
		}
		a.log("error", "提取失败: "+errStr)
		return false
	}

	a.log("ok", "提取 → "+filepath.Base(outputFile))
	return true
}

var tradTitleMarkers = []string{
	"繁体", "繁體", "繁中", "正体", "正體",
	"traditional", "chinese traditional", "t.chinese",
	"cht", "(cht)", "big5", "hant",
	" tc", "(tc)", "[tc]",
}

func isSimplifiedChinese(lang, title string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	title = strings.ToLower(strings.TrimSpace(title))

	ambiguousGenericLang := lang == "chi" || lang == "zh" || lang == "zho" || lang == "chinese"
	if ambiguousGenericLang {
		for _, m := range tradTitleMarkers {
			if strings.Contains(title, m) {
				return false
			}
		}
		if title == "tc" || title == "cht" || title == "traditional" || title == "hant" {
			return false
		}
	}

	if strings.HasPrefix(lang, "zh-hans") {
		return true
	}
	if lang == "zh-cn" || lang == "zh-sg" {
		return true
	}
	simpLangs := []string{"chi", "zh", "chs", "sc", "zhs", "chn", "gb", "chinese", "simp", "zh_cn", "zh_sg", "zho"}
	for _, l := range simpLangs {
		if lang == l {
			return true
		}
	}
	simpKeywords := []string{"简体", "简中", "simplified", " sc", "(sc)", "[sc]", "chs", "(chs)", " gb", "(gb)", "hans", "s.chinese", "chinese simplified"}
	for _, k := range simpKeywords {
		if strings.Contains(title, k) {
			return true
		}
	}
	simpPrefixes := []string{"sc ", "chs ", "gb "}
	simpSuffixes := []string{" sc", " chs", " gb"}
	for _, p := range simpPrefixes {
		if strings.HasPrefix(title, p) {
			return true
		}
	}
	for _, s := range simpSuffixes {
		if strings.HasSuffix(title, s) {
			return true
		}
	}
	if title == "sc" || title == "chs" || title == "gb" || title == "simplified" || title == "hans" {
		return true
	}
	return false
}

func isTraditionalChinese(lang, title string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	title = strings.ToLower(strings.TrimSpace(title))

	if strings.HasPrefix(lang, "zh-hant") {
		return true
	}
	if lang == "zh-tw" || lang == "zh-hk" || lang == "zh-mo" {
		return true
	}
	tradLangs := []string{"cht", "tc", "zht", "big5", "hk", "tw", "mo", "trad", "zh_tw", "zh_hk", "zh_mo"}
	for _, l := range tradLangs {
		if lang == l {
			return true
		}
	}
	for _, k := range tradTitleMarkers {
		if strings.Contains(title, k) {
			return true
		}
	}
	tradPrefixes := []string{"tc ", "cht ", "tw ", "hk "}
	tradSuffixes := []string{" tc", " cht", " tw", " hk"}
	for _, p := range tradPrefixes {
		if strings.HasPrefix(title, p) {
			return true
		}
	}
	for _, s := range tradSuffixes {
		if strings.HasSuffix(title, s) {
			return true
		}
	}
	if title == "tc" || title == "cht" || title == "traditional" || title == "hant" || title == "tw" || title == "hk" || title == "mo" {
		return true
	}
	return false
}

func isEnglish(lang, title string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	title = strings.ToLower(strings.TrimSpace(title))
	engLangs := []string{"eng", "en", "en-us", "en-gb", "en-au", "en-ca", "english"}
	for _, l := range engLangs {
		if lang == l {
			return true
		}
	}
	if strings.HasPrefix(lang, "en-") {
		return true
	}
	engKeywords := []string{"english", "英语", "英文"}
	for _, k := range engKeywords {
		if strings.Contains(title, k) || title == k {
			return true
		}
	}
	return false
}

func (a *App) ExtractSubtitles(inputDir, outputDir string) bool {
	videoFiles := findFilesRecursive(inputDir, videoExts)
	if len(videoFiles) == 0 {
		a.log("error", "未找到视频文件")
		return false
	}
	videoFiles = dedupBasenames(videoFiles, func(kept, dropped string) {
		a.log("warn", "同名跳过: "+filepath.Base(dropped))
	})

	saveDir := strings.TrimSpace(outputDir)
	if saveDir == "" {
		saveDir = inputDir
	}
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		a.log("error", "创建目录失败")
		return false
	}

	total := len(videoFiles)
	successCount := 0
	for idx, videoFile := range videoFiles {
		if total > 1 {
			a.log("info", fmt.Sprintf("── [%d/%d] %s", idx+1, total, filepath.Base(videoFile)))
		} else {
			a.log("info", "提取字幕: "+filepath.Base(videoFile))
		}

		if ok := a.extractOneSubtitle(videoFile, saveDir); ok {
			successCount++
		}
	}

	if total > 1 {
		a.log("ok", fmt.Sprintf("提取完成 %d/%d", successCount, total))
	}
	a.PlaySoundExtractDone()
	return successCount > 0
}

type subTrack struct {
	subIndex    int
	streamIndex int
	codec       string
	lang        string
	title       string
}

func isTextSubCodec(codec string) bool {
	switch codec {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text":
		return true
	default:
		return false
	}
}

func firstTextSubTrack(tracks []subTrack) *subTrack {
	for i := range tracks {
		if isTextSubCodec(tracks[i].codec) {
			return &tracks[i]
		}
	}
	return nil
}

func entriesAreBilingual(entries []SubtitleEntry) bool {
	bi, total := 0, 0
	for _, e := range entries {
		zh, en := false, false
		for _, line := range splitRawSubtitleLines(e.Text) {
			t := strings.TrimSpace(line)
			if t == "" {
				continue
			}
			if looksForeignSubtitle(t) {
				en = true
			} else {
				zh = true
			}
		}
		if !zh && !en {
			continue
		}
		total++
		if zh && en {
			bi++
		}
	}
	return total > 0 && bi*100/total >= 25
}

func probeTrackBilingual(videoFile string, streamIndex int) bool {
	cmd := exec.Command("ffmpeg", "-v", "quiet", "-i", videoFile,
		"-map", fmt.Sprintf("0:%d", streamIndex), "-c:s", "srt", "-f", "srt", "-t", "300", "pipe:1")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return false
	}
	entries := parseSRTEntries(decodeTextFile(out))
	if len(entries) > 120 {
		entries = entries[:120]
	}
	return entriesAreBilingual(entries)
}

func firstBilingualTrack(videoFile string, tracks []subTrack) *subTrack {
	for i := range tracks {
		if !isTextSubCodec(tracks[i].codec) {
			continue
		}
		if probeTrackBilingual(videoFile, tracks[i].streamIndex) {
			return &tracks[i]
		}
	}
	return nil
}

func (a *App) extractOneSubtitle(videoFile, saveDir string) bool {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_streams", videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		a.log("error", "无法读取视频信息")
		return false
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		a.log("error", "解析失败")
		return false
	}

	streams, _ := raw["streams"].([]interface{})
	var allTracks []subTrack
	var simpTracks []subTrack
	var tradTracks []subTrack
	var engTracks []subTrack

	for _, s := range streams {
		stream, ok := s.(map[string]interface{})
		if !ok || stream["codec_type"] != "subtitle" {
			continue
		}
		lang, title := "", ""
		if tags, ok := stream["tags"].(map[string]interface{}); ok {
			lang = fmt.Sprintf("%v", tags["language"])
			title = fmt.Sprintf("%v", tags["title"])
		}
		streamIndex := 0
		if idx, ok := stream["index"].(float64); ok {
			streamIndex = int(idx)
		}
		track := subTrack{
			subIndex:    len(allTracks),
			streamIndex: streamIndex,
			codec:       fmt.Sprintf("%v", stream["codec_name"]),
			lang:        lang,
			title:       title,
		}
		allTracks = append(allTracks, track)
		if isSimplifiedChinese(lang, title) {
			simpTracks = append(simpTracks, track)
		} else if isTraditionalChinese(lang, title) {
			tradTracks = append(tradTracks, track)
		} else if isEnglish(lang, title) {
			engTracks = append(engTracks, track)
		}
	}

	if len(allTracks) == 0 {
		a.log("warn", "该视频没有内嵌字幕轨")
		return false
	}

	simplified := firstTextSubTrack(simpTracks)
	traditional := firstTextSubTrack(tradTracks)
	zhTrack := simplified
	if zhTrack == nil {
		zhTrack = traditional
	}
	enTrack := firstTextSubTrack(engTracks)

	bilingualTrack := firstBilingualTrack(videoFile, simpTracks)
	if bilingualTrack == nil && simplified == nil {
		bilingualTrack = firstBilingualTrack(videoFile, tradTracks)
	}
	if bilingualTrack == nil && zhTrack != nil && enTrack != nil {
		if a.extractBilingualMerged(videoFile, saveDir, *zhTrack, *enTrack) {
			return true
		}
		a.log("warn", "双语合并失败，回退为提取单条字幕")
	}

	var target *subTrack
	var pickReason string
	switch {
	case bilingualTrack != nil:
		target = bilingualTrack
		pickReason = "中英双语"
	case simplified != nil:
		target = simplified
		pickReason = "简体"
	case traditional != nil:
		target = traditional
		pickReason = "繁体"
	case enTrack != nil:
		target = enTrack
		pickReason = "英文"
	case firstTextSubTrack(allTracks) != nil:
		target = firstTextSubTrack(allTracks)
		pickReason = "首条文本"
	default:
		target = &allTracks[0]
		pickReason = "首条图形"
	}
	a.log("info", fmt.Sprintf("轨 %d / 流 #%d / %s / %s", target.subIndex, target.streamIndex, target.codec, pickReason))

	ext := codecToExt(target.codec)
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	outName := stem + ext
	if ext == ".mks" || ext == ".mkv" {
		outName = stem + ".subtitle" + ext
	}
	outFile := filepath.Join(saveDir, outName)

	subCodec := "copy"
	containerFormat := ""
	if target.codec == "mov_text" && ext == ".srt" {
		subCodec = "srt"
	}
	if ext == ".mks" {
		containerFormat = "matroska"
	}

	args := []string{"-y", "-i", videoFile,
		"-map", fmt.Sprintf("0:%d", target.streamIndex),
		"-c:s", subCodec}
	if containerFormat != "" {
		args = append(args, "-f", containerFormat)
	}
	args = append(args, outFile)
	cmd = exec.Command("ffmpeg", args...)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()

	if err != nil {
		decoded := decodeGBK(output)
		a.log("error", "提取失败: "+friendlySubtitleExtractError(decoded, target.codec, ext))
		return false
	}

	a.log("ok", "提取 → "+filepath.Base(outFile))

	fileExt := strings.ToLower(filepath.Ext(outFile))
	if fileExt == ".srt" || fileExt == ".ass" || fileExt == ".ssa" || fileExt == ".vtt" {
		a.cleanExtractedSubtitle(outFile)
	}

	return true
}

func extractSubTrackToSRT(videoFile string, streamIndex int, dest string) error {
	cmd := exec.Command("ffmpeg", "-y", "-i", videoFile,
		"-map", fmt.Sprintf("0:%d", streamIndex),
		"-c:s", "srt", dest)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(decodeGBK(out)))
	}
	return nil
}

func (a *App) extractBilingualMerged(videoFile, saveDir string, zhTrack, enTrack subTrack) bool {
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	now := time.Now().UnixNano()
	tmpZh := filepath.Join(saveDir, fmt.Sprintf("muxer_bi_zh_%d.srt", now))
	tmpEn := filepath.Join(saveDir, fmt.Sprintf("muxer_bi_en_%d.srt", now+1))
	defer os.Remove(tmpZh)
	defer os.Remove(tmpEn)

	if err := extractSubTrackToSRT(videoFile, zhTrack.streamIndex, tmpZh); err != nil {
		a.log("warn", "提取中文字幕轨失败: "+err.Error())
		return false
	}
	if err := extractSubTrackToSRT(videoFile, enTrack.streamIndex, tmpEn); err != nil {
		a.log("warn", "提取英文字幕轨失败: "+err.Error())
		return false
	}

	zhData, err1 := os.ReadFile(tmpZh)
	enData, err2 := os.ReadFile(tmpEn)
	if err1 != nil || err2 != nil {
		return false
	}
	zhEntries := parseSRTEntries(decodeTextFile(zhData))
	enEntries := parseSRTEntries(decodeTextFile(enData))
	if len(zhEntries) == 0 {
		a.log("warn", "中文字幕轨内容为空，无法合并双语")
		return false
	}
	if len(enEntries) == 0 {
		a.log("warn", "英文字幕轨内容为空，无法合并双语")
		return false
	}

	merged := mergeBilingualSRT(zhEntries, enEntries)
	if strings.TrimSpace(merged) == "" {
		return false
	}

	outFile := filepath.Join(saveDir, stem+".srt")
	if err := os.WriteFile(outFile, []byte(merged), 0644); err != nil {
		a.log("error", "写入双语字幕失败: "+err.Error())
		return false
	}
	a.log("ok", fmt.Sprintf("双语合并 中#%d + 英#%d", zhTrack.streamIndex, enTrack.streamIndex))
	a.cleanExtractedSubtitle(outFile)
	return true
}

func mergeBilingualSRT(zhEntries, enEntries []SubtitleEntry) string {
	type span struct {
		start, end float64
		text       string
	}
	ens := make([]span, 0, len(enEntries))
	for _, e := range enEntries {
		t := strings.TrimSpace(strings.ReplaceAll(e.Text, "\n", " "))
		if t == "" {
			continue
		}
		s, en := parseClockSec(e.Start), parseClockSec(e.End)
		if en < s {
			s, en = en, s
		}
		ens = append(ens, span{s, en, t})
	}

	var sb strings.Builder
	idx := 0
	for _, z := range zhEntries {
		zhText := strings.TrimSpace(z.Text)
		if zhText == "" {
			continue
		}
		zs, ze := parseClockSec(z.Start), parseClockSec(z.End)
		if ze < zs {
			zs, ze = ze, zs
		}
		var zhParts, enParts []string
		seen := make(map[string]bool, len(ens)+2)
		addEN := func(t string) {
			key := lineKey(t)
			if key == "" || seen[key] {
				return
			}
			seen[key] = true
			enParts = append(enParts, t)
		}
		for _, ln := range splitRawSubtitleLines(zhText) {
			t := strings.TrimSpace(ln)
			if t == "" {
				continue
			}
			if looksForeignSubtitle(t) {
				addEN(t)
				continue
			}
			zhParts = append(zhParts, t)
		}
		for _, en := range ens {
			ov := math.Min(ze, en.end) - math.Max(zs, en.start)
			if ov <= 0 {
				continue
			}
			dur := en.end - en.start
			if dur > 0 && ov < 0.5*dur {
				continue
			}
			addEN(en.text)
		}
		idx++
		sb.WriteString(formatSRTEntry(idx, zs, ze, joinBilingual(strings.Join(zhParts, "\n"), strings.Join(enParts, " "))))
	}
	return strings.TrimLeft(sb.String(), "\n")
}

func formatSRTTimeLine(start, end float64, text string) string {
	return fmt.Sprintf("%s --> %s\n%s",
		msToSubTime(secToSRTMs(start), "srt"), msToSubTime(secToSRTMs(end), "srt"), text)
}

func formatSRTEntry(idx int, start, end float64, text string) string {
	return fmt.Sprintf("\n%d\n%s\n", idx, formatSRTTimeLine(start, end, text))
}

func (a *App) cleanExtractedSubtitle(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	content := strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n")

	ext := strings.ToLower(filepath.Ext(filePath))
	isASS := ext == ".ass" || ext == ".ssa"

	var assHeader string
	var entries []SubtitleEntry
	if isASS {
		entries = parseASSEntries(content)
		idx := strings.Index(content, "\nDialogue:")
		if idx < 0 {
			idx = strings.Index(content, "Dialogue:")
		}
		if idx > 0 {
			assHeader = content[:idx+1]
		}
	} else if ext == ".vtt" {
		entries = parseVTTEntries(content)
	} else {
		entries = parseSRTEntries(content)
	}

	if len(entries) == 0 {
		return
	}

	origCount := len(entries)
	var stats cleanStats

	for i := range entries {
		text := entries[i].Text
		before := text
		text = reHTMLTag.ReplaceAllString(text, "")
		if !isASS {
			text = reASSOverride.ReplaceAllString(text, "")
		}
		text = strings.TrimSpace(text)
		if text != before {
			entries[i].Text = text
			stats.tagsStripped++
		}
	}

	for i := range entries {
		text := entries[i].Text
		before := text
		text = reHIBracket.ReplaceAllString(text, "")
		text = reHIParen.ReplaceAllString(text, "")
		text = reHIFullParen.ReplaceAllString(text, "")
		text = reHIMusic.ReplaceAllString(text, "")
		text = reHIColon.ReplaceAllString(text, "")
		lines := strings.Split(text, "\n")
		var cleaned []string
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				cleaned = append(cleaned, l)
			}
		}
		text = strings.Join(cleaned, "\n")
		if text != before {
			entries[i].Text = text
			stats.hiRemoved++
		}
	}

	adKeywords := []string{
		"opensubtitles", "subscene", "addic7ed", "podnapisi",
		"subtitles by", "sync by", "synced by", "corrected by",
		"translated by", "caption by", "captioned by", "ripped by",
		"encoded by", "downloaded from", "brought to you",
		"字幕组", "字幕网", "字幕站", "翻译:", "翻译：", "校对:", "校对：",
		"时间轴:", "时间轴：", "后期:", "后期：", "压制:", "压制：",
	}
	for i := range entries {
		text := entries[i].Text
		before := text
		text = stripContactInfo(text)
		lower := strings.ToLower(text)
		isAd := false
		for _, kw := range adKeywords {
			if strings.Contains(lower, kw) {
				isAd = true
				break
			}
		}
		if isAd {
			entries[i].Text = ""
			stats.adsRemoved++
		} else {
			text = strings.TrimSpace(text)
			if text != before {
				entries[i].Text = text
				stats.adsRemoved++
			}
		}
	}

	for i := range entries {
		cleaned := dedupeEntryLines(entries[i].Text)
		if cleaned != entries[i].Text {
			entries[i].Text = cleaned
			stats.linesDeduped++
		}
	}

	var nonEmpty []SubtitleEntry
	for _, e := range entries {
		if strings.TrimSpace(e.Text) != "" {
			nonEmpty = append(nonEmpty, e)
		} else {
			stats.emptyRemoved++
		}
	}
	entries = nonEmpty

	for i := range entries {
		startMs := subTimeToMs(entries[i].Start, entries[i].Format)
		endMs := subTimeToMs(entries[i].End, entries[i].Format)
		if endMs < startMs {
			entries[i].Start, entries[i].End = entries[i].End, entries[i].Start
			stats.negDurFixed++
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return subTimeToMs(entries[i].Start, entries[i].Format) < subTimeToMs(entries[j].Start, entries[j].Format)
	})

	if len(entries) > 1 {
		var deduped []SubtitleEntry
		deduped = append(deduped, entries[0])
		for i := 1; i < len(entries); i++ {
			prev := deduped[len(deduped)-1]
			cur := entries[i]
			if cur.Start == prev.Start && cur.End == prev.End &&
				normalizeText(cur.Text) == normalizeText(prev.Text) {
				stats.exactDupes++
				continue
			}
			deduped = append(deduped, cur)
		}
		entries = deduped
	}

	if len(entries) > 1 {
		var merged []SubtitleEntry
		merged = append(merged, entries[0])
		for i := 1; i < len(entries); i++ {
			prev := &merged[len(merged)-1]
			cur := entries[i]
			prevEnd := subTimeToMs(prev.End, prev.Format)
			curStart := subTimeToMs(cur.Start, cur.Format)
			gap := curStart - prevEnd
			if gap < 0 {
				gap = -gap
			}
			if gap <= 500 && normalizeText(cur.Text) == normalizeText(prev.Text) {
				curEnd := subTimeToMs(cur.End, cur.Format)
				if curEnd > prevEnd {
					prev.End = cur.End
				}
				stats.nearDupes++
				continue
			}
			merged = append(merged, cur)
		}
		entries = merged
	}

	for i := 0; i+1 < len(entries); i++ {
		curEnd := subTimeToMs(entries[i].End, entries[i].Format)
		nextStart := subTimeToMs(entries[i+1].Start, entries[i+1].Format)
		if curEnd > nextStart {
			entries[i].End = entries[i+1].Start
			stats.overlapsFixed++
		}
	}

	var filtered []SubtitleEntry
	for _, e := range entries {
		dur := subTimeToMs(e.End, e.Format) - subTimeToMs(e.Start, e.Format)
		if dur >= 100 {
			filtered = append(filtered, e)
		} else {
			stats.shortRemoved++
		}
	}
	entries = filtered

	if len(entries) == 0 {
		a.log("warn", "清理后字幕为空，保留原文件")
		return
	}

	for i := range entries {
		entries[i].Index = i + 1
	}

	var output string
	if isASS && assHeader != "" {
		var sb strings.Builder
		sb.WriteString(assHeader)
		for _, e := range entries {
			if len(e.AssParts) >= 7 {
				fmt.Fprintf(&sb, "Dialogue: %s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
					e.AssParts[0], e.Start, e.End,
					e.AssParts[1], e.AssParts[2], e.AssParts[3],
					e.AssParts[4], e.AssParts[5], e.AssParts[6], e.Text)
			}
		}
		output = sb.String()
	} else if ext == ".vtt" {
		var sb strings.Builder
		sb.WriteString("WEBVTT\n\n")
		for i, e := range entries {
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
		}
		output = sb.String()
	} else {
		var sb strings.Builder
		for i, e := range entries {
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
		}
		output = sb.String()
	}

	bom := "\xEF\xBB\xBF"
	if err := os.WriteFile(filePath, []byte(bom+output), 0644); err != nil {
		a.log("warn", "字幕清理写回失败: "+err.Error())
		return
	}

	totalFixed := stats.tagsStripped + stats.hiRemoved + stats.adsRemoved +
		stats.emptyRemoved + stats.negDurFixed + stats.exactDupes +
		stats.nearDupes + stats.overlapsFixed + stats.shortRemoved + stats.linesDeduped

	if totalFixed == 0 {
		return
	}

	var parts []string
	if stats.exactDupes > 0 {
		parts = append(parts, fmt.Sprintf("去重%d条", stats.exactDupes))
	}
	if stats.nearDupes > 0 {
		parts = append(parts, fmt.Sprintf("合并近似%d条", stats.nearDupes))
	}
	if stats.overlapsFixed > 0 {
		parts = append(parts, fmt.Sprintf("修重叠%d处", stats.overlapsFixed))
	}
	if stats.tagsStripped > 0 {
		parts = append(parts, fmt.Sprintf("去标签%d条", stats.tagsStripped))
	}
	if stats.hiRemoved > 0 {
		parts = append(parts, fmt.Sprintf("去HI标注%d条", stats.hiRemoved))
	}
	if stats.adsRemoved > 0 {
		parts = append(parts, fmt.Sprintf("去广告%d条", stats.adsRemoved))
	}
	if stats.emptyRemoved > 0 {
		parts = append(parts, fmt.Sprintf("去空%d条", stats.emptyRemoved))
	}
	if stats.shortRemoved > 0 {
		parts = append(parts, fmt.Sprintf("去超短%d条", stats.shortRemoved))
	}
	if stats.negDurFixed > 0 {
		parts = append(parts, fmt.Sprintf("修负时长%d条", stats.negDurFixed))
	}
	if stats.linesDeduped > 0 {
		parts = append(parts, fmt.Sprintf("条目内去重%d行", stats.linesDeduped))
	}

	a.log("info", fmt.Sprintf("字幕清理 %d→%d · %s",
		origCount, len(entries), strings.Join(parts, "、")))
}

type cleanStats struct {
	tagsStripped  int
	hiRemoved     int
	adsRemoved    int
	emptyRemoved  int
	negDurFixed   int
	exactDupes    int
	nearDupes     int
	overlapsFixed int
	shortRemoved  int
	linesDeduped  int
}

func normalizeText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func (a *App) preprocessSubtitleForMux(subPath string) string {
	ext := strings.ToLower(filepath.Ext(subPath))
	if ext == ".sup" || ext == ".idx" || ext == ".mks" {
		return ""
	}
	cleaned, n, ok := a.buildCleanSRT(subPath)
	if !ok || n == 0 {
		return ""
	}
	safeStem := sanitizeTmpName(filepath.Base(subPath))
	dir := filepath.Dir(subPath)
	if assContent, isBi := buildBilingualASS(cleaned); isBi {
		assPath := filepath.Join(dir, safeStem+".muxer-clean.ass")
		if err := os.WriteFile(assPath, []byte("\xEF\xBB\xBF"+assContent), 0644); err != nil {
			a.log("warn", "双语ASS写入失败，改用简洁SRT: "+err.Error())
		} else {
			a.log("info", fmt.Sprintf("双语字幕 %d 条", n))
			return assPath
		}
	}
	if ext == ".srt" {
		if origBytes, err := os.ReadFile(subPath); err == nil {
			if normalizeSubtitleBytes(origBytes) == normalizeSubtitleBytes([]byte(cleaned)) {
				return ""
			}
		}
	}
	tmpPath := filepath.Join(dir, safeStem+".muxer-clean.srt")
	bom := "\xEF\xBB\xBF"
	if err := os.WriteFile(tmpPath, []byte(bom+cleaned), 0644); err != nil {
		a.log("warn", "字幕写入失败: "+err.Error())
		return ""
	}
	a.log("info", fmt.Sprintf("字幕清理 %d 条", n))
	return tmpPath
}

func normalizeSubtitleBytes(b []byte) string {
	s := decodeTextFile(b)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

func (a *App) filterToChineseSubtitles(subs []string) []string {
	if len(subs) <= 1 {
		return subs
	}
	type cand struct {
		path string
		cjk  int
	}
	var chinese []cand
	var bitmap []string
	var nonChinese []string
	for _, p := range subs {
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".sup" || ext == ".idx" || ext == ".mks" {
			bitmap = append(bitmap, p)
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			chinese = append(chinese, cand{path: p})
			continue
		}
		s := decodeTextFile(data)
		cjk, total := 0, 0
		for _, r := range s {
			if !unicode.IsSpace(r) {
				total++
				if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) {
					cjk++
				}
			}
		}
		if total > 0 && cjk*100/total >= 10 {
			chinese = append(chinese, cand{path: p, cjk: cjk})
		} else {
			nonChinese = append(nonChinese, p)
		}
	}
	if len(chinese) == 0 {
		return subs
	}
	picked := 0
	for i := 1; i < len(chinese); i++ {
		if chinese[i].cjk > chinese[picked].cjk {
			picked = i
		}
	}
	chosen := chinese[picked].path
	result := append([]string{chosen}, bitmap...)
	var dropped []string
	for i, c := range chinese {
		if i == picked {
			continue
		}
		dropped = append(dropped, filepath.Base(c.path))
	}
	for _, p := range nonChinese {
		dropped = append(dropped, filepath.Base(p))
	}
	if len(dropped) > 0 {
		a.log("warn", fmt.Sprintf("字幕取 %s，丢弃 %d 条: %s",
			filepath.Base(chosen), len(dropped), strings.Join(dropped, "、")))
	}
	return result
}

func (a *App) validateSubtitleForHardsub(subtitleFile string, videoDuration float64) (fixedPath string, fatalErr string) {
	ext := strings.ToLower(filepath.Ext(subtitleFile))

	if ext == ".sup" || ext == ".idx" || ext == ".mks" {
		if fi, err := os.Stat(subtitleFile); err != nil {
			return "", "字幕文件无法读取: " + err.Error()
		} else if fi.Size() == 0 {
			return "", "字幕文件为空"
		}
		return "", ""
	}

	data, err := os.ReadFile(subtitleFile)
	if err != nil {
		return "", "字幕文件无法读取: " + err.Error()
	}
	if len(data) == 0 {
		return "", "字幕文件为空"
	}

	isASS := ext == ".ass" || ext == ".ssa"

	cleaned := make([]byte, 0, len(data))
	hasNUL := false
	for _, b := range data {
		if b == 0x00 {
			hasNUL = true
			continue
		}
		if b < 0x09 || (b > 0x0D && b < 0x20 && b != 0x1B) {
			hasNUL = true
			continue
		}
		cleaned = append(cleaned, b)
	}
	if hasNUL {
		data = cleaned
	}

	content := strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n")

	if isASS {
		if !strings.Contains(content, "[Events]") {
			return "", "字幕文件缺少 [Events] 段，FFmpeg 无法识别字幕内容"
		}
		if !strings.Contains(content, "Dialogue:") {
			return "", "字幕文件中无 Dialogue 行，硬压后将无字幕显示"
		}
	}

	var entries []SubtitleEntry
	if isASS {
		entries = parseASSEntries(content)
	} else if ext == ".vtt" {
		entries = parseVTTEntries(content)
	} else {
		entries = parseSRTEntries(content)
	}
	if len(entries) == 0 {
		return "", "字幕文件解析后无有效条目"
	}

	videoDurMs := int(videoDuration * 1000)
	fixCount := 0

	for i := range entries {
		startMs := subTimeToMs(entries[i].Start, entries[i].Format)
		endMs := subTimeToMs(entries[i].End, entries[i].Format)

		if endMs < startMs {
			entries[i].Start, entries[i].End = entries[i].End, entries[i].Start
			fixCount++
		}

		if videoDurMs > 0 {
			startMs = subTimeToMs(entries[i].Start, entries[i].Format)
			endMs = subTimeToMs(entries[i].End, entries[i].Format)
			if endMs > videoDurMs+5000 {
				entries[i].End = msToSubTime(videoDurMs, entries[i].Format)
				fixCount++
			}
			if startMs > videoDurMs {
				entries[i].Start = msToSubTime(videoDurMs-1000, entries[i].Format)
				if subTimeToMs(entries[i].Start, entries[i].Format) < 0 {
					entries[i].Start = msToSubTime(0, entries[i].Format)
				}
				fixCount++
			}
		}
	}

	needRewrite := hasNUL || fixCount > 0
	if !needRewrite {
		return "", ""
	}

	var fixParts []string
	if hasNUL {
		fixParts = append(fixParts, "清除非法字符")
	}
	if fixCount > 0 {
		fixParts = append(fixParts, fmt.Sprintf("修复时间轴 %d 处", fixCount))
	}
	a.log("info", "字幕 "+strings.Join(fixParts, "、"))

	dir := filepath.Dir(subtitleFile)
	stem := strings.TrimSuffix(filepath.Base(subtitleFile), filepath.Ext(subtitleFile))
	tmpPath := filepath.Join(dir, sanitizeTmpName(stem)+".muxer-validated"+ext)

	var output string
	if isASS {
		idx := strings.Index(content, "\nDialogue:")
		if idx < 0 {
			idx = strings.Index(content, "Dialogue:")
		}
		var sb strings.Builder
		if idx > 0 {
			sb.WriteString(content[:idx+1])
		}
		for _, e := range entries {
			if len(e.AssParts) >= 7 {
				fmt.Fprintf(&sb, "Dialogue: %s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
					e.AssParts[0], e.Start, e.End,
					e.AssParts[1], e.AssParts[2], e.AssParts[3],
					e.AssParts[4], e.AssParts[5], e.AssParts[6], e.Text)
			}
		}
		output = sb.String()
	} else if ext == ".vtt" {
		var sb strings.Builder
		sb.WriteString("WEBVTT\n\n")
		for i, e := range entries {
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
		}
		output = sb.String()
	} else {
		var sb strings.Builder
		for i, e := range entries {
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
		}
		output = sb.String()
	}

	bom := "\xEF\xBB\xBF"
	if werr := os.WriteFile(tmpPath, []byte(bom+output), 0644); werr != nil {
		return "", "字幕修复后写入临时文件失败: " + werr.Error()
	}

	return tmpPath, ""
}

func (a *App) testSubtitleRender(videoFile, subtitleFile string, videoDuration float64) string {
	ext := strings.ToLower(filepath.Ext(subtitleFile))
	isBitmapSub := (ext == ".sup" || ext == ".idx" || ext == ".mks")

	subPathEsc := strings.ReplaceAll(filepath.ToSlash(subtitleFile), ":", "\\:")
	subPathEsc = strings.ReplaceAll(subPathEsc, "'", "\\'")
	var vf string
	if ext == ".ass" || ext == ".ssa" {
		vf = fmt.Sprintf("ass='%s'", subPathEsc) + assFontsDirOpt()
	} else if !isBitmapSub {
		vf = fmt.Sprintf("subtitles='%s'", subPathEsc)
	}

	dur := videoDuration
	if dur <= 0 {
		dur = 60
	}
	seekPoints := []float64{dur * 0.1, dur * 0.5, dur * 0.9}

	for _, ss := range seekPoints {
		if ss < 0 {
			ss = 0
		}
		var args []string
		if isBitmapSub {
			args = []string{
				"-hide_banner", "-v", "error",
				"-ss", fmt.Sprintf("%.1f", ss),
				"-i", videoFile,
				"-ss", fmt.Sprintf("%.1f", ss),
				"-i", subtitleFile,
				"-filter_complex", "[0:v][1:s:0]overlay",
				"-frames:v", "1",
				"-f", "null", "-",
			}
		} else {
			args = []string{
				"-hide_banner", "-v", "error",
				"-ss", fmt.Sprintf("%.1f", ss),
				"-i", videoFile,
				"-vf", vf,
				"-frames:v", "1",
				"-f", "null", "-",
			}
		}
		cmd := exec.Command("ffmpeg", args...)
		hideWindow(cmd)
		_, err := cmd.CombinedOutput()
		if err != nil {
			a.log("warn", fmt.Sprintf("字幕渲染失败 @ %.0fs", ss))
			return fmt.Sprintf("字幕在 %.0f 秒无法渲染", ss)
		}
	}
	return ""
}

func shouldPreRemux(videoFile string, info *videoInfo) bool {
	if info == nil {
		return false
	}
	fi, err := os.Stat(videoFile)
	if err != nil {
		return false
	}
	var sizeThreshold int64 = 20 * 1024 * 1024 * 1024
	const bitrateThreshold = 30000
	return fi.Size() >= sizeThreshold || info.VideoBitrateKbps >= bitrateThreshold
}

func hasMP4IncompatAudio(info *videoInfo) bool {
	if info == nil {
		return false
	}
	for _, c := range info.AudioCodecs {
		switch c {
		case "truehd", "dts", "dts-hd", "pcm_bluray":
			return true
		}
	}
	return false
}

func isMKVPath(outputPath string) bool {
	return strings.ToLower(filepath.Ext(outputPath)) == ".mkv"
}

func isLosslessAudioCodec(c string) bool {
	switch c {
	case "truehd", "mlp", "dts", "flac", "alac", "tta", "wavpack":
		return true
	}
	return strings.HasPrefix(c, "pcm_")
}

func audioReencodeArgs(info *videoInfo, audioTrackIndex int, allowLossless bool, aacBitrate string) []string {
	var srcCodecs []string
	if info != nil {
		switch {
		case audioTrackIndex < 0:
			srcCodecs = info.AudioCodecs
		case audioTrackIndex < len(info.AudioCodecs):
			srcCodecs = []string{info.AudioCodecs[audioTrackIndex]}
		}
	}

	useFlac := make([]bool, len(srcCodecs))
	allSame := true
	for i, c := range srcCodecs {
		useFlac[i] = allowLossless && isLosslessAudioCodec(c)
		if useFlac[i] != useFlac[0] {
			allSame = false
		}
	}

	if len(srcCodecs) == 0 || allSame {
		if len(srcCodecs) > 0 && useFlac[0] {
			return []string{"-c:a", "flac", "-compression_level", "5"}
		}
		return []string{"-c:a", "aac", "-b:a", aacBitrate}
	}

	var args []string
	for i, flac := range useFlac {
		if flac {
			args = append(args, fmt.Sprintf("-c:a:%d", i), "flac", fmt.Sprintf("-compression_level:a:%d", i), "5")
		} else {
			args = append(args, fmt.Sprintf("-c:a:%d", i), "aac", fmt.Sprintf("-b:a:%d", i), aacBitrate)
		}
	}
	return args
}

func (a *App) preRemuxIfNeeded(videoFile string, info *videoInfo) string {
	if !shouldPreRemux(videoFile, info) {
		return ""
	}
	fi, err := os.Stat(videoFile)
	if err != nil {
		return ""
	}

	a.log("info", fmt.Sprintf("预处理重封装 %s · %d kbps", friendlyFileSize(fi.Size()), info.VideoBitrateKbps))

	ext := filepath.Ext(videoFile)
	tmpPath := filepath.Join(filepath.Dir(videoFile),
		strings.TrimSuffix(filepath.Base(videoFile), ext)+".muxer-remux"+ext)

	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-i", videoFile,
		"-c", "copy",
		"-fflags", "+genpts",
		"-map", "0",
		tmpPath)
	hideWindow(cmd)

	a.cmdLock.Lock()
	a.cmd = cmd
	a.cmdLock.Unlock()
	defer func() {
		a.cmdLock.Lock()
		a.cmd = nil
		a.cmdLock.Unlock()
	}()

	if err := cmd.Start(); err != nil {
		a.log("warn", "重封装启动失败，改用原文件: "+err.Error())
		return ""
	}
	if err := cmd.Wait(); err != nil {
		if a.stopped.Load() {
			os.Remove(tmpPath)
			return ""
		}
		a.log("warn", "重封装失败，改用原文件")
		os.Remove(tmpPath)
		return ""
	}

	a.log("ok", "重封装完成")
	return tmpPath
}

func (a *App) premuxBitmapSubWithVideo(videoFile, subtitleFile string, info *videoInfo, subOffset float64) (string, error) {
	dir := filepath.Dir(videoFile)
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	tmpPath := filepath.Join(dir, stem+".muxer-pgsmix.mkv")

	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-copyts",
		"-i", videoFile,
	}
	if info != nil && info.Width > 0 && info.Height > 0 {
		args = append(args, externalSubCanvasArgs(info, subtitleFile)...)
	}
	if subOffset != 0 {
		args = append(args, "-itsoffset", fmt.Sprintf("%.3f", subOffset))
	}
	args = append(args, "-i", subtitleFile,
		"-map", "0:v:0", "-map", "0:a?", "-map", "1:s:0",
		"-c", "copy",
		"-avoid_negative_ts", "make_zero",
		tmpPath,
	)
	cmd := exec.Command("ffmpeg", args...)
	hideWindow(cmd)
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf

	a.cmdLock.Lock()
	a.cmd = cmd
	a.cmdLock.Unlock()
	defer func() {
		a.cmdLock.Lock()
		a.cmd = nil
		a.cmdLock.Unlock()
	}()

	if err := cmd.Start(); err != nil {
		return "", err
	}
	if err := cmd.Wait(); err != nil {
		os.Remove(tmpPath)
		if a.stopped.Load() {
			return "", fmt.Errorf("用户中止")
		}
		tail := stderrBuf.String()
		if len(tail) > 400 {
			tail = "..." + tail[len(tail)-400:]
		}
		tail = strings.TrimSpace(tail)
		if tail != "" {
			return "", fmt.Errorf("%v: %s", err, tail)
		}
		return "", err
	}
	return tmpPath, nil
}

func grabFrameHash(videoFile string, seekSec float64) string {
	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-v", "error",
		"-ss", fmt.Sprintf("%.1f", seekSec),
		"-i", videoFile,
		"-frames:v", "1",
		"-vf", "scale=160:90",
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"pipe:1",
	)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	h := md5.Sum(out)
	return hex.EncodeToString(h[:])
}

func detectFrozenOutput(videoFile string, duration, headOffset float64) bool {
	if duration <= 10 {
		return false
	}
	points := []float64{
		headOffset + duration*0.2,
		headOffset + duration*0.4,
		headOffset + duration*0.6,
		headOffset + duration*0.8,
	}
	var hashes []string

	for _, ss := range points {
		cmd := exec.Command("ffmpeg",
			"-hide_banner", "-v", "error",
			"-ss", fmt.Sprintf("%.1f", ss),
			"-i", videoFile,
			"-frames:v", "1",
			"-vf", "scale=160:90",
			"-f", "rawvideo",
			"-pix_fmt", "rgb24",
			"pipe:1",
		)
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil || len(out) == 0 {
			return false
		}
		h := md5.Sum(out)
		hashes = append(hashes, hex.EncodeToString(h[:]))
	}

	if len(hashes) != 4 {
		return false
	}
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			return false
		}
	}
	return true
}

func (a *App) EditMetadata(videoFile, outputDir, title, year, comment string) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()
	if videoFile == "" {
		a.log("error", "未选择视频文件")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	ext := filepath.Ext(videoFile)
	outputFile := filepath.Join(outputDir, stem+".meta"+ext)

	args := []string{"ffmpeg", "-y", "-i", videoFile}
	if title != "" {
		args = append(args, "-metadata", "title="+title)
	}
	if year != "" {
		args = append(args, "-metadata", "date="+year)
	}
	if comment != "" {
		args = append(args, "-metadata", "comment="+comment)
	}
	args = append(args, "-c", "copy", outputFile)

	a.log("info", "写入元数据: "+filepath.Base(videoFile))
	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		errStr := decodeGBK(out)
		if len(errStr) > 300 {
			errStr = errStr[len(errStr)-300:]
		}
		a.log("error", "写入失败: "+errStr)
		return false
	}

	a.log("ok", "元数据 → "+filepath.Base(outputFile))
	return true
}

func (a *App) sendNotification(message string) {
	safe := strings.ReplaceAll(message, "'", "''")
	script := `$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName System.Windows.Forms
$n=New-Object System.Windows.Forms.NotifyIcon
$n.Icon=[System.Drawing.SystemIcons]::Application
$n.Visible=$true
$n.ShowBalloonTip(5000,'Muxer','` + safe + `',[System.Windows.Forms.ToolTipIcon]::Info)
Start-Sleep -Seconds 6
$n.Dispose()`
	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", script)
	hideWindow(cmd)
	go cmd.Run()
}

func (a *App) playSound(name string) {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	soundPath := filepath.Join(filepath.Dir(exePath), "sounds", name)
	if _, err := os.Stat(soundPath); os.IsNotExist(err) {
		return
	}
	script := fmt.Sprintf(`Add-Type -AssemblyName presentationCore
$player = New-Object System.Windows.Media.MediaPlayer
$player.Open('%s')
$player.Play()
Start-Sleep -Milliseconds 2000`, strings.ReplaceAll(soundPath, `\`, `/`))
	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-NonInteractive", "-Command", script)
	hideWindow(cmd)
	go cmd.Run()
}

func (a *App) PlaySoundExtractDone()  { a.playSound("extract_done.mp3") }
func (a *App) PlaySoundStartProcess() { a.playSound("start_process.mp3") }
func (a *App) PlaySoundMuxDone()      { a.playSound("mux_done.mp3") }

func (a *App) ShutdownSystem() {
	a.log("warn", "60 秒后关机")
	cmd := exec.Command("shutdown", "/s", "/t", "60")
	hideWindow(cmd)
	cmd.Start()
}

type ASSStyle struct {
	Name            string  `json:"name"`
	Fontname        string  `json:"fontname"`
	Fontsize        float64 `json:"fontsize"`
	PrimaryColour   string  `json:"primaryColour"`
	SecondaryColour string  `json:"secondaryColour"`
	OutlineColour   string  `json:"outlineColour"`
	BackColour      string  `json:"backColour"`
	Bold            int     `json:"bold"`
	Italic          int     `json:"italic"`
	Underline       int     `json:"underline"`
	StrikeOut       int     `json:"strikeOut"`
	ScaleX          float64 `json:"scaleX"`
	ScaleY          float64 `json:"scaleY"`
	Spacing         float64 `json:"spacing"`
	Angle           float64 `json:"angle"`
	BorderStyle     int     `json:"borderStyle"`
	Outline         float64 `json:"outline"`
	Shadow          float64 `json:"shadow"`
	Alignment       int     `json:"alignment"`
	MarginL         int     `json:"marginL"`
	MarginR         int     `json:"marginR"`
	MarginV         int     `json:"marginV"`
	Encoding        int     `json:"encoding"`
}

func readASSStyleLine(fields []string) ASSStyle {
	f := func(n int) string {
		if n < len(fields) {
			return strings.TrimSpace(fields[n])
		}
		return ""
	}
	fi := func(n int) int {
		v, _ := strconv.Atoi(f(n))
		return v
	}
	ff := func(n int) float64 {
		v, _ := strconv.ParseFloat(f(n), 64)
		return v
	}
	return ASSStyle{
		Name:            f(0),
		Fontname:        f(1),
		Fontsize:        ff(2),
		PrimaryColour:   f(3),
		SecondaryColour: f(4),
		OutlineColour:   f(5),
		BackColour:      f(6),
		Bold:            fi(7),
		Italic:          fi(8),
		Underline:       fi(9),
		StrikeOut:       fi(10),
		ScaleX:          ff(11),
		ScaleY:          ff(12),
		Spacing:         ff(13),
		Angle:           ff(14),
		BorderStyle:     fi(15),
		Outline:         ff(16),
		Shadow:          ff(17),
		Alignment:       fi(18),
		MarginL:         fi(19),
		MarginR:         fi(20),
		MarginV:         fi(21),
		Encoding:        fi(22),
	}
}

func assStyleToLine(s ASSStyle) string {
	return fmt.Sprintf("Style: %s,%s,%.0f,%s,%s,%s,%s,%d,%d,%d,%d,%.0f,%.0f,%.2f,%.2f,%d,%.2f,%.2f,%d,%d,%d,%d,%d",
		s.Name, s.Fontname, s.Fontsize,
		s.PrimaryColour, s.SecondaryColour, s.OutlineColour, s.BackColour,
		s.Bold, s.Italic, s.Underline, s.StrikeOut,
		s.ScaleX, s.ScaleY, s.Spacing, s.Angle,
		s.BorderStyle, s.Outline, s.Shadow, s.Alignment,
		s.MarginL, s.MarginR, s.MarginV, s.Encoding,
	)
}

func (a *App) ReadASSStyles(filePath string) []ASSStyle {
	data, err := os.ReadFile(filePath)
	if err != nil {
		a.log("error", "无法读取文件: "+err.Error())
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n"), "\n")

	var styles []ASSStyle
	inStyles := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "[v4+ styles]") || strings.EqualFold(trimmed, "[v4 styles]") {
			inStyles = true
			continue
		}
		if inStyles && strings.HasPrefix(trimmed, "[") {
			break
		}
		if inStyles && strings.HasPrefix(strings.ToLower(trimmed), "style:") {
			body := strings.TrimSpace(trimmed[6:])
			fields := strings.Split(body, ",")
			if len(fields) >= 23 {
				styles = append(styles, readASSStyleLine(fields))
			}
		}
	}
	return styles
}

func (a *App) WriteASSStyles(filePath, outPath string, styles []ASSStyle) bool {
	data, err := os.ReadFile(filePath)
	if err != nil {
		a.log("error", "无法读取文件: "+err.Error())
		return false
	}
	if outPath == "" {
		outPath = filePath
	}

	lines := strings.Split(strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n"), "\n")
	var out []string
	inStyles := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "[v4+ styles]") || strings.EqualFold(trimmed, "[v4 styles]") {
			inStyles = true
			out = append(out, line)
			continue
		}
		if inStyles && strings.HasPrefix(trimmed, "[") {
			for _, s := range styles {
				out = append(out, assStyleToLine(s))
			}
			inStyles = false
			styles = nil
			out = append(out, line)
			continue
		}
		if inStyles && strings.HasPrefix(strings.ToLower(trimmed), "style:") {
			continue
		}
		out = append(out, line)
	}
	if inStyles && len(styles) > 0 {
		for _, s := range styles {
			out = append(out, assStyleToLine(s))
		}
	}

	if err := os.WriteFile(outPath, []byte(strings.Join(out, "\n")), 0644); err != nil {
		a.log("error", "写入文件失败: "+err.Error())
		return false
	}
	a.log("ok", "ASS 样式已保存: "+filepath.Base(outPath))
	return true
}

func queueID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func (a *App) AddToQueue(opts ProcessOptions, name string) string {
	if name == "" {
		name = filepath.Base(opts.InputDir)
		if name == "" || name == "." {
			name = "任务"
		}
	}
	t := &QueueTask{
		ID:      queueID(),
		Name:    name,
		Opts:    opts,
		Status:  "pending",
		AddedAt: time.Now().Format("15:04:05"),
	}
	a.queueMu.Lock()
	a.queue = append(a.queue, t)
	a.queueMu.Unlock()
	a.log("info", fmt.Sprintf("排队 %s · 队列 %d", name, len(a.queue)))
	a.emitQueue()
	return t.ID
}

func (a *App) GetQueue() []*QueueTask {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	cp := make([]*QueueTask, len(a.queue))
	copy(cp, a.queue)
	return cp
}

func (a *App) RemoveQueueTask(id string) {
	a.queueMu.Lock()
	for i, t := range a.queue {
		if t.ID == id && t.Status != "running" {
			a.queue = append(a.queue[:i], a.queue[i+1:]...)
			break
		}
	}
	a.queueMu.Unlock()
	a.emitQueue()
}

func (a *App) ClearQueue() {
	a.queueMu.Lock()
	var keep []*QueueTask
	for _, t := range a.queue {
		if t.Status == "running" {
			keep = append(keep, t)
		}
	}
	a.queue = keep
	a.queueMu.Unlock()
	a.emitQueue()
}

func (a *App) StartQueue(scheduledTime, postAction string) bool {
	if a.processing.Load() {
		a.log("warn", "已有任务在执行")
		return false
	}
	if !a.queueRunning.CompareAndSwap(false, true) {
		a.log("warn", "队列已在运行中")
		return false
	}
	a.queueMu.Lock()
	pendingCnt := 0
	for _, t := range a.queue {
		if t.Status == "pending" {
			pendingCnt++
		}
	}
	a.queueMu.Unlock()
	if pendingCnt == 0 {
		a.queueRunning.Store(false)
		a.log("warn", "队列中没有等待执行的任务")
		return false
	}

	var delay time.Duration
	if scheduledTime != "" {
		parts := strings.SplitN(scheduledTime, ":", 2)
		if len(parts) == 2 {
			now := time.Now()
			h, _ := strconv.Atoi(parts[0])
			m, _ := strconv.Atoi(parts[1])
			target := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
			if target.Before(now) {
				target = target.Add(24 * time.Hour)
			}
			delay = time.Until(target)
			a.log("info", fmt.Sprintf("队列 %s 开始 · %.0f 分钟后",
				scheduledTime, delay.Minutes()))
		}
	}

	a.queueStop = make(chan struct{})
	a.emitQueueRunning(true)

	go func() {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-a.queueStop:
				a.queueRunning.Store(false)
				a.emitQueueRunning(false)
				return
			}
		}
		a.runQueueWorker(postAction)
	}()
	return true
}

func (a *App) StopQueue() {
	if a.queueStop != nil {
		select {
		case <-a.queueStop:
		default:
			close(a.queueStop)
		}
	}
	a.queueMu.Lock()
	for _, t := range a.queue {
		if t.Status == "pending" {
			t.Status = "cancelled"
		}
	}
	a.queueMu.Unlock()
	a.emitQueue()
	a.log("info", "队列停止")
}

func (a *App) ResetCancelledTasks() {
	a.queueMu.Lock()
	for _, t := range a.queue {
		if t.Status == "cancelled" || t.Status == "failed" {
			t.Status = "pending"
			t.Err = ""
			t.Attempts = 0
		}
	}
	a.queueMu.Unlock()
	a.emitQueue()
}

func queueOutcome(ok, stopped bool, attempt int) (status, errMsg string) {
	switch {
	case ok:
		return "done", ""
	case stopped:
		return "cancelled", "已停止"
	case attempt < queueMaxAttempts:
		return "pending", fmt.Sprintf("第 %d 次失败，将自动重试", attempt)
	default:
		return "failed", fmt.Sprintf("处理失败（已尝试 %d 次），请查看日志", attempt)
	}
}

func (a *App) runQueueWorker(postAction string) {
	defer func() {
		a.queueRunning.Store(false)
		a.emitQueueRunning(false)
	}()

	a.log("info", "队列开始")
	doneCount, failCount, retryCount := 0, 0, 0
	totalSpent := 0.0
	var failedNames []string

	for {
		a.queueMu.Lock()
		var next *QueueTask
		for _, t := range a.queue {
			if t.Status == "pending" {
				next = t
				break
			}
		}
		a.queueMu.Unlock()

		if next == nil {
			break
		}

		select {
		case <-a.queueStop:
			return
		default:
		}

		gateWaitLogged := false
		for a.processing.Load() {
			if !gateWaitLogged {
				gateWaitLogged = true
				a.log("info", "队列等待空闲")
			}
			select {
			case <-a.queueStop:
				return
			case <-time.After(2 * time.Second):
			}
		}

		a.queueMu.Lock()
		next.Status = "running"
		next.StartedAt = time.Now().Format("15:04:05")
		a.queueMu.Unlock()
		a.emitQueue()

		a.queueMu.Lock()
		next.Attempts++
		attempt := next.Attempts
		a.queueMu.Unlock()
		if attempt > 1 {
			a.log("info", fmt.Sprintf("▶ 重试 %d: %s", attempt, next.Name))
		} else {
			a.log("info", "▶ "+next.Name)
		}
		started := time.Now()
		ok := a.ProcessFiles(next.Opts)
		spent := time.Since(started).Seconds()

		status, errMsg := queueOutcome(ok, a.stopped.Load(), attempt)
		a.queueMu.Lock()
		next.FinishedAt = time.Now().Format("15:04:05")
		next.Status, next.Err = status, errMsg
		switch status {
		case "done":
			doneCount++
			totalSpent += spent
		case "pending":
			retryCount++
		case "failed":
			failCount++
			failedNames = append(failedNames, next.Name)
		}
		a.queueMu.Unlock()
		a.emitQueue()
	}

	a.log("ok", fmt.Sprintf("队列完成 %d 成功 / %d 失败%s · %s",
		doneCount, failCount,
		map[bool]string{true: fmt.Sprintf(" · 重试 %d 次", retryCount), false: ""}[retryCount > 0],
		friendlyDuration(totalSpent)))
	if len(failedNames) > 0 {
		a.log("warn", "失败: "+strings.Join(failedNames, "、"))
	}

	switch postAction {
	case "shutdown":
		if failCount > 0 {
			a.log("warn", "有失败任务，已取消关机")
			break
		}
		a.log("warn", "60 秒后关机")
		offCmd := exec.Command("shutdown", "/s", "/f", "/t", "60")
		hideWindow(offCmd)
		offCmd.Run()
	case "sleep":
		if failCount > 0 {
			a.log("warn", "有失败任务，已取消休眠")
			break
		}
		a.log("info", "进入休眠")
		sleepCmd := exec.Command("rundll32", "powrprof.dll,SetSuspendState", "0,1,0")
		hideWindow(sleepCmd)
		sleepCmd.Run()
	}
}

func (a *App) emitQueueRunning(running bool) { a.emit("queue:running", running) }

func (a *App) emitQueue() {
	a.queueMu.Lock()
	cp := make([]*QueueTask, len(a.queue))
	copy(cp, a.queue)
	a.queueMu.Unlock()
	a.saveQueueSnapshot(cp)
	a.emit("queue:update", cp)
}

type VMAFResult struct {
	VMAF    float64 `json:"vmaf"`
	PSNR    float64 `json:"psnr"`
	SSIM    float64 `json:"ssim"`
	HasVMAF bool    `json:"hasVmaf"`
}

func (a *App) CheckVMAF() bool {
	cmd := exec.Command("ffmpeg", "-filters")
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput()
	if strings.Contains(decodeGBK(out), "libvmaf") {
		a.log("ok", "libvmaf 可用")
		return true
	}
	a.log("warn", "无 libvmaf，仅 PSNR / SSIM")
	return false
}

func (a *App) ScoreVMAF(refFile, distFile string) *VMAFResult {
	if !a.beginJob() {
		return nil
	}
	defer a.endJob()
	if refFile == "" || distFile == "" {
		a.log("error", "未选择参考视频和压缩视频")
		return nil
	}
	refInfo, rErr := getVideoInfo(refFile)
	distInfo, dErr := getVideoInfo(distFile)
	if rErr != nil || dErr != nil {
		a.log("error", "无法读取视频信息")
		return nil
	}
	if refInfo.Width != distInfo.Width || refInfo.Height != distInfo.Height {
		a.log("error", fmt.Sprintf("分辨率不一致 %dx%d vs %dx%d", refInfo.Width, refInfo.Height, distInfo.Width, distInfo.Height))
		return nil
	}
	if refInfo.Duration > 0 && distInfo.Duration > 0 {
		if math.Abs(refInfo.Duration-distInfo.Duration) > math.Max(2, refInfo.Duration*0.02) {
			a.log("warn", fmt.Sprintf("时长差 %s vs %s，评分可能失真",
				friendlyDuration(refInfo.Duration), friendlyDuration(distInfo.Duration)))
		}
	}

	chkCmd := exec.Command("ffmpeg", "-filters")
	hideWindow(chkCmd)
	chkOut, _ := chkCmd.CombinedOutput()
	hasVMAF := strings.Contains(decodeGBK(chkOut), "libvmaf")

	a.log("info", fmt.Sprintf("评分 %s vs %s", filepath.Base(refFile), filepath.Base(distFile)))

	result := &VMAFResult{HasVMAF: hasVMAF}

	a.log("info", "PSNR / SSIM")
	psnrLog := uniqueTempPath("muxer_psnr", ".log")
	ssimLog := uniqueTempPath("muxer_ssim", ".log")
	defer os.Remove(psnrLog)
	defer os.Remove(ssimLog)

	psCmd := exec.Command("ffmpeg", "-y",
		"-i", distFile, "-i", refFile,
		"-lavfi", fmt.Sprintf("[0:v][1:v]psnr=f=%s;[0:v][1:v]ssim=f=%s", psnrLog, ssimLog),
		"-f", "null", "-")
	hideWindow(psCmd)
	psOut, psErr := psCmd.CombinedOutput()
	if psErr != nil {
		s := decodeGBK(psOut)
		if len(s) > 400 {
			s = s[len(s)-400:]
		}
		a.log("error", "PSNR/SSIM 计算失败: "+strings.TrimSpace(s))
		return nil
	}

	result.PSNR = parseMetricLog(psnrLog, "psnr_avg")
	result.SSIM = parseMetricLog(ssimLog, "All")

	if hasVMAF {
		a.log("info", "VMAF")
		vmafLog := uniqueTempPath("muxer_vmaf", ".json")
		defer os.Remove(vmafLog)

		vmafFilter := fmt.Sprintf("[0:v][1:v]libvmaf=log_path=%s:log_fmt=json:n_threads=4",
			strings.ReplaceAll(vmafLog, "\\", "/"))
		vmCmd := exec.Command("ffmpeg", "-y",
			"-i", distFile, "-i", refFile,
			"-lavfi", vmafFilter,
			"-f", "null", "-")
		hideWindow(vmCmd)
		vmOut, vmErr := vmCmd.CombinedOutput()
		if vmErr != nil {
			s := decodeGBK(vmOut)
			if len(s) > 400 {
				s = s[len(s)-400:]
			}
			a.log("warn", "VMAF 计算失败: "+strings.TrimSpace(s))
		} else {
			result.VMAF = parseVMAFJson(vmafLog)
		}
	}

	if hasVMAF {
		a.log("ok", fmt.Sprintf("VMAF=%.2f  PSNR=%.2f dB  SSIM=%.4f",
			result.VMAF, result.PSNR, result.SSIM))
	} else {
		a.log("ok", fmt.Sprintf("PSNR=%.2f dB  SSIM=%.4f", result.PSNR, result.SSIM))
	}
	return result
}

func parseMetricLog(path, key string) float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, key+":") {
				var v float64
				fmt.Sscanf(field[len(key)+1:], "%f", &v)
				return v
			}
		}
		break
	}
	return 0
}

func parseVMAFJson(path string) float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0
	}
	if pm, ok := raw["pooled_metrics"].(map[string]interface{}); ok {
		if vmaf, ok := pm["vmaf"].(map[string]interface{}); ok {
			if mean, ok := vmaf["mean"].(float64); ok {
				return mean
			}
		}
	}
	return 0
}

type PreviewResult struct {
	OriginalSizeMB  float64 `json:"originalSizeMB"`
	EstimatedSizeMB float64 `json:"estimatedSizeMB"`
	Ratio           float64 `json:"ratio"`
	OriginalShot    string  `json:"originalShot"`
	CompressedShot  string  `json:"compressedShot"`
	ClipDurationSec float64 `json:"clipDurationSec"`
	Codec           string  `json:"codec"`
	CRF             int     `json:"crf"`
}

func imgToDataURI(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
}

func buildEncArgs(opts ProcessOptions, info *videoInfo) []string {
	crf := fmt.Sprintf("%d", opts.CRF)
	isH265 := opts.Codec == "h265"
	isAV1 := opts.Codec == "av1"
	switch opts.Encoder {
	case "nvenc":
		cv := "h264_nvenc"
		if isH265 {
			cv = "hevc_nvenc"
		} else if isAV1 {
			cv = "av1_nvenc"
		}
		srcBitrate := 0
		outHeight := opts.ScaleHeight
		srcFPS := 0.0
		if info != nil {
			srcBitrate = info.VideoBitrateKbps
			srcFPS = info.FPS
			if outHeight == 0 {
				outHeight = info.Height
			}
		}
		maxrate, bufsize := nvencMaxrate(outHeight, srcFPS, isH265, srcBitrate)
		args := []string{"-c:v", cv, "-rc:v", "vbr"}
		args = append(args, nvencTuneArgs(cv)...)
		args = append(args,
			"-cq:v", crf,
			"-b:v", "0",
			"-maxrate", maxrate,
			"-bufsize", bufsize,
			"-spatial-aq", "1",
			"-aq-strength", "8",
			"-rc-lookahead", "32",
			"-preset", nvencPreset(opts.Preset),
		)
		if isH265 {
			args = append(args, "-pix_fmt", "p010le")
		}
		return args
	case "amf":
		cv := "h264_amf"
		if isH265 {
			cv = "hevc_amf"
		} else if isAV1 {
			cv = "av1_amf"
		}
		return []string{"-c:v", cv, "-qp_i", crf, "-qp_p", crf, "-qp_b", crf, "-quality", amfQuality(opts.Preset)}
	case "qsv":
		cv := "h264_qsv"
		if isH265 {
			cv = "hevc_qsv"
		} else if isAV1 {
			cv = "av1_qsv"
		}
		return []string{"-c:v", cv, "-global_quality", crf, "-preset", qsvPreset(opts.Preset)}
	default:
		if isAV1 {
			return []string{"-c:v", "libsvtav1", "-crf", crf, "-preset", svtav1Preset(opts.Preset)}
		}
		cv := "libx264"
		if isH265 {
			cv = "libx265"
		}
		return []string{"-c:v", cv, "-crf", crf, "-preset", opts.Preset}
	}
}

func (a *App) PreviewCompress(opts ProcessOptions) *PreviewResult {
	if !a.beginJob() {
		return nil
	}
	defer a.endJob()
	if opts.InputDir == "" {
		a.log("error", "未设置输入目录")
		return nil
	}
	videoFiles := findFiles(opts.InputDir, videoExts)
	if len(videoFiles) == 0 {
		a.log("error", "未找到视频文件")
		return nil
	}
	videoFile := videoFiles[0]

	info, err := getVideoInfo(videoFile)
	if err != nil || info.Duration <= 0 {
		a.log("error", "无法读取视频信息")
		return nil
	}

	origStat, err := os.Stat(videoFile)
	if err != nil {
		a.log("error", "无法读取文件大小")
		return nil
	}
	origSizeMB := float64(origStat.Size()) / (1024 * 1024)

	clipDur := 10.0
	if info.Duration < 60 {
		clipDur = info.Duration / 6
		if clipDur < 2 {
			clipDur = 2
		}
	}
	if clipDur > info.Duration {
		clipDur = info.Duration
	}

	samplePositions := []float64{0.25, 0.50, 0.75}
	if info.Duration < 30 {
		samplePositions = []float64{0.50}
	}

	origShot := uniqueTempPath("muxer_preview_orig", ".jpg")
	compShot := uniqueTempPath("muxer_preview_comp", ".jpg")
	var tmpFiles []string
	defer func() {
		for _, f := range tmpFiles {
			os.Remove(f)
		}
		os.Remove(origShot)
		os.Remove(compShot)
	}()

	codec := opts.Codec
	if codec == "" {
		codec = "h264"
	}
	a.log("info", fmt.Sprintf("试算 %s · %d 段 %.0fs · Q%d %s %s",
		filepath.Base(videoFile), len(samplePositions), clipDur, opts.CRF, strings.ToUpper(codec), opts.Encoder))

	var ratios []float64
	var midCompFile string
	midIdx := len(samplePositions) / 2

	for i, pos := range samplePositions {
		startSec := info.Duration*pos - clipDur/2
		if startSec < 0 {
			startSec = 0
		}
		if startSec+clipDur > info.Duration {
			startSec = info.Duration - clipDur
		}

		clipFile := uniqueTempPath(fmt.Sprintf("muxer_preview_clip_%d", i), ".mp4")
		compFile := uniqueTempPath(fmt.Sprintf("muxer_preview_comp_%d", i), ".mp4")
		tmpFiles = append(tmpFiles, clipFile, compFile)

		posLabel := fmt.Sprintf("%.0f%%", pos*100)
		a.log("info", fmt.Sprintf("  采样 %d/%d %s", i+1, len(samplePositions), posLabel))

		clipCmd := exec.Command("ffmpeg", "-y",
			"-ss", fmt.Sprintf("%.3f", startSec),
			"-t", fmt.Sprintf("%.3f", clipDur),
			"-i", videoFile,
			"-c", "copy", clipFile)
		hideWindow(clipCmd)
		if out, err := clipCmd.CombinedOutput(); err != nil {
			a.log("warn", fmt.Sprintf("  采样 %s 提取失败，跳过: %s", posLabel, friendlyErrorMsg(decodeGBK(out), opts.Encoder, false)))
			continue
		}

		compArgs := []string{"ffmpeg", "-y", "-i", clipFile}
		compArgs = append(compArgs, buildEncArgs(opts, info)...)
		if opts.ScaleHeight > 0 {
			compArgs = append(compArgs, "-vf", fmt.Sprintf("scale=-2:%d", opts.ScaleHeight))
		}
		if opts.TenBit && opts.Encoder != "nvenc" && opts.Encoder != "amf" {
			compArgs = append(compArgs, "-pix_fmt", "yuv420p10le")
		}
		compArgs = append(compArgs, "-an", compFile)
		compCmd := exec.Command(compArgs[0], compArgs[1:]...)
		hideWindow(compCmd)
		if out, err := compCmd.CombinedOutput(); err != nil {
			a.log("warn", fmt.Sprintf("  采样 %s 压缩失败，跳过: %s", posLabel, friendlyErrorMsg(decodeGBK(out), opts.Encoder, false)))
			continue
		}

		clipStat, _ := os.Stat(clipFile)
		compStat, err := os.Stat(compFile)
		if err != nil || clipStat == nil {
			continue
		}
		clipOrigMB := float64(clipStat.Size()) / (1024 * 1024)
		clipCompMB := float64(compStat.Size()) / (1024 * 1024)
		if clipOrigMB > 0 {
			r := clipCompMB / clipOrigMB
			ratios = append(ratios, r)
			a.log("info", fmt.Sprintf("  采样 %s 压缩比=%.0f%%", posLabel, r*100))
		}

		if i == midIdx {
			midCompFile = compFile
		}
	}

	if len(ratios) == 0 {
		a.log("error", "所有采样点均失败，无法预估")
		return nil
	}

	var sumRatio float64
	for _, r := range ratios {
		sumRatio += r
	}
	ratio := sumRatio / float64(len(ratios))

	estVideoMB := origSizeMB * ratio

	audioBitrate := 192.0
	if opts.AudioBitrate != "" {
		if parsed, err := strconv.ParseFloat(strings.TrimSuffix(opts.AudioBitrate, "k"), 64); err == nil {
			audioBitrate = parsed
		}
	}
	audioMB := info.Duration * audioBitrate / 8 / 1024

	bufferFactor := 1.05
	if opts.Hardsub {
		bufferFactor = 1.20
	}

	estMB := (estVideoMB + audioMB) * bufferFactor

	a.log("info", "生成对比截图")
	midStartSec := info.Duration*samplePositions[midIdx] - clipDur/2
	if midStartSec < 0 {
		midStartSec = 0
	}
	midTS := fmt.Sprintf("%.3f", midStartSec+clipDur/2)
	shotOrigCmd := exec.Command("ffmpeg", "-y",
		"-ss", midTS, "-i", videoFile,
		"-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "3", origShot)
	hideWindow(shotOrigCmd)
	shotOrigCmd.Run()
	if midCompFile != "" {
		shotCompCmd := exec.Command("ffmpeg", "-y",
			"-ss", fmt.Sprintf("%.3f", clipDur/2), "-i", midCompFile,
			"-frames:v", "1", "-vf", "scale=640:-2", "-q:v", "3", compShot)
		hideWindow(shotCompCmd)
		shotCompCmd.Run()
	}

	codecLabel := map[string]string{"h264": "H.264", "h265": "H.265", "av1": "AV1", "": "H.264"}[codec]
	displayRatio := ratio
	if origSizeMB > 0 {
		displayRatio = estMB / origSizeMB
	}
	result := &PreviewResult{
		OriginalSizeMB:  origSizeMB,
		EstimatedSizeMB: estMB,
		Ratio:           displayRatio,
		OriginalShot:    imgToDataURI(origShot),
		CompressedShot:  imgToDataURI(compShot),
		ClipDurationSec: clipDur * float64(len(samplePositions)),
		Codec:           codecLabel,
		CRF:             opts.CRF,
	}
	a.log("ok", fmt.Sprintf("试算 %s → %s · %.0f%%",
		fmtSizeMB(origSizeMB), fmtSizeMB(estMB), displayRatio*100))
	return result
}

func fmtSizeMB(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.2f GB", mb/1024)
	}
	return fmt.Sprintf("%.1f MB", mb)
}

func (a *App) runCancellableCmd(cmd *exec.Cmd) ([]byte, error) {
	a.cmdLock.Lock()
	a.cmd = cmd
	a.cmdLock.Unlock()
	defer func() {
		a.cmdLock.Lock()
		a.cmd = nil
		a.cmdLock.Unlock()
	}()
	return cmd.CombinedOutput()
}

func (a *App) runWithProgress(cmd *exec.Cmd, totalDuration float64) (string, error) {
	a.cmdLock.Lock()
	a.cmd = cmd
	a.cmdLock.Unlock()
	defer func() {
		a.cmdLock.Lock()
		a.cmd = nil
		a.cmdLock.Unlock()
	}()

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	var lastProgressLine string
	var stderrTail []string
	stderrDropped := 0
	lastPct := -1

	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Split(func(data []byte, atEOF bool) (advance int, token []byte, err error) {
			if atEOF && len(data) == 0 {
				return 0, nil, nil
			}
			for i, b := range data {
				if b == '\r' || b == '\n' {
					advance = i + 1
					if b == '\r' && len(data) > i+1 && data[i+1] == '\n' {
						advance++
					}
					return advance, data[:i], nil
				}
			}
			if atEOF {
				return len(data), data, nil
			}
			return 0, nil, nil
		})
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := decodeTextFile(scanner.Bytes())
			if isFFmpegProgressLine(line) {
				stderrMu.Lock()
				lastProgressLine = line
				stderrMu.Unlock()
			} else {
				stderrMu.Lock()
				if stderrBuf.Len() < stderrHeadLimit {
					stderrBuf.WriteString(line + "\n")
				} else {
					stderrTail = append(stderrTail, line)
					if len(stderrTail) > stderrTailLines {
						stderrTail = stderrTail[1:]
					}
					stderrDropped++
				}
				stderrMu.Unlock()
			}
			if totalDuration > 0 {
				if m := reFFmpegTime.FindStringSubmatch(line); m != nil {
					h, _ := strconv.Atoi(m[1])
					mn, _ := strconv.Atoi(m[2])
					sec, _ := strconv.ParseFloat(m[3], 64)
					cur := float64(h*3600+mn*60) + sec
					pct := int(cur / totalDuration * 100)
					if pct > 99 {
						pct = 99
					}
					if pct != lastPct {
						lastPct = pct
						speed := ""
						if sm := reFFmpegSpeed.FindStringSubmatch(line); sm != nil {
							speed = sm[1] + "x"
						}
						a.emit("progress", map[string]interface{}{
							"pct":   pct,
							"speed": speed,
						})
					}
				}
			}
		}
		io.Copy(io.Discard, stderrPipe)
	}()

	waitErr := cmd.Wait()
	<-done

	if waitErr == nil && totalDuration > 0 && a.ctx != nil {
		a.emit("progress", map[string]interface{}{"pct": 100, "speed": ""})
	}

	stderrMu.Lock()
	out := stderrBuf.String()
	if stderrDropped > 0 {
		if omitted := stderrDropped - len(stderrTail); omitted > 0 {
			out += fmt.Sprintf("…（省略 %d 行重复输出）…\n", omitted)
		}
		out += strings.Join(stderrTail, "\n") + "\n"
	}
	if lastProgressLine != "" {
		out += lastProgressLine + "\n"
	}
	stderrMu.Unlock()

	if waitErr != nil {
		return out, waitErr
	}
	return "", nil
}

const (
	stderrHeadLimit = 128 * 1024
	stderrTailLines = 200
)

func isFFmpegProgressLine(line string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, "frame=") || strings.HasPrefix(s, "size=")
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true,
	".iso": true, ".vob": true, ".m2ts": true, ".ts": true, ".m4v": true, ".webm": true,
}
var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".sup": true, ".vtt": true, ".ttml": true, ".idx": true, ".mks": true,
}
var fontExts = map[string]bool{
	".ttf": true, ".otf": true, ".ttc": true,
}

func isMuxerTempArtifact(name string) bool {
	return strings.HasPrefix(name, "muxer-ad-") ||
		strings.Contains(name, ".muxer-clean.") ||
		strings.Contains(name, ".muxer-utf8.") ||
		strings.Contains(name, ".muxer-validated.") ||
		strings.Contains(name, ".muxer-remux.") ||
		strings.Contains(name, ".muxer-shift.") ||
		strings.Contains(name, ".muxer-fix.") ||
		strings.Contains(name, ".muxer-pgsmix.")
}

var muxerTempSoleCopyTags = []string{".muxer-remux.", ".muxer-fix."}

func muxerTempSoleCopyStem(name string) string {
	for _, tag := range muxerTempSoleCopyTags {
		if strings.Contains(name, tag) {
			return strings.Replace(name, strings.TrimSuffix(tag, "."), "", 1)
		}
	}
	return ""
}

func muxerTempIsSoleCopy(dir, name string) bool {
	stem := muxerTempSoleCopyStem(name)
	if stem == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, stem))
	return err != nil
}

func (a *App) cleanupTempArtifacts(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isMuxerTempArtifact(name) {
			continue
		}
		if muxerTempIsSoleCopy(dir, name) {
			a.log("warn", "临时产物无成品，保留 "+name)
			continue
		}
		os.Remove(filepath.Join(dir, name))
	}
}

func findFiles(dir string, exts map[string]bool) []string {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !exts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		if isMuxerTempArtifact(name) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files
}

func findTextSubtitles(dir string) []string {
	textExts := map[string]bool{
		".srt": true, ".ass": true, ".ssa": true,
		".sub": true, ".vtt": true, ".ttml": true,
	}
	return findFiles(dir, textExts)
}

func extractEpisodeTag(name string) (cleaned, epTag string) {
	if m := reEpSeasonTag.FindStringIndex(name); m != nil {
		epTag = strings.ToUpper(name[m[0]:m[1]])
		cleaned = strings.TrimSpace(name[:m[0]] + name[m[1]:])
		return
	}
	if m := reEpWordTag.FindStringSubmatchIndex(name); m != nil {
		full := name[m[0]:m[1]]
		num, _ := strconv.Atoi(reBareDigits.FindString(full))
		epTag = fmt.Sprintf("E%02d", num)
		cleaned = strings.TrimSpace(name[:m[0]] + name[m[1]:])
		return
	}
	if m := reEpBracketTag.FindStringSubmatchIndex(name); m != nil {
		num, _ := strconv.Atoi(name[m[2]:m[3]])
		epTag = fmt.Sprintf("E%02d", num)
		cleaned = strings.TrimSpace(name[:m[0]] + name[m[1]:])
		return
	}
	if m := reEpLooseTag.FindStringSubmatchIndex(name); m != nil {
		num, _ := strconv.Atoi(name[m[2]:m[3]])
		epTag = fmt.Sprintf("E%02d", num)
		cleaned = strings.TrimSpace(name[:m[0]] + name[m[1]:])
		return
	}
	return name, ""
}

func cleanFilename(name string) string {
	remaining, epTag := extractEpisodeTag(name)

	remaining = reBracketGroup.ReplaceAllString(remaining, "")
	remaining = reReleaseTag.ReplaceAllString(remaining, "")
	remaining = reSpaceRun.ReplaceAllString(remaining, ".")
	remaining = reDotRun.ReplaceAllString(remaining, ".")
	remaining = strings.Trim(remaining, ".")

	if remaining == "" {
		if epTag != "" {
			return epTag
		}
		return "unknown"
	}

	if epTag != "" {
		remaining = remaining + "." + epTag
	}
	return remaining
}

func detectHardcodedSubtitle(videoPath string) bool {
	name := strings.ToUpper(filepath.Base(videoPath))
	for _, kw := range []string{"HC", "HARDSUB", "HARDSUBS", "HARDCODED", "BURNED"} {
		if strings.Contains(name, kw) {
			return true
		}
	}
	for _, kw := range []string{"硬压中英双语", "DDYS.中英双语", "硬压中字", "硬压", "DDYS.中字"} {
		if strings.Contains(name, kw) {
			return true
		}
	}
	return false
}

type videoInfo struct {
	Duration         float64
	HasSub           bool
	FPS              float64
	BrokenTimestamps bool
	VideoBitrateKbps int
	SizeBitrateKbps  int
	Width            int
	Height           int
	SubWidth         int
	SubHeight        int
	AudioCodecs      []string
	VideoStreamIndex int
	VideoCodec       string
	ColorPrimaries   string
	ColorTransfer    string
	ColorSpace       string
}

func parseFrameRate(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0/0" || strings.EqualFold(s, "n/a") {
		return 0
	}
	var num, den float64
	if n, _ := fmt.Sscanf(s, "%f/%f", &num, &den); n == 2 {
		if den == 0 {
			return 0
		}
		return num / den
	}
	var v float64
	fmt.Sscanf(s, "%f", &v)
	return v
}

func chooseVideoFPS(rRate, avgRate string) (fps float64, brokenTS bool) {
	r := parseFrameRate(rRate)
	avg := parseFrameRate(avgRate)
	avgOK := avg > 0 && avg <= 240
	if r > 240 {
		if avgOK {
			return avg, true
		}
		return 24, true
	}
	if avgOK && r > 120 && r/avg >= 4 {
		return avg, true
	}
	if r > 0 {
		return r, false
	}
	if avg > 0 {
		return avg, false
	}
	return 0, false
}

func timestampRepairArgs(info *videoInfo) []string {
	if info == nil || !info.BrokenTimestamps {
		return nil
	}
	fps := info.FPS
	if fps <= 0 || fps > 240 {
		fps = 24
	}
	return []string{"-fps_mode", "cfr", "-r", strconv.FormatFloat(fps, 'f', 3, 64)}
}

func isMuxTimestampError(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "application provided duration") {
		return true
	}
	if strings.Contains(lower, "duration") && strings.Contains(lower, "is invalid") &&
		(strings.Contains(lower, "error muxing") || strings.Contains(lower, "submitting a packet")) {
		return true
	}
	return false
}

func getVideoInfo(path string) (*videoInfo, error) {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	info := &videoInfo{}
	if fmtMap, ok := raw["format"].(map[string]interface{}); ok {
		if dur, ok := fmtMap["duration"].(string); ok {
			fmt.Sscanf(dur, "%f", &info.Duration)
		}
	}
	if streams, ok := raw["streams"].([]interface{}); ok {
		for _, s := range streams {
			if stream, ok := s.(map[string]interface{}); ok {
				if stream["codec_type"] == "subtitle" {
					info.HasSub = true
					if info.SubWidth == 0 {
						w, wok := stream["width"].(float64)
						h, hok := stream["height"].(float64)
						if wok && hok && w > 0 && h > 0 {
							info.SubWidth = int(w)
							info.SubHeight = int(h)
						}
					}
				}
				if stream["codec_type"] == "audio" {
					if cn, ok := stream["codec_name"].(string); ok {
						info.AudioCodecs = append(info.AudioCodecs, strings.ToLower(cn))
					}
				}
				if stream["codec_type"] == "video" && info.FPS == 0 {
					isAttachedPic := false
					if disp, ok := stream["disposition"].(map[string]interface{}); ok {
						if v, ok := disp["attached_pic"].(float64); ok && v == 1 {
							isAttachedPic = true
						}
					}
					if !isAttachedPic {
						codecName, _ := stream["codec_name"].(string)
						info.VideoCodec = codecName
						dur, _ := stream["duration"].(string)
						isAttachedPic = (codecName == "mjpeg" || codecName == "png" || codecName == "bmp") &&
							(dur == "" || dur == "0.000000" || dur == "0")
					}
					if isAttachedPic {
						continue
					}
					if idx, ok := stream["index"].(float64); ok {
						info.VideoStreamIndex = int(idx)
					}
					rRate, _ := stream["r_frame_rate"].(string)
					avgRate, _ := stream["avg_frame_rate"].(string)
					info.FPS, info.BrokenTimestamps = chooseVideoFPS(rRate, avgRate)
					if br, ok := stream["bit_rate"].(string); ok {
						var bps int
						fmt.Sscanf(br, "%d", &bps)
						info.VideoBitrateKbps = bps / 1000
					}
					if w, ok := stream["width"].(float64); ok {
						info.Width = int(w)
					}
					if h, ok := stream["height"].(float64); ok {
						info.Height = int(h)
					}
					info.ColorPrimaries, _ = stream["color_primaries"].(string)
					info.ColorTransfer, _ = stream["color_transfer"].(string)
					info.ColorSpace, _ = stream["color_space"].(string)
				}
			}
		}
	}
	if info.VideoBitrateKbps == 0 {
		if fmtMap, ok := raw["format"].(map[string]interface{}); ok {
			if br, ok := fmtMap["bit_rate"].(string); ok {
				var bps int
				fmt.Sscanf(br, "%d", &bps)
				info.VideoBitrateKbps = bps / 1000
			}
		}
	}
	if info.VideoBitrateKbps == 0 && info.Duration > 1 {
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			info.SizeBitrateKbps = int(float64(st.Size()) * 8 / info.Duration / 1000)
		}
	}
	if info.FPS == 0 {
		info.FPS = 23.976
	}
	return info, nil
}

func probeBitmapSubSize(path string) (int, int) {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-select_streams", "s:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	var w, h int
	if n, _ := fmt.Sscanf(strings.TrimSpace(string(out)), "%d,%d", &w, &h); n != 2 {
		return 0, 0
	}
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}

func subCanvasSize(info *videoInfo, subW, subH int) (int, int) {
	vidW, vidH := 0, 0
	if info != nil {
		vidW, vidH = info.Width, info.Height
	}
	if subW <= 0 || subH <= 0 {
		return vidW, vidH
	}
	if vidW > 0 && vidH > 0 && (subW > vidW || subH > vidH) {
		return vidW, vidH
	}
	return subW, subH
}

func bitmapCanvasArgs(info *videoInfo, subW, subH int) []string {
	w, h := subCanvasSize(info, subW, subH)
	if w <= 0 || h <= 0 {
		return nil
	}
	return []string{"-canvas_size", fmt.Sprintf("%dx%d", w, h)}
}

func embeddedSubCanvasArgs(info *videoInfo) []string {
	if info == nil {
		return nil
	}
	return bitmapCanvasArgs(info, info.SubWidth, info.SubHeight)
}

func externalSubCanvasArgs(info *videoInfo, subPath string) []string {
	w, h := probeBitmapSubSize(subPath)
	return bitmapCanvasArgs(info, w, h)
}

func codecToExt(codec string) string {
	switch codec {
	case "ass", "ssa":
		return ".ass"
	case "subrip", "srt", "mov_text":
		return ".srt"
	case "webvtt":
		return ".vtt"
	case "ttml", "timedtext":
		return ".ttml"
	case "hdmv_pgs_subtitle", "pgssub":
		return ".sup"
	case "microdvd":
		return ".sub"
	case "dvb_subtitle", "dvd_subtitle":
		return ".mks"
	default:
		return ".mks"
	}
}

func friendlySubtitleExtractError(rawErr string, codec string, ext string) string {
	tail := rawErr
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	lower := strings.ToLower(tail)

	if codec == "mov_text" && ext == ".srt" && (strings.Contains(lower, "unsupported subtitles codec") ||
		(strings.Contains(lower, "could not write header") && strings.Contains(lower, "invalid argument"))) {
		return "当前字幕轨是 mov_text（常见于 MP4 内嵌字幕），不能直接 copy 到 .srt；已改为自动转码为 srt 后再导出"
	}
	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "access is denied") {
		return "字幕文件写入失败 — 请检查输出目录权限，或确认目标字幕文件未被其他程序占用"
	}
	if strings.Contains(lower, "no such file") && strings.Contains(lower, "ffmpeg") {
		return "未找到 FFmpeg — 请确认 FFmpeg 已安装并加入系统 PATH"
	}
	if strings.Contains(lower, "stream map") && strings.Contains(lower, "matches no streams") {
		return "未找到可提取的字幕轨 — 该轨道可能不存在，或文件中的字幕索引已变化"
	}
	if (ext == ".mks" || ext == ".mkv") && (strings.Contains(lower, "could not write header") || strings.Contains(lower, "invalid argument") || strings.Contains(lower, "unsupported subtitles codec")) {
		return fmt.Sprintf("当前字幕轨编码 %s 无法直接导出为独立字幕文件；这类轨道通常是特殊封装或私有格式，FFmpeg 可能只能保留在原视频容器中", codec)
	}

	return tail[max(0, len(tail)-300):]
}

func isDiskFullError(lower string) bool {
	for _, sig := range []string{
		"no space left",
		"disk full",
		"not enough space on the disk",
		"there is not enough space",
		"insufficient disk space",
		"磁盘空间不足",
		"空间不足",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func isHostMemoryExhausted(lower string) bool {
	for _, sig := range []string{
		"impossible to get a blank canvas",
		"error while add the frame to buffer source",
		"cannot allocate memory",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func isGPUEncoderFailure(errMsg, encoder string) bool {
	switch strings.ToLower(encoder) {
	case "nvenc", "amf", "qsv":
	default:
		return false
	}
	lower := strings.ToLower(errMsg)
	if isHostMemoryExhausted(lower) {
		return false
	}
	if strings.Contains(lower, "failed to parse header of nalu") ||
		strings.Contains(lower, "failed to parse nal") ||
		strings.Contains(lower, "invalid data found") ||
		strings.Contains(lower, "corrupt") ||
		strings.Contains(lower, "moov atom not found") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "access is denied") ||
		isDiskFullError(lower) ||
		(strings.Contains(lower, "no such file") && strings.Contains(lower, "ffmpeg")) {
		return false
	}
	if strings.Contains(lower, "vf_subtitles") ||
		strings.Contains(lower, "ass_process_chunk") ||
		strings.Contains(lower, "error parsing ssa") ||
		strings.Contains(lower, "error parsing ass") ||
		(strings.Contains(lower, "libass") && strings.Contains(lower, "error")) {
		return false
	}
	for _, sig := range []string{
		"nvenc", "amf", "qsv", "cuda", "cuvid", "d3d11", "dxva", "vaapi",
		"impossible to convert between the formats",
		"device setup failed",
		"encoder initialization failed",
		"no capable devices found",
		"out of memory",
		"driver",
		"generic error in an external library",
	} {
		if strings.Contains(lower, sig) {
			return true
		}
	}
	return false
}

func friendlyErrorMsg(rawErr string, encoder string, hardsub bool) string {
	tail := rawErr
	if len(tail) > 4000 {
		tail = tail[len(tail)-4000:]
	}
	lower := strings.ToLower(tail)
	isNV := encoder == "nvenc" || strings.Contains(lower, "nvenc")
	isAMD := encoder == "amf" || strings.Contains(lower, "amf")
	isQSV := encoder == "qsv" || strings.Contains(lower, "qsv")
	isGPU := isNV || isAMD || isQSV

	gpuName := "显卡"
	if isNV {
		gpuName = "NVIDIA"
	} else if isAMD {
		gpuName = "AMD"
	} else if isQSV {
		gpuName = "Intel"
	}

	if strings.Contains(lower, "failed to parse header of nalu") ||
		strings.Contains(lower, "failed to parse nal") ||
		(strings.Contains(lower, "invalid data") && strings.Contains(lower, "error writing trailer")) ||
		(strings.Contains(lower, "[hevc") && strings.Contains(lower, "invalid data found")) ||
		(strings.Contains(lower, "[h264") && strings.Contains(lower, "invalid data found")) {
		return "源文件视频流损坏，文件末尾数据异常"
	}

	if isMuxTimestampError(tail) {
		return "源文件时间戳损坏，MP4 封装拒绝写入"
	}

	if isHostMemoryExhausted(lower) {
		if hardsub {
			return "内存不足，字幕画布分配失败"
		}
		return "内存不足，滤镜链缓冲分配失败"
	}

	if isNV && (strings.Contains(lower, "encoder") && strings.Contains(lower, "not found") ||
		strings.Contains(lower, "unknown encoder") || strings.Contains(lower, "no such encoder")) {
		return "NVENC 不可用，检查显卡驱动"
	}
	if isNV && (strings.Contains(lower, "device lost") ||
		strings.Contains(lower, "device removed") ||
		strings.Contains(lower, "encodepicture failed") ||
		strings.Contains(lower, "encode_picture") && strings.Contains(lower, "fail") ||
		strings.Contains(lower, "nvencodepicture") ||
		strings.Contains(lower, "error while encoding video frame") ||
		strings.Contains(lower, "error encoding") && strings.Contains(lower, "video")) {
		return "NVENC 编码中断，驱动重置 / GPU 被抢占 / 显存不足"
	}
	if isNV && (strings.Contains(lower, "invalid argument") ||
		strings.Contains(lower, "driver does not support") ||
		strings.Contains(lower, "nvenc api") ||
		strings.Contains(lower, "no capable devices found") ||
		strings.Contains(lower, "cannot load") ||
		strings.Contains(lower, "initialization failed")) {
		return "NVENC 初始化失败，驱动版本过低或显卡不支持该编码格式"
	}
	if isNV && (strings.Contains(lower, "out of memory") || strings.Contains(lower, "allocate")) {
		return "显存不足"
	}
	if isNV && strings.Contains(lower, "too many") {
		return "NVENC 会话数超限"
	}

	if isAMD && (strings.Contains(lower, "invalid") || strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "not found") || strings.Contains(lower, "initialization")) {
		return "AMD 编码失败，驱动版本或格式不支持"
	}

	if isQSV && (strings.Contains(lower, "invalid") || strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "not found") || strings.Contains(lower, "initialization") ||
		strings.Contains(lower, "error") || strings.Contains(lower, "fail")) {
		return "QSV 编码失败，驱动版本或格式不支持"
	}

	if strings.Contains(lower, "permission denied") || strings.Contains(lower, "access is denied") {
		return "文件访问被拒绝"
	}
	if isDiskFullError(lower) {
		return "磁盘空间不足"
	}
	if strings.Contains(lower, "no such file") && strings.Contains(lower, "ffmpeg") {
		return "未找到 FFmpeg"
	}

	if hardsub {
		isSubErr := strings.Contains(lower, "error while filtering") ||
			strings.Contains(lower, "filter error") ||
			strings.Contains(lower, "vf_subtitles") ||
			strings.Contains(lower, "ass_process_chunk") ||
			strings.Contains(lower, "libass") && strings.Contains(lower, "error") ||
			strings.Contains(lower, "error parsing ssa") ||
			strings.Contains(lower, "error parsing ass") ||
			strings.Contains(lower, "subtitle codec") && strings.Contains(lower, "error") ||
			(strings.Contains(lower, "subtitle") || strings.Contains(lower, "ass") || strings.Contains(lower, "srt")) &&
				(strings.Contains(lower, "not found") || strings.Contains(lower, "no such") || strings.Contains(lower, "failed"))
		if isSubErr {
			return "字幕渲染出错，字幕格式有缺陷"
		}
	}

	if isGPU && (strings.Contains(lower, "error") || strings.Contains(lower, "fail")) {
		return fmt.Sprintf("%s 硬件编码失败", gpuName)
	}

	if strings.Contains(lower, "unknown encoder") || strings.Contains(lower, "encoder") && strings.Contains(lower, "not found") {
		return "当前 FFmpeg 不支持该编码格式"
	}

	if strings.Contains(lower, "corrupt") || strings.Contains(lower, "moov atom not found") {
		return "视频文件损坏"
	}

	if strings.Contains(lower, "nothing was written") || strings.Contains(lower, "received no packets") {
		return "编码未产出数据，编码器初始化或字幕渲染崩溃"
	}

	display := tail
	if len(display) > 400 {
		display = display[len(display)-400:]
	}
	for _, line := range strings.Split(display, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 20 && !strings.HasPrefix(line, "frame=") && !strings.HasPrefix(line, "size=") {
			return line
		}
	}
	if display != "" {
		return strings.TrimSpace(display)
	}
	return "编码出错"
}

func friendlyDuration(sec float64) string {
	total := int(sec)
	if total < 60 {
		return fmt.Sprintf("%d 秒", total)
	}
	m, s := total/60, total%60
	if m < 60 {
		if s == 0 {
			return fmt.Sprintf("%d 分钟", m)
		}
		return fmt.Sprintf("%d 分 %d 秒", m, s)
	}
	h := m / 60
	m = m % 60
	if m == 0 && s == 0 {
		return fmt.Sprintf("%d 小时", h)
	}
	if s == 0 {
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	}
	return fmt.Sprintf("%d 小时 %d 分 %d 秒", h, m, s)
}

func friendlyFileSize(bytes int64) string {
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func decodeTextFile(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return string(data[3:])
	}
	if len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE {
		return utf16BytesToString(data[2:], false)
	}
	if len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF {
		return utf16BytesToString(data[2:], true)
	}
	if !utf8.Valid(data) {
		dec := simplifiedchinese.GBK.NewDecoder()
		if out, _, err := transform.Bytes(dec, data); err == nil && utf8.Valid(out) {
			return string(out)
		}
	}
	return string(data)
}

func utf16BytesToString(b []byte, bigEndian bool) string {
	if len(b) == 0 {
		return ""
	}
	if len(b)%2 == 1 {
		b = b[:len(b)-1]
	}
	n := len(b) / 2
	u := make([]uint16, n)
	if bigEndian {
		for i := 0; i < n; i++ {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		}
	} else {
		for i := 0; i < n; i++ {
			u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
		}
	}
	return string(utf16.Decode(u))
}

type SubtitleEntry struct {
	Index    int      `json:"index"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Text     string   `json:"text"`
	Format   string   `json:"format"`
	AssParts []string `json:"assParts"`
	FPS      float64  `json:"fps,omitempty"`
}

var reSubFPSDecl = regexp.MustCompile(`^\{(\d+)\}\{(\d+)\}(\d+(?:\.\d+)?)\s*$`)

func subDeclaredFPS(line string) (float64, bool) {
	m := reSubFPSDecl.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return 0, false
	}
	s, _ := strconv.Atoi(m[1])
	e, _ := strconv.Atoi(m[2])
	if s != e || s > 1 {
		return 0, false
	}
	fps, err := strconv.ParseFloat(m[3], 64)
	if err != nil || fps < 10 || fps > 120 {
		return 0, false
	}
	return fps, true
}

func (a *App) LoadSubtitle(filePath string) []SubtitleEntry {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == ".sup" {
		a.log("warn", "SUP/PGS 为图形字幕，无法编辑，仅可用于封装")
		return nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		a.log("error", "读取字幕失败: "+err.Error())
		return nil
	}
	content := strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n")
	if ext == ".ass" || ext == ".ssa" {
		return parseASSEntries(content)
	}
	if ext == ".vtt" {
		return parseVTTEntries(content)
	}
	if ext == ".sub" {
		return parseSubEntries(content)
	}
	if ext == ".ttml" {
		return parseTTMLEntries(content)
	}
	return parseSRTEntries(content)
}

func parseSRTEntries(content string) []SubtitleEntry {
	var entries []SubtitleEntry
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	blocks := strings.Split(content, "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		tsIdx := -1
		for i, l := range lines {
			if strings.Contains(l, " --> ") {
				tsIdx = i
				break
			}
		}
		if tsIdx < 0 {
			continue
		}
		parts := strings.SplitN(lines[tsIdx], " --> ", 2)
		if len(parts) != 2 {
			continue
		}
		text := strings.Join(lines[tsIdx+1:], "\n")
		var idx int
		if tsIdx > 0 {
			fmt.Sscanf(lines[tsIdx-1], "%d", &idx)
		}
		if idx == 0 {
			idx = len(entries) + 1
		}
		entries = append(entries, SubtitleEntry{
			Index:  idx,
			Start:  strings.TrimSpace(parts[0]),
			End:    strings.TrimSpace(parts[1]),
			Text:   strings.TrimSpace(text),
			Format: "srt",
		})
	}
	return entries
}

func parseVTTEntries(content string) []SubtitleEntry {
	var entries []SubtitleEntry
	lines := strings.Split(content, "\n")
	i := 0
	for i < len(lines) && !strings.Contains(lines[i], "-->") {
		i++
	}
	for i < len(lines) {
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
		if i >= len(lines) {
			break
		}
		var idx int
		tsLine := -1
		if strings.Contains(lines[i], "-->") {
			tsLine = i
		} else {
			fmt.Sscanf(strings.TrimSpace(lines[i]), "%d", &idx)
			if i+1 < len(lines) && strings.Contains(lines[i+1], "-->") {
				tsLine = i + 1
			}
		}
		if tsLine < 0 || tsLine >= len(lines) {
			break
		}
		parts := strings.SplitN(lines[tsLine], " --> ", 2)
		if len(parts) != 2 {
			i++
			continue
		}
		var textLines []string
		j := tsLine + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) != "" {
			textLines = append(textLines, lines[j])
			j++
		}
		if idx == 0 {
			idx = len(entries) + 1
		}
		entries = append(entries, SubtitleEntry{
			Index:  idx,
			Start:  strings.TrimSpace(parts[0]),
			End:    strings.TrimSpace(parts[1]),
			Text:   strings.TrimSpace(strings.Join(textLines, "\n")),
			Format: "vtt",
		})
		i = j
	}
	return entries
}

func parseSubEntries(content string) []SubtitleEntry {
	var entries []SubtitleEntry
	idx := 1
	fps := float64(subDefaultFPS)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		if len(entries) == 0 {
			if f, ok := subDeclaredFPS(line); ok {
				fps = f
				continue
			}
		}
		var startF, endF int
		n, err := fmt.Sscanf(line, "{%d}{%d}", &startF, &endF)
		if err != nil || n != 2 {
			continue
		}
		pipeIdx := strings.Index(line, "}")
		if pipeIdx < 0 {
			continue
		}
		rest := line[pipeIdx+1:]
		pipeIdx2 := strings.Index(rest, "}")
		if pipeIdx2 < 0 {
			continue
		}
		text := rest[pipeIdx2+1:]
		entries = append(entries, SubtitleEntry{
			Index:  idx,
			Start:  msToSubTime(subFramesToMs(startF, fps), "sub"),
			End:    msToSubTime(subFramesToMs(endF, fps), "sub"),
			Text:   strings.TrimSpace(text),
			Format: "sub",
			FPS:    fps,
		})
		idx++
	}
	return entries
}

func parseTTMLEntries(content string) []SubtitleEntry {
	var entries []SubtitleEntry
	idx := 1
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "<p ") || !strings.Contains(line, "begin=") {
			continue
		}
		beginIdx := strings.Index(line, "begin=\"")
		if beginIdx < 0 {
			continue
		}
		beginIdx += 7
		endIdx := strings.Index(line[beginIdx:], "\"")
		if endIdx < 0 {
			continue
		}
		beginTime := line[beginIdx : beginIdx+endIdx]

		endAttrIdx := strings.Index(line[beginIdx+endIdx:], "end=\"")
		if endAttrIdx < 0 {
			continue
		}
		endAttrIdx += beginIdx + endIdx + 5
		endEndIdx := strings.Index(line[endAttrIdx:], "\"")
		if endEndIdx < 0 {
			continue
		}
		endTime := line[endAttrIdx : endAttrIdx+endEndIdx]

		textStart := strings.Index(line, ">")
		if textStart < 0 {
			continue
		}
		textEnd := strings.LastIndex(line, "</p>")
		var text string
		if textEnd > textStart {
			text = line[textStart+1 : textEnd]
		} else {
			textEnd = strings.LastIndex(line, "/>")
			if textEnd > textStart {
				text = ""
			} else {
				text = line[textStart+1:]
			}
		}
		text = strings.TrimSpace(text)

		entries = append(entries, SubtitleEntry{
			Index:  idx,
			Start:  formatTTMLTime(parseTTMLTimeSec(beginTime, 0)),
			End:    formatTTMLTime(parseTTMLTimeSec(endTime, 0)),
			Text:   text,
			Format: "ttml",
		})
		idx++
	}
	return entries
}

func parseASSEntries(content string) []SubtitleEntry {
	var entries []SubtitleEntry
	idx := 1
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "Dialogue:") {
			continue
		}
		rest := strings.TrimSpace(line[len("Dialogue:"):])
		p := strings.SplitN(rest, ",", 10)
		if len(p) != 10 {
			continue
		}
		entries = append(entries, SubtitleEntry{
			Index:    idx,
			Start:    p[1],
			End:      p[2],
			Text:     p[9],
			Format:   "ass",
			AssParts: []string{p[0], p[3], p[4], p[5], p[6], p[7], p[8]},
		})
		idx++
	}
	return entries
}

func (a *App) SaveSubtitle(originalFilePath, outputFilePath string, entries []SubtitleEntry) bool {
	if len(entries) == 0 {
		a.log("error", "字幕内容为空")
		return false
	}
	if err := os.MkdirAll(filepath.Dir(outputFilePath), 0755); err != nil {
		a.log("error", "创建目录失败")
		return false
	}
	var content string
	if entries[0].Format == "ass" || entries[0].Format == "ssa" {
		origData, err := os.ReadFile(originalFilePath)
		if err != nil {
			a.log("error", "读取原始文件失败: "+err.Error())
			return false
		}
		origLines := strings.Split(strings.ReplaceAll(decodeTextFile(origData), "\r\n", "\n"), "\n")
		formatDlg := func(e SubtitleEntry) string {
			return fmt.Sprintf("Dialogue: %s,%s,%s,%s,%s,%s,%s,%s,%s,%s",
				e.AssParts[0], e.Start, e.End,
				e.AssParts[1], e.AssParts[2], e.AssParts[3],
				e.AssParts[4], e.AssParts[5], e.AssParts[6], e.Text)
		}
		out := make([]string, 0, len(origLines)+4)
		eIdx := 0
		lastDlgOut := -1
		for _, line := range origLines {
			if !strings.HasPrefix(line, "Dialogue:") {
				out = append(out, line)
				continue
			}
			if eIdx >= len(entries) {
				continue
			}
			e := entries[eIdx]
			if len(e.AssParts) >= 7 {
				out = append(out, formatDlg(e))
			} else {
				out = append(out, line)
			}
			lastDlgOut = len(out) - 1
			eIdx++
		}
		if eIdx < len(entries) {
			var extra []string
			for _, e := range entries[eIdx:] {
				if len(e.AssParts) >= 7 {
					extra = append(extra, formatDlg(e))
				}
			}
			insertAt := lastDlgOut + 1
			if lastDlgOut < 0 {
				insertAt = len(out)
			}
			rebuilt := make([]string, 0, len(out)+len(extra))
			rebuilt = append(rebuilt, out[:insertAt]...)
			rebuilt = append(rebuilt, extra...)
			rebuilt = append(rebuilt, out[insertAt:]...)
			out = rebuilt
		}
		content = strings.Join(out, "\n")
	} else if entries[0].Format == "vtt" {
		var sb strings.Builder
		sb.WriteString("WEBVTT\n\n")
		for i, e := range entries {
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
		}
		content = strings.TrimRight(sb.String(), "\n") + "\n"
	} else if entries[0].Format == "sub" {
		fps := entries[0].FPS
		if fps <= 0 {
			fps = subDefaultFPS
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "{1}{1}%g\n", fps)
		for _, e := range entries {
			startF := subMsToFrames(subTimeToMs(e.Start, e.Format), fps)
			endF := subMsToFrames(subTimeToMs(e.End, e.Format), fps)
			fmt.Fprintf(&sb, "{%d}{%d}%s\n", startF, endF, e.Text)
		}
		content = strings.TrimRight(sb.String(), "\n") + "\n"
	} else if entries[0].Format == "ttml" {
		var sb strings.Builder
		sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
		sb.WriteString("<tt xmlns=\"http://www.w3.org/ns/ttml\">\n")
		sb.WriteString("  <body>\n")
		sb.WriteString("    <div>\n")
		for _, e := range entries {
			fmt.Fprintf(&sb, "      <p begin=\"%s\" end=\"%s\">%s</p>\n", e.Start, e.End, e.Text)
		}
		sb.WriteString("    </div>\n")
		sb.WriteString("  </body>\n")
		sb.WriteString("</tt>\n")
		content = sb.String()
	} else {
		var sb strings.Builder
		for i, e := range entries {
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, e.Start, e.End, e.Text)
		}
		content = strings.TrimRight(sb.String(), "\n") + "\n"
	}
	if err := os.WriteFile(outputFilePath, []byte(content), 0644); err != nil {
		a.log("error", "保存失败: "+err.Error())
		return false
	}
	a.log("ok", "字幕已保存 → "+filepath.Base(outputFilePath))
	return true
}

func (a *App) DetectGPU() string {
	cmd := exec.Command("wmic", "path", "win32_VideoController", "get", "Name,DriverVersion", "/format:list")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	content := strings.ReplaceAll(decodeGBK(out), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	var result []string
	var name, driver string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "Name=") {
			name = strings.TrimSpace(strings.TrimPrefix(l, "Name="))
		} else if strings.HasPrefix(l, "DriverVersion=") {
			driver = strings.TrimSpace(strings.TrimPrefix(l, "DriverVersion="))
		}
		if name != "" && driver != "" {
			result = append(result, name+" | 驱动 "+driver)
			name, driver = "", ""
		}
	}
	if name != "" || driver != "" {
		result = append(result, name+" | 驱动 "+driver)
	}
	return strings.Join(result, "\n")
}

func probeEncoder(encName string) int {
	code, _ := probeEncoderDetail(encName)
	return code
}

func probeEncoderDetail(encName string) (int, string) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=black:s=320x240:r=1",
		"-frames:v", "1",
		"-c:v", encName,
		"-f", "null", "-")
	hideWindow(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return 0, ""
	}
	errStr := strings.TrimSpace(stderr.String())
	errLower := strings.ToLower(errStr)
	if strings.Contains(errLower, "unknown encoder") ||
		strings.Contains(errLower, "no such encoder") ||
		(strings.Contains(errLower, "encoder") && strings.Contains(errLower, "not found")) {
		return 1, errStr
	}
	if strings.Contains(errLower, "no capable devices") ||
		strings.Contains(errLower, "nvenc") ||
		strings.Contains(errLower, "amf") ||
		strings.Contains(errLower, "qsv") ||
		strings.Contains(errLower, "cannot load") ||
		strings.Contains(errLower, "initialization failed") ||
		strings.Contains(errLower, "driver") {
		return 2, errStr
	}
	return 3, errStr
}

func (a *App) DetectEncoders() map[string]bool {
	encs := []string{
		"libx264", "libx265",
		"h264_nvenc", "hevc_nvenc",
		"h264_amf", "hevc_amf",
		"h264_qsv", "hevc_qsv",
		"libsvtav1",
		"av1_nvenc", "av1_amf", "av1_qsv",
	}
	result := map[string]bool{}
	for _, enc := range encs {
		result[enc] = probeEncoder(enc) == 0
	}
	return result
}

func (a *App) DiagnoseEncoders() map[string]string {
	gpuEncs := []string{
		"h264_nvenc", "hevc_nvenc", "av1_nvenc",
		"h264_amf", "hevc_amf", "av1_amf",
		"h264_qsv", "hevc_qsv", "av1_qsv",
	}
	result := map[string]string{}
	for _, enc := range gpuEncs {
		code, errStr := probeEncoderDetail(enc)
		if code != 0 {
			result[enc] = fmt.Sprintf("%d|%s", code, errStr)
		}
	}
	return result
}

func (a *App) GetFFmpegInfo() string {
	cmd := exec.Command("ffmpeg", "-version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "FFmpeg 不可用"
	}
	lines := strings.SplitN(strings.TrimSpace(decodeGBK(out)), "\n", 2)
	return strings.TrimSpace(lines[0])
}

func (a *App) ReadSubtitlePreview(file string) []string {
	if file == "" {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	content := decodeTextFile(data)
	ext := strings.ToLower(filepath.Ext(file))
	var lines []string

	if ext == ".ass" || ext == ".ssa" {
		for _, m := range reASSDialogue.FindAllStringSubmatch(content, 20) {
			text := reASSBraces.ReplaceAllString(m[1], "")
			text = strings.ReplaceAll(text, `\N`, " ")
			text = strings.TrimSpace(text)
			if text != "" {
				lines = append(lines, text)
				if len(lines) >= 6 {
					break
				}
			}
		}
	} else {
		scanner := bufio.NewScanner(strings.NewReader(content))
		inText := false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.Contains(line, "-->") {
				inText = true
				continue
			}
			if line == "" {
				inText = false
				continue
			}
			if inText {
				lines = append(lines, line)
				if len(lines) >= 6 {
					break
				}
			}
		}
	}
	return lines
}

type subtitlePreviewPoint struct {
	Label string
	Sec   float64
	Index int
	Text  string
}

func (a *App) GenerateSubtitlePreview(opts ProcessOptions) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if opts.InputDir == "" {
		a.log("error", "未设置输入目录")
		return false
	}
	if opts.OutputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}
	videoFiles := findFilesRecursive(opts.InputDir, videoExts)
	subtitleFiles := findFilesRecursive(opts.InputDir, subtitleExts)
	if len(videoFiles) == 0 {
		a.log("error", "未找到视频文件")
		return false
	}
	if len(subtitleFiles) == 0 {
		a.log("error", "未找到字幕文件")
		return false
	}
	videoFiles = dedupBasenames(videoFiles, func(kept, dropped string) {
		a.log("warn", "同名跳过: "+filepath.Base(dropped))
	})
	pairs := pairVideosAndSubsByDir(videoFiles, subtitleFiles, false, nil)
	if len(pairs) == 0 {
		a.log("error", "无法配对视频和字幕")
		return false
	}
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败: "+err.Error())
		return false
	}
	ok := true
	a.log("info", fmt.Sprintf("字幕预览: 共 %d 组，输出到 %s", len(pairs), opts.OutputDir))
	for _, p := range pairs {
		if a.stopped.Load() {
			a.log("warn", "字幕预览已取消")
			return false
		}
		if len(p.subtitles) == 0 {
			continue
		}
		if !a.generateSubtitlePreviewSingle(p.video, p.subtitles[0], opts) {
			ok = false
		}
	}
	return ok
}

func (a *App) generateSubtitlePreviewSingle(videoFile, subtitleFile string, opts ProcessOptions) bool {
	info, err := getVideoInfo(videoFile)
	if err != nil {
		a.log("error", "无法读取视频信息: "+filepath.Base(videoFile)+" - "+err.Error())
		return false
	}
	stem := cleanFilename(strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile)))
	outDir := filepath.Join(opts.OutputDir, stem+".字幕预览")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		a.log("error", "创建字幕预览目录失败: "+err.Error())
		return false
	}
	originalVideoFile := videoFile
	subtitleForRender := subtitleFile
	var tempFiles []string
	defer func() {
		for _, f := range tempFiles {
			os.Remove(f)
		}
	}()
	if tp := a.preprocessSubtitleForMux(subtitleFile); tp != "" {
		subtitleForRender = tp
		tempFiles = append(tempFiles, tp)
	}
	ext := strings.ToLower(filepath.Ext(subtitleForRender))
	isBitmapSub := ext == ".sup" || ext == ".idx" || ext == ".mks"
	subEmbedded := false
	if isBitmapSub {
		a.log("info", "图形字幕预览: 跳过整片预合成，改用采样点小片段生成预览")
	}
	points := a.pickSubtitlePreviewPoints(subtitleForRender, info.Duration)
	a.log("info", fmt.Sprintf("字幕预览 %s · %d 张", filepath.Base(originalVideoFile), len(points)))
	var made []string
	var failed []string
	for i, pt := range points {
		if a.stopped.Load() {
			return false
		}
		name := fmt.Sprintf("%02d_%s_%s.jpg", i+1, sanitizePreviewName(pt.Label), secondsToFileTime(pt.Sec))
		outFile := filepath.Join(outDir, name)
		a.log("info", fmt.Sprintf("截图 %d/%d %s %s", i+1, len(points), pt.Label, friendlyDuration(pt.Sec)))
		if a.renderSubtitlePreviewFrame(videoFile, subtitleForRender, outFile, pt.Sec, opts, info, isBitmapSub, subEmbedded, outDir) {
			made = append(made, name)
		} else {
			failed = append(failed, name)
		}
	}
	report := a.buildSubtitlePreviewReport(originalVideoFile, subtitleFile, points, made, failed)
	reportPath := filepath.Join(outDir, "字幕预览报告.txt")
	if err := os.WriteFile(reportPath, []byte(report), 0644); err != nil {
		a.log("warn", "写入预览报告失败: "+err.Error())
	}
	if len(made) == 0 {
		a.log("error", "字幕预览生成失败: "+filepath.Base(subtitleFile))
		return false
	}
	a.log("ok", fmt.Sprintf("字幕预览 %s · %d 张", filepath.Base(outDir), len(made)))
	return true
}

func (a *App) pickSubtitlePreviewPoints(subtitleFile string, duration float64) []subtitlePreviewPoint {
	ext := strings.ToLower(filepath.Ext(subtitleFile))
	if ext == ".sup" || ext == ".idx" || ext == ".mks" {
		if points := pickBitmapSubtitlePreviewPoints(subtitleFile, duration); len(points) > 0 {
			return points
		}
	}
	entries := a.LoadSubtitle(subtitleFile)
	var points []subtitlePreviewPoint
	used := make(map[int]bool)
	addEntry := func(label string, idx int) {
		if idx < 0 || idx >= len(entries) || used[idx] {
			return
		}
		e := entries[idx]
		startMs := subTimeToMs(e.Start, e.Format)
		endMs := subTimeToMs(e.End, e.Format)
		sec := float64(startMs)/1000 + 0.35
		if endMs > startMs {
			mid := float64(startMs+endMs) / 2000
			if mid > sec {
				sec = mid
			}
		}
		points = append(points, subtitlePreviewPoint{Label: label, Sec: clampPreviewSecond(sec, duration), Index: e.Index, Text: previewTextOneLine(e.Text)})
		used[idx] = true
	}
	if len(entries) > 0 {
		addEntry("片头字幕", 0)
		addEntry("前段字幕", len(entries)/5)
		addEntry("中前段字幕", len(entries)*2/5)
		addEntry("中段字幕", len(entries)/2)
		addEntry("中后段字幕", len(entries)*3/5)
		addEntry("后段字幕", len(entries)*4/5)
		addEntry("片尾字幕", len(entries)-1)
		for i, e := range entries {
			if strings.TrimSpace(e.Text) == "" {
				addEntry("异常检查_空白", i)
				break
			}
		}
		for i, e := range entries {
			startMs := subTimeToMs(e.Start, e.Format)
			endMs := subTimeToMs(e.End, e.Format)
			if endMs > startMs && endMs-startMs < 500 {
				addEntry("异常检查_过短", i)
				break
			}
		}
		for i, e := range entries {
			for _, line := range strings.Split(e.Text, "\n") {
				if len([]rune(line)) > 60 {
					addEntry("异常检查_长句", i)
					break
				}
			}
			if len(points) >= 10 {
				break
			}
		}
		for i := 0; i+1 < len(entries); i++ {
			if subTimeToMs(entries[i].End, entries[i].Format) > subTimeToMs(entries[i+1].Start, entries[i+1].Format) {
				addEntry("异常检查_重叠", i)
				break
			}
		}
	}
	if duration <= 0 {
		duration = 60
	}
	if len(points) < 3 {
		for _, item := range []struct {
			label string
			ratio float64
		}{{"片头画面", 0.08}, {"前段画面", 0.25}, {"中段画面", 0.50}, {"后段画面", 0.75}, {"片尾画面", 0.92}} {
			points = append(points, subtitlePreviewPoint{Label: item.label, Sec: clampPreviewSecond(duration*item.ratio, duration)})
		}
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].Sec < points[j].Sec })
	if len(points) > 12 {
		points = points[:12]
	}
	return points
}

func pickBitmapSubtitlePreviewPoints(subtitleFile string, duration float64) []subtitlePreviewPoint {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "s:0",
		"-show_entries", "packet=pts_time,dts_time",
		"-of", "csv=p=0",
		subtitleFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var times []float64
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, field := range strings.Split(line, ",") {
			field = strings.TrimSpace(field)
			if field == "" || strings.EqualFold(field, "N/A") {
				continue
			}
			if sec, err := strconv.ParseFloat(field, 64); err == nil && sec > 0 {
				if duration <= 0 || sec < duration {
					times = append(times, sec+0.2)
					break
				}
			}
		}
	}
	if len(times) == 0 {
		return nil
	}
	sort.Float64s(times)
	var dedup []float64
	last := -999.0
	for _, t := range times {
		if t-last >= 8 {
			dedup = append(dedup, t)
			last = t
		}
	}
	if len(dedup) == 0 {
		return nil
	}
	pickIdx := []int{0, len(dedup) / 5, len(dedup) * 2 / 5, len(dedup) / 2, len(dedup) * 3 / 5, len(dedup) * 4 / 5, len(dedup) - 1}
	labels := []string{"图形字幕_片头", "图形字幕_前段", "图形字幕_中前段", "图形字幕_中段", "图形字幕_中后段", "图形字幕_后段", "图形字幕_片尾"}
	used := make(map[int]bool)
	var points []subtitlePreviewPoint
	for i, idx := range pickIdx {
		if idx < 0 || idx >= len(dedup) || used[idx] {
			continue
		}
		used[idx] = true
		points = append(points, subtitlePreviewPoint{
			Label: labels[i],
			Sec:   clampPreviewSecond(dedup[idx], duration),
		})
	}
	return points
}

func (a *App) renderSubtitlePreviewFrame(videoFile, subtitleFile, outFile string, sec float64, opts ProcessOptions, info *videoInfo, isBitmapSub, subEmbedded bool, tempDir string) bool {
	var args []string
	ss := fmt.Sprintf("%.3f", sec)
	subDir := filepath.Dir(subtitleFile)
	if isBitmapSub {
		if !subEmbedded {
			return a.renderBitmapSubtitlePreviewFrame(videoFile, subtitleFile, outFile, sec, opts, info, tempDir)
		}
		subRef := "[0:s:0]"
		postParts := []string{}
		if opts.ScaleHeight > 0 {
			postParts = append(postParts, fmt.Sprintf("scale=-2:%d", opts.ScaleHeight))
		} else {
			postParts = append(postParts, "scale='min(1280,iw)':-2")
		}
		if u := brandURL(); u != "" {
			postParts = append(postParts, previewDrawtext(u, "x=w-tw-42:y=36:fontsize=38:fontcolor=white@0.48"))
		}
		filter := fmt.Sprintf("[0:v]%soverlay[_ovr];[_ovr]%s[vout]", subRef, strings.Join(postParts, ","))
		args = []string{"-y", "-hide_banner", "-loglevel", "error"}
		if subEmbedded {
			args = append(args, embeddedSubCanvasArgs(info)...)
		}
		args = append(args, "-ss", ss, "-i", videoFile)
		if !subEmbedded {
			args = append(args, externalSubCanvasArgs(info, subtitleFile)...)
			args = append(args, "-ss", ss, "-i", subtitleFile)
		}
		args = append(args, "-filter_complex", filter, "-map", "[vout]", "-frames:v", "1", "-q:v", "2", outFile)
	} else {
		seekStart := sec - 3
		if seekStart < 0 {
			seekStart = 0
		}
		localSec := sec - seekStart
		var localSub string
		ext := strings.ToLower(filepath.Ext(subtitleFile))
		if ext != ".ass" && ext != ".ssa" {
			localSub = a.buildLocalTextSubtitleForPreview(subtitleFile, seekStart, 8, tempDir)
		}
		if localSub != "" {
			defer os.Remove(localSub)
			subtitleFile = localSub
			subDir = filepath.Dir(localSub)
			ss = fmt.Sprintf("%.3f", localSec)
		}
		safeName := escapeFFmpegPath(filepath.Base(subtitleFile))
		var subFilter string
		if ext == ".ass" || ext == ".ssa" {
			subFilter = fmt.Sprintf("ass='%s'", safeName) + assFontsDirOpt()
		} else {
			subFilter = fmt.Sprintf("subtitles='%s':force_style='FontName=Microsoft YaHei,FontSize=16,PrimaryColour=&H00FFFFFF,OutlineColour=&H40000000,Outline=1,Shadow=0,MarginV=20,BorderStyle=1'", safeName)
		}
		parts := []string{subFilter}
		if opts.ScaleHeight > 0 {
			parts = append(parts, fmt.Sprintf("scale=-2:%d", opts.ScaleHeight))
		} else {
			parts = append(parts, "scale='min(1280,iw)':-2")
		}
		if u := brandURL(); u != "" {
			parts = append(parts, previewDrawtext(u, "x=w-tw-42:y=36:fontsize=38:fontcolor=white@0.48"))
		}
		if localSub != "" {
			args = []string{"-y", "-hide_banner", "-loglevel", "error", "-ss", fmt.Sprintf("%.3f", seekStart), "-i", videoFile, "-vf", strings.Join(parts, ","), "-ss", ss, "-frames:v", "1", "-q:v", "2", outFile}
		} else {
			args = []string{"-y", "-hide_banner", "-loglevel", "error", "-copyts", "-ss", fmt.Sprintf("%.3f", seekStart), "-i", videoFile, "-vf", strings.Join(parts, ","), "-ss", ss, "-frames:v", "1", "-q:v", "2", outFile}
		}
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Dir = subDir
	hideWindow(cmd)
	out, err := a.runCancellableCmd(cmd)
	if err == nil {
		return true
	}
	errStr := strings.TrimSpace(decodeGBK(out))
	if len(errStr) > 220 {
		errStr = errStr[len(errStr)-220:]
	}
	a.log("warn", fmt.Sprintf("预览截图失败 %.1fs: %s", sec, errStr))
	return false
}

func (a *App) renderBitmapSubtitlePreviewFrame(videoFile, subtitleFile, outFile string, sec float64, opts ProcessOptions, info *videoInfo, tempDir string) bool {
	clipDuration := 12.0
	start := sec - 8
	if start < 0 {
		start = 0
	}
	localSec := sec - start
	stem := sanitizePreviewName(strings.TrimSuffix(filepath.Base(outFile), filepath.Ext(outFile)))
	clipFile := filepath.Join(tempDir, stem+".pgs-preview.mkv")
	defer os.Remove(clipFile)

	startStr := fmt.Sprintf("%.3f", start)
	durationStr := fmt.Sprintf("%.3f", clipDuration)
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-ss", startStr, "-t", durationStr, "-i", videoFile}
	args = append(args, externalSubCanvasArgs(info, subtitleFile)...)
	args = append(args, "-i", subtitleFile, "-map", "0:v:0", "-map", "1:s:0", "-c:v", "copy", "-c:s", "copy", "-shortest", "-fflags", "+genpts", clipFile)
	cmd := exec.Command("ffmpeg", args...)
	hideWindow(cmd)
	if out, err := a.runCancellableCmd(cmd); err != nil {
		errStr := strings.TrimSpace(decodeGBK(out))
		if len(errStr) > 220 {
			errStr = errStr[len(errStr)-220:]
		}
		a.log("warn", fmt.Sprintf("图形字幕预览小片段生成失败 %.1fs: %s", sec, errStr))
		return false
	}

	clipInfo := info
	if clipInfo != nil {
		copied := *clipInfo
		copied.Duration = clipDuration
		clipInfo = &copied
	}
	return a.renderSubtitlePreviewFrame(clipFile, subtitleFile, outFile, localSec, opts, clipInfo, true, true, tempDir)
}

func (a *App) buildLocalTextSubtitleForPreview(subtitleFile string, startSec, durationSec float64, tempDir string) string {
	entries := a.LoadSubtitle(subtitleFile)
	if len(entries) == 0 {
		return ""
	}
	startMs := int(startSec * 1000)
	endMs := int((startSec + durationSec) * 1000)
	var sb strings.Builder
	idx := 1
	for _, e := range entries {
		es := subTimeToMs(e.Start, e.Format)
		ee := subTimeToMs(e.End, e.Format)
		if ee < startMs || es > endMs {
			continue
		}
		ls := es - startMs
		le := ee - startMs
		if ls < 0 {
			ls = 0
		}
		if le <= ls {
			le = ls + 800
		}
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", idx, msToSubTime(ls, "srt"), msToSubTime(le, "srt"), text))
		idx++
	}
	if idx == 1 {
		return ""
	}
	base := sanitizePreviewName(strings.TrimSuffix(filepath.Base(subtitleFile), filepath.Ext(subtitleFile)))
	tmp := filepath.Join(tempDir, fmt.Sprintf("%s.preview-%d.srt", base, startMs))
	if err := os.WriteFile(tmp, []byte(sb.String()), 0644); err != nil {
		return ""
	}
	return tmp
}

func (a *App) buildSubtitlePreviewReport(videoFile, subtitleFile string, points []subtitlePreviewPoint, made, failed []string) string {
	var sb strings.Builder
	sb.WriteString("Muxer 字幕预览报告\n")
	sb.WriteString("─────────────────────────────────\n")
	sb.WriteString("视频文件: " + filepath.Base(videoFile) + "\n")
	sb.WriteString("字幕文件: " + filepath.Base(subtitleFile) + "\n")
	sb.WriteString("生成时间: " + time.Now().Format("2006-01-02 15:04:05") + "\n")
	sb.WriteString(fmt.Sprintf("采样数量: %d 张成功 / %d 张失败\n", len(made), len(failed)))
	sb.WriteString("采样方式: 优先按字幕时间轴抽样，图形字幕无法解析时按视频比例抽样\n\n")
	sb.WriteString("截图清单:\n")
	for _, name := range made {
		sb.WriteString(" - " + name + "\n")
	}
	if len(failed) > 0 {
		sb.WriteString("\n失败截图:\n")
		for _, name := range failed {
			sb.WriteString(" - " + name + "\n")
		}
	}
	sb.WriteString("\n采样点:\n")
	for _, pt := range points {
		line := fmt.Sprintf(" - %s %s", pt.Label, friendlyDuration(pt.Sec))
		if pt.Index > 0 {
			line += fmt.Sprintf(" 第%d条", pt.Index)
		}
		if pt.Text != "" {
			line += " " + pt.Text
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n说明: 本报告用于压制前快速确认字幕时间轴、文字内容、显示样式及烧录效果；截图为抽样预览，不代表完整逐帧校对。\n")
	return sb.String()
}

func clampPreviewSecond(sec, duration float64) float64 {
	if sec < 0.2 {
		sec = 0.2
	}
	if duration > 1 && sec > duration-0.5 {
		sec = duration - 0.5
	}
	return sec
}

func sanitizePreviewName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = "字幕预览"
	}
	repl := strings.NewReplacer("\\", "-", "/", "-", ":", "-", "*", "-", "?", "", "\"", "", "<", "《", ">", "》", "|", "-")
	return repl.Replace(s)
}

func previewTextOneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = reASSBraces.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 36 {
		return string(r[:36]) + "…"
	}
	return s
}

func previewDrawtext(text, style string) string {
	return fmt.Sprintf("drawtext=text='%s':%s:shadowcolor=black@0.5:shadowx=2:shadowy=2%s",
		escapeDrawtext(text), style, drawtextFontOpt(text))
}

func shiftTextSubtitleFile(subtitleFile string, offsetSec float64) (string, error) {
	ext := strings.ToLower(filepath.Ext(subtitleFile))
	if ext != ".srt" && ext != ".ass" && ext != ".ssa" {
		return "", fmt.Errorf("仅支持 SRT/ASS 偏移，当前为 %s", ext)
	}
	data, err := os.ReadFile(subtitleFile)
	if err != nil {
		return "", err
	}
	result := shiftSubtitleContent(decodeTextFile(data), ext, offsetSec)
	dir := filepath.Dir(subtitleFile)
	stem := strings.TrimSuffix(filepath.Base(subtitleFile), ext)
	tmpPath := filepath.Join(dir, sanitizeTmpName(stem)+".muxer-shift"+ext)
	if err := os.WriteFile(tmpPath, []byte(result), 0644); err != nil {
		return "", err
	}
	return tmpPath, nil
}

func (a *App) ShiftSubtitleFile(inputFile, outputDir string, offsetSec float64) bool {
	if inputFile == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	ext := strings.ToLower(filepath.Ext(inputFile))
	if ext != ".srt" && ext != ".ass" && ext != ".ssa" {
		a.log("error", "仅支持 SRT / ASS 格式")
		return false
	}
	data, err := os.ReadFile(inputFile)
	if err != nil {
		a.log("error", "读取失败: "+err.Error())
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建目录失败")
		return false
	}
	result := shiftSubtitleContent(decodeTextFile(data), ext, offsetSec)
	sign := "+"
	if offsetSec < 0 {
		sign = ""
	}
	stem := strings.TrimSuffix(filepath.Base(inputFile), ext)
	outFile := filepath.Join(outputDir, fmt.Sprintf("%s.shifted(%s%.1fs)%s", stem, sign, offsetSec, ext))
	if err := os.WriteFile(outFile, []byte(result), 0644); err != nil {
		a.log("error", "写入失败: "+err.Error())
		return false
	}
	if ext == ".srt" {
		cleanSRTFile(outFile)
	}
	a.log("ok", "偏移 → "+filepath.Base(outFile))
	return true
}

func (a *App) ConcatVideos(files []string, outputDir, outputName string, copyMode bool, headAd, tailAd bool, adText string, dynAd bool) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if len(files) < 2 {
		a.log("error", "至少两个视频文件")
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

	if outputName == "" {
		stem := strings.TrimSuffix(filepath.Base(files[0]), filepath.Ext(files[0]))
		ext := filepath.Ext(files[0])
		suffix := ".merged"
		if t := brandTag(); t != "" {
			suffix = "." + strings.ToLower(t)
		}
		outputName = stem + suffix + ext
	}
	if !copyMode {
		extLower := strings.ToLower(filepath.Ext(outputName))
		incompat := map[string]bool{".avi": true, ".wmv": true, ".webm": true, ".flv": true, ".rmvb": true, ".rm": true}
		if incompat[extLower] {
			stem := strings.TrimSuffix(outputName, filepath.Ext(outputName))
			outputName = stem + ".mp4"
			a.log("warn", fmt.Sprintf("%s 不支持 H.265，改为 .mp4", extLower))
		}
	}
	outFile := filepath.Join(outputDir, outputName)
	outFile = a.safeOutputPath(outFile, files)
	outputName = filepath.Base(outFile)

	a.log("info", fmt.Sprintf("合并 %d 个文件 → %s", len(files), outputName))

	infos := make([]*videoInfo, len(files))
	var totalDuration float64
	for i, f := range files {
		fi, e := getVideoInfo(f)
		if e != nil {
			a.log("warn", "信息读取失败: "+filepath.Base(f))
			continue
		}
		infos[i] = fi
		if fi.Duration > 0 {
			totalDuration += fi.Duration
		}
	}
	refInfo := infos[0]
	if refInfo == nil {
		a.log("warn", "首文件信息读取失败")
		refInfo = &videoInfo{Height: 1080, VideoBitrateKbps: 8000}
	}
	if totalDuration <= 0 {
		totalDuration = 3600
	}
	a.log("info", "总时长 "+friendlyDuration(totalDuration))

	if copyMode {
		listFile := uniqueTempPath("muxer_concat_list", ".txt")
		var sb strings.Builder
		for _, f := range files {
			sb.WriteString(fmt.Sprintf("file '%s'\n", strings.ReplaceAll(f, "'", "\\'")))
		}
		if err := os.WriteFile(listFile, []byte(sb.String()), 0644); err != nil {
			a.log("error", "写入临时文件列表失败: "+err.Error())
			return false
		}
		defer os.Remove(listFile)

		a.log("info", "无损合并 concat")
		if dynAd && (headAd || tailAd) {
			a.log("warn", "无损合并 厂标不生效")
		}
		var noAudio []string
		for i, fi := range infos {
			if fi != nil && len(fi.AudioCodecs) == 0 {
				noAudio = append(noAudio, filepath.Base(files[i]))
			}
		}
		if len(noAudio) > 0 && len(noAudio) < len(files) {
			a.log("warn", fmt.Sprintf("无音频，可能音画不同步: %s", strings.Join(noAudio, "、")))
		}
		cmd := exec.Command("ffmpeg", "-y",
			"-f", "concat", "-safe", "0",
			"-i", listFile,
			"-c", "copy", outFile)
		hideWindow(cmd)
		errMsg, err := a.runWithProgress(cmd, totalDuration)
		if err != nil {
			os.Remove(outFile)
			if a.stopped.Load() {
				a.log("warn", "合并已取消")
				return false
			}
			a.log("error", "合并失败: "+friendlyErrorMsg(errMsg, "", false))
			return false
		}
	} else {

		cq := autoNvencCQ(refInfo.VideoBitrateKbps, refInfo.Height)
		a.log("info", fmt.Sprintf("CQ %d", cq))

		scaleH := autoNvencScaleHeight(refInfo.Height)
		if scaleH > 0 && scaleH != refInfo.Height {
			a.log("info", fmt.Sprintf("缩放 %dp → %dp", refInfo.Height, scaleH))
		} else {
			scaleH = 0
		}

		encArgs := []string{
			"-c:v", "hevc_nvenc",
			"-rc", "vbr", "-cq", fmt.Sprintf("%d", cq),
			"-b:v", "0", "-preset", "p4",
			"-spatial-aq", "1", "-aq-strength", "8",
		}
		encArgs = append(encArgs, nvencTuneArgs("hevc_nvenc")...)
		a.log("info", fmt.Sprintf("转码合并 NVENC H.265 CQ %d", cq))

		n := len(files)

		needSilence := make([]bool, n)
		anyMissingAudio := false
		audioCodecSet := make(map[string]bool)
		for i, fi := range infos {
			if fi == nil {
				continue
			}
			if len(fi.AudioCodecs) == 0 {
				if fi.Duration <= 0 {
					a.log("error", fmt.Sprintf("文件既无音频轨也无法读取时长，无法合并: %s", filepath.Base(files[i])))
					return false
				}
				needSilence[i] = true
				anyMissingAudio = true
				continue
			}
			audioCodecSet[fi.AudioCodecs[0]] = true
		}
		needAudioNorm := anyMissingAudio || len(audioCodecSet) > 1

		refW, refH := 0, 0
		if refInfo != nil {
			refW, refH = refInfo.Width, refInfo.Height
		}
		needVideoNorm := false
		if refW > 0 && refH > 0 {
			for _, fi := range infos {
				if fi != nil && fi.Width > 0 && fi.Height > 0 && (fi.Width != refW || fi.Height != refH) {
					needVideoNorm = true
					break
				}
			}
		}
		if anyMissingAudio {
			var names []string
			for i, need := range needSilence {
				if need {
					names = append(names, filepath.Base(files[i]))
				}
			}
			a.log("warn", fmt.Sprintf("补静音: %s", strings.Join(names, "、")))
		}
		if needVideoNorm {
			a.log("warn", fmt.Sprintf("分辨率不一致，统一为 %dx%d", refW, refH))
		}

		var normParts []string
		concatInputs := ""
		for i := 0; i < n; i++ {
			vLabel := fmt.Sprintf("%d:v", i)
			if needVideoNorm {
				normParts = append(normParts, fmt.Sprintf(
					"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1[nv%d]",
					i, refW, refH, refW, refH, i))
				vLabel = fmt.Sprintf("nv%d", i)
			}
			aLabel := fmt.Sprintf("%d:a", i)
			switch {
			case needSilence[i]:
				normParts = append(normParts, fmt.Sprintf(
					"anullsrc=channel_layout=stereo:sample_rate=48000,atrim=duration=%.3f,asetpts=PTS-STARTPTS[na%d]",
					infos[i].Duration, i))
				aLabel = fmt.Sprintf("na%d", i)
			case needAudioNorm:
				normParts = append(normParts, fmt.Sprintf(
					"[%d:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,asetpts=PTS-STARTPTS[na%d]",
					i, i))
				aLabel = fmt.Sprintf("na%d", i)
			}
			concatInputs += fmt.Sprintf("[%s][%s]", vLabel, aLabel)
		}
		concatFilter := concatInputs + fmt.Sprintf("concat=n=%d:v=1:a=1[cv][outa]", n)
		if len(normParts) > 0 {
			concatFilter = strings.Join(normParts, ";") + ";" + concatFilter
		}

		var vfParts []string

		if scaleH > 0 {
			vfParts = append(vfParts, fmt.Sprintf("scale=-2:%d", scaleH))
		}

		dynAdHeadPad, dynAdTailPad := 0.0, 0.0
		dynAdBumper := ""
		if dynAd && (headAd || tailAd) && totalDuration > 10 {
			if p, dur, err := writeDynAdBumper(); err == nil {
				defer os.Remove(p)
				dynAdBumper = p
				dynAdHeadPad, dynAdTailPad = dynAdPads(headAd, tailAd, dur)
				a.log("info", fmt.Sprintf("厂标 %s %.1fs", dynAdDescribe(headAd, tailAd), dynAdHeadPad+dynAdTailPad))
			} else {
				dynAd = false
				a.log("warn", "厂标跳过，改用文字标注: "+err.Error())
			}
		}

		outH := outputHeightOf(scaleH, refInfo)

		if (headAd || tailAd) && !dynAd && adText != "" && totalDuration > 10 {
			adEscaped := escapeDrawtext(adText)
			adFont := drawtextFontOpt(adText)
			adSize := scaledFontSize(42, outH)
			if headAd {
				headEnd := 8.0
				if headEnd > totalDuration-1 {
					headEnd = totalDuration - 1
				}
				dt := fmt.Sprintf("drawtext=text='%s':fontcolor=white:fontsize=%d:x=(w-tw)/2:y=36:shadowcolor=black@0.5:shadowx=2:shadowy=2:enable='between(t\\,1.0\\,%.1f)'%s",
					adEscaped, adSize, headEnd, adFont)
				vfParts = append(vfParts, dt)
				a.log("info", fmt.Sprintf("片头标注 %s %.1fs", adText, headEnd))
			}
			if tailAd {
				tailStart := totalDuration - 9
				if tailStart < 1 {
					tailStart = 1
				}
				tailEnd := tailStart + 8
				if tailEnd > totalDuration-0.5 {
					tailEnd = totalDuration - 0.5
				}
				dt := fmt.Sprintf("drawtext=text='%s':fontcolor=white:fontsize=%d:x=(w-tw)/2:y=36:shadowcolor=black@0.5:shadowx=2:shadowy=2:enable='between(t\\,%.1f\\,%.1f)'%s",
					adEscaped, adSize, tailStart, tailEnd, adFont)
				vfParts = append(vfParts, dt)
				a.log("info", fmt.Sprintf("片尾标注 %s %.1fs", adText, tailEnd-tailStart))
			}
		}

		if wmText := brandURL(); wmText != "" && totalDuration > 30 {
			wmCount := int(totalDuration/1500) + 1
			if wmCount < 2 {
				wmCount = 2
			}
			if wmCount > 4 {
				wmCount = 4
			}
			segTotal := totalDuration * 0.8
			segLen := segTotal / float64(wmCount)
			wmFont := drawtextFontOpt(wmText)
			wmSize := scaledFontSize(38, outH)
			wmEsc := escapeDrawtext(wmText)
			for i := 0; i < wmCount; i++ {
				wmDuration := float64(6 + rand.Intn(5))
				segStart := totalDuration*0.1 + float64(i)*segLen
				segEnd := segStart + segLen - wmDuration
				if segEnd < segStart {
					segEnd = segStart
				}
				wmStart := segStart + float64(rand.Intn(int(segEnd-segStart)+1))
				wmEnd := wmStart + wmDuration
				a.log("info", fmt.Sprintf("水印 %d/%d %s-%s", i+1, wmCount, friendlyDuration(wmStart), friendlyDuration(wmEnd)))
				dt := fmt.Sprintf("drawtext=text='%s':fontcolor=white@0.48:fontsize=%d:x=w-tw-42:y=36:shadowcolor=black@0.35:shadowx=1:shadowy=1:enable='between(t\\,%.1f\\,%.1f)'%s",
					wmEsc, wmSize, wmStart, wmEnd, wmFont)
				vfParts = append(vfParts, dt)
			}
		}

		if dynAdBumper != "" {
			if pad := dynAdPadFilter(dynAdHeadPad, dynAdTailPad); pad != "" {
				vfParts = append(vfParts, pad)
			}
		}

		vfParts = append(vfParts, "format=p010le")

		videoOut := "[outv]"
		if dynAdBumper != "" {
			videoOut = "[_dyncv]"
		}
		fullFilter := concatFilter + ";[cv]" + strings.Join(vfParts, ",") + videoOut
		if dynAdBumper != "" {
			fullFilter += ";" + dynAdOverlayStage("[_dyncv]", "outv", len(files), dynAdHeadPad, dynAdTailPad, totalDuration, outH, "")
		}

		audioMap := "[outa]"
		if apads := dynAdAudioPadFilters(dynAdHeadPad, dynAdTailPad); len(apads) > 0 {
			if dynAdBumper != "" {
				graph, aout, _ := dynAdBumperAudioGraph("outa", len(files), strings.Join(apads, ","), dynAdHeadPad, dynAdTailPad, totalDuration, false)
				fullFilter += ";" + graph
				audioMap = aout
			} else {
				fullFilter += ";[outa]" + strings.Join(apads, ",") + "[aout]"
				audioMap = "[aout]"
			}
		}

		args := []string{"ffmpeg", "-y", "-hwaccel", "cuda"}
		for _, f := range files {
			args = append(args, "-i", f)
		}
		if dynAdBumper != "" {
			args = append(args, "-i", dynAdBumper)
		}
		args = append(args,
			"-filter_complex", fullFilter,
			"-map", "[outv]", "-map", audioMap)
		args = append(args, encArgs...)
		args = append(args,
			"-c:a", "aac", "-b:a", "192k",
			outFile)
		cmd := exec.Command(args[0], args[1:]...)
		hideWindow(cmd)
		errMsg, err := a.runWithProgress(cmd, totalDuration+dynAdHeadPad+dynAdTailPad)
		if err != nil {
			os.Remove(outFile)
			if a.stopped.Load() {
				a.log("warn", "合并已取消")
				return false
			}
			a.log("error", "合并失败: "+friendlyErrorMsg(errMsg, "nvenc", true))
			return false
		}
	}

	a.log("ok", "合并 → "+filepath.Base(outFile))
	return true
}

func (a *App) ScanConcatDir(dir string) []string {
	files := findFiles(dir, videoExts)
	if len(files) == 0 {
		return nil
	}
	sort.SliceStable(files, func(i, j int) bool {
		ei := extractEpisode(filepath.Base(files[i]))
		ej := extractEpisode(filepath.Base(files[j]))
		if ei != ej {
			return ei < ej
		}
		return strings.ToLower(filepath.Base(files[i])) < strings.ToLower(filepath.Base(files[j]))
	})
	return files
}

var (
	reSeasonEp = regexp.MustCompile(`S\d+E(\d+)`)
	reEP       = regexp.MustCompile(`EP(\d+)`)
	reE        = regexp.MustCompile(`(?:^|[^A-Z0-9])E(\d+)`)
	reCNEp     = regexp.MustCompile(`第(\d+)[集话章期]`)
	reDigits   = regexp.MustCompile(`(\d+)`)
)

var (
	reCropExpr     = regexp.MustCompile(`crop=(\d+):(\d+):(\d+):(\d+)`)
	reEncoderTag   = regexp.MustCompile(`(?i)[.\-_ ](H\.?264|H\.?265|AVC|HEVC|x264|x265)([.\-_ ]|$)`)
	reEpSeasonTag  = regexp.MustCompile(`(?i)(S\d{1,2}E\d{1,3})`)
	reEpWordTag    = regexp.MustCompile(`(?i)(?:^|[\s._\-\[（【])(?:EP?|第)(\d{1,3})(?:集|话|话)?(?:$|[\s._\-\]）】])`)
	reEpBracketTag = regexp.MustCompile(`[\[\(（【](\d{1,3})[\]\)）】]`)
	reEpLooseTag   = regexp.MustCompile(`(?:^|[\s._\-])(\d{1,2})(?:[\s._\-]|$)`)
	reBareDigits   = regexp.MustCompile(`\d+`)
	reBracketGroup = regexp.MustCompile(`\[.*?\]|\(.*?\)`)
	reReleaseTag   = regexp.MustCompile(`(?i)\b(UNRATED|REMASTERED|PROPER|REPACKED|LIMITED|INTERNAL|BONE|BluRay|HEVC|x265|x264|AAC|FLAC|5\.1)\b`)
	reSpaceRun     = regexp.MustCompile(`[\s_]+`)
	reDotRun       = regexp.MustCompile(`\.{2,}`)
	reNonASCIIKey  = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

	reFFmpegTime  = regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`)
	reFFmpegSpeed = regexp.MustCompile(`speed=\s*([\d.]+)\s*x`)
	reSyncOffset  = regexp.MustCompile(`(?i)offset[:\s]+([-\d.]+)\s*s`)
	reSyncScale   = regexp.MustCompile(`(?i)(?:framerate[_ ]?scale|scale)[:\s]+([\d.]+)`)

	reHTMLTag     = regexp.MustCompile(`<[^>]+>`)
	reASSOverride = regexp.MustCompile(`\{\\[^}]*\}`)
	reASSBraces   = regexp.MustCompile(`\{[^}]*\}`)
	reHIBracket   = regexp.MustCompile(`\[[^\]]*\]`)
	reHIParen     = regexp.MustCompile(`\([^)]*\)`)
	reHIFullParen = regexp.MustCompile(`（[^）]*）`)
	reHIMusic     = regexp.MustCompile(`[♪♫♬♩🎵🎶]+`)
	reHIColon     = regexp.MustCompile(`(?i)^[A-Z][A-Z\s]{0,20}:\s*`)

	reSRTTimestamp  = regexp.MustCompile(`\d{2}:\d{2}:\d{2},\d{3}`)
	reASSDialogueTS = regexp.MustCompile(`(Dialogue:\s*\d+,)(\d+:\d{2}:\d{2}\.\d{2})(,)(\d+:\d{2}:\d{2}\.\d{2})`)
	reASSDialogue   = regexp.MustCompile(`(?m)^Dialogue:[^,]*,[^,]*,[^,]*,[^,]*,[^,]*,[^,]*,[^,]*,[^,]*,[^,]*,(.+)$`)
)

func extractEpisode(name string) int {
	upper := strings.ToUpper(name)
	if m := reSeasonEp.FindStringSubmatch(upper); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := reEP.FindStringSubmatch(upper); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := reE.FindStringSubmatch(upper); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := reCNEp.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := reDigits.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func getVideoDuration(videoPath string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, err
	}
	return dur, nil
}

func (a *App) TrimVideo(inputFile, outputDir, startTime, endTime string, copyMode bool) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if inputFile == "" {
		a.log("error", "未选择视频文件")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}
	startSec, sErr := parseHMSToSeconds(startTime)
	endSec, eErr := parseHMSToSeconds(endTime)
	if sErr != nil || eErr != nil {
		a.log("error", "时间格式无效，需 HH:MM:SS")
		return false
	}
	if endSec <= startSec {
		a.log("error", fmt.Sprintf("结束时间早于开始时间 %s → %s", startTime, endTime))
		return false
	}
	if dur, derr := getVideoDuration(inputFile); derr == nil && dur > 0 {
		if startSec >= dur {
			a.log("error", fmt.Sprintf("开始时间 %s 超出视频总长 %s", startTime, friendlyDuration(dur)))
			return false
		}
		if endSec > dur+0.5 {
			a.log("warn", fmt.Sprintf("结束时间 %s 超出视频总长 %s，将裁到片尾", endTime, friendlyDuration(dur)))
		}
	}

	ext := filepath.Ext(inputFile)
	stem := cleanFilename(strings.TrimSuffix(filepath.Base(inputFile), ext))
	safe := strings.NewReplacer(":", "-", ".", "-")
	tag := safe.Replace(startTime) + "_" + safe.Replace(endTime)
	outFile := filepath.Join(outputDir, fmt.Sprintf("%s.trim(%s)%s", stem, tag, ext))
	outFile = a.safeOutputPath(outFile, []string{inputFile})

	if copyMode {
		a.log("info", "无损裁剪 关键帧对齐")
	}
	a.log("info", fmt.Sprintf("裁剪 %s · %s → %s", filepath.Base(inputFile), startTime, endTime))

	var args []string
	if copyMode {
		args = []string{"ffmpeg", "-y", "-i", inputFile, "-ss", startTime, "-to", endTime, "-c", "copy", outFile}
	} else {
		args = []string{"ffmpeg", "-y", "-i", inputFile, "-ss", startTime, "-to", endTime, outFile}
	}

	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	out, err := a.runCancellableCmd(cmd)
	if err != nil {
		os.Remove(outFile)
		if a.stopped.Load() {
			a.log("warn", "裁剪已取消")
			return false
		}
		errStr := decodeTextFile(out)
		if len(errStr) > 300 {
			errStr = errStr[len(errStr)-300:]
		}
		a.log("error", "裁剪失败: "+errStr)
		return false
	}
	a.log("ok", "裁剪 → "+filepath.Base(outFile))
	return true
}

func parseHMSToSeconds(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("空时间串")
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("时间段过多")
	}
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("非法时间段: %s", p)
		}
		total = total*60 + v
	}
	return total, nil
}

func (a *App) GenerateThumbnailGrid(videoFile, outputDir string, cols, rows int) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if videoFile == "" {
		a.log("error", "未选择视频文件")
		return false
	}
	info, err := getVideoInfo(videoFile)
	if err != nil {
		a.log("error", "无法读取视频信息: "+err.Error())
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	if info.Duration <= 0 {
		a.log("error", "无法获取视频时长，无法生成缩略图")
		return false
	}
	if cols < 1 || rows < 1 || cols > 10 || rows > 10 {
		a.log("error", fmt.Sprintf("缩略图行列数无效 %dx%d", cols, rows))
		return false
	}
	total := cols * rows
	fps := fmt.Sprintf("%.6f", float64(total)/math.Max(info.Duration, 0.1))
	vf := fmt.Sprintf("fps=%s,scale=240:135:force_original_aspect_ratio=decrease,pad=240:135:(ow-iw)/2:(oh-ih)/2,tile=%dx%d", fps, cols, rows)

	stem := cleanFilename(strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile)))
	outFile := filepath.Join(outputDir, fmt.Sprintf("%s_preview_%dx%d.jpg", stem, cols, rows))
	outFile = a.safeOutputPath(outFile, []string{videoFile})

	a.log("info", fmt.Sprintf("缩略图 %dx%d → %s", cols, rows, filepath.Base(outFile)))
	cmd := exec.Command("ffmpeg", "-y", "-i", videoFile, "-vf", vf, "-frames:v", "1", "-q:v", "2", outFile)
	hideWindow(cmd)
	out, err := a.runCancellableCmd(cmd)
	if err != nil {
		if a.stopped.Load() {
			a.log("warn", "缩略图生成已取消")
			return false
		}
		errStr := decodeGBK(out)
		if len(errStr) > 300 {
			errStr = errStr[len(errStr)-300:]
		}
		a.log("error", "生成失败: "+errStr)
		return false
	}
	a.log("ok", "缩略图已生成 → "+filepath.Base(outFile))
	return true
}

type SubtitleIssue struct {
	Index  int    `json:"index"`
	Start  string `json:"start"`
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

func (a *App) CheckSubtitleQuality(filePath, outputDir string) bool {
	if filePath == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	if info, err := os.Stat(filePath); err == nil && info.IsDir() {
		files := findTextSubtitles(filePath)
		if len(files) == 0 {
			a.log("warn", "目录中未找到字幕文件")
			return false
		}
		a.log("info", fmt.Sprintf("批量质量检查: %d 个文件", len(files)))
		ok := true
		for _, f := range files {
			if !a.checkSubtitleQualitySingle(f, outputDir) {
				ok = false
			}
		}
		return ok
	}
	return a.checkSubtitleQualitySingle(filePath, outputDir)
}

func (a *App) checkSubtitleQualitySingle(filePath, outputDir string) bool {
	if outputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}
	entries := a.LoadSubtitle(filePath)
	if entries == nil {
		a.log("error", "字幕解析失败或为空")
		return false
	}

	for i := range entries {
		entries[i].Text = asrCleanSubtitleText(entries[i].Text)
	}

	typeLabel := map[string]string{"overlap": "重叠", "short": "过短", "empty": "空白", "long_line": "行过长"}
	var issues []SubtitleIssue
	for i, e := range entries {
		if strings.TrimSpace(e.Text) == "" {
			issues = append(issues, SubtitleIssue{e.Index, e.Start, "empty", "字幕文本为空"})
		}
		startMs := subTimeToMs(e.Start, e.Format)
		endMs := subTimeToMs(e.End, e.Format)
		dur := endMs - startMs
		if dur > 0 && dur < 300 {
			issues = append(issues, SubtitleIssue{e.Index, e.Start, "short", fmt.Sprintf("显示时长仅 %dms（< 300ms）", dur)})
		}
		if i+1 < len(entries) {
			nextStart := subTimeToMs(entries[i+1].Start, entries[i+1].Format)
			if endMs > nextStart {
				issues = append(issues, SubtitleIssue{e.Index, e.Start, "overlap",
					fmt.Sprintf("与第 %d 条重叠 %dms", entries[i+1].Index, endMs-nextStart)})
			}
		}
		for _, line := range strings.Split(e.Text, "\n") {
			if len([]rune(line)) > 80 {
				issues = append(issues, SubtitleIssue{e.Index, e.Start, "long_line",
					fmt.Sprintf("行宽 %d 字符（> 80）", len([]rune(line)))})
				break
			}
		}
	}

	if len(issues) == 0 {
		a.log("ok", fmt.Sprintf("字幕质量检查通过，共 %d 条，未发现问题", len(entries)))
		return true
	}

	stem := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	reportPath := filepath.Join(outputDir, stem+".质量检查报告.txt")
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("字幕质量检查报告\n"))
	sb.WriteString(fmt.Sprintf("文件: %s\n", filepath.Base(filePath)))
	sb.WriteString(fmt.Sprintf("总条目: %d\n", len(entries)))
	sb.WriteString(fmt.Sprintf("发现问题: %d\n", len(issues)))
	sb.WriteString("─────────────────────────────────\n\n")
	for _, issue := range issues {
		label := typeLabel[issue.Type]
		if label == "" {
			label = issue.Type
		}
		sb.WriteString(fmt.Sprintf("[%s] 第 %d 条 [%s] %s\n", label, issue.Index, issue.Start, issue.Detail))
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}
	if err := os.WriteFile(reportPath, []byte(sb.String()), 0644); err != nil {
		a.log("error", "写入检查报告失败")
		return false
	}
	a.log("warn", fmt.Sprintf("问题 %d 个 · %s", len(issues), filepath.Base(reportPath)))
	return true
}

func (a *App) ConvertToCleanSRT(inputFile, outputDir string) bool {
	if inputFile == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	if info, err := os.Stat(inputFile); err == nil && info.IsDir() {
		files := findTextSubtitles(inputFile)
		if len(files) == 0 {
			a.log("warn", "目录中未找到字幕文件")
			return false
		}
		a.log("info", fmt.Sprintf("批量转换简洁 SRT: %d 个文件", len(files)))
		ok := true
		for _, f := range files {
			if !a.convertToCleanSRTSingle(f, outputDir) {
				ok = false
			}
		}
		return ok
	}
	return a.convertToCleanSRTSingle(inputFile, outputDir)
}

func (a *App) convertToCleanSRTSingle(inputFile, outputDir string) bool {
	if outputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}
	stem := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
	outFile := filepath.Join(outputDir, stem+".简洁.srt")
	idx, ok := a.writeCleanSRT(inputFile, outFile)
	if !ok {
		a.log("error", "字幕解析失败或清理后为空")
		return false
	}
	a.log("ok", fmt.Sprintf("简洁 SRT %s · %d 条", filepath.Base(outFile), idx))
	return true
}

func (a *App) buildCleanSRT(inputFile string) (string, int, bool) {
	entries := a.LoadSubtitle(inputFile)
	if len(entries) == 0 {
		return "", 0, false
	}
	entries, removedCredits := cleanSubtitleCreditEntries(entries)
	if removedCredits > 0 {
		a.log("info", fmt.Sprintf("移除无关条目 %d 条", removedCredits))
	}
	if len(entries) == 0 {
		return "", 0, false
	}
	cleanTexts := make([]string, len(entries))
	suspicious := make([]int, 0)
	for i, e := range entries {
		text := cleanToCJKOnly(e.Text)
		text = stripContactInfo(text)
		text = stripSubtitleAdText(text)
		text = asrCleanSubtitleText(text)
		text = mergeShortLines(text)
		text = dedupeEntryLines(text)
		cleanTexts[i] = text
		if text != "" && isSuspiciousSubtitleAd(text) {
			suspicious = append(suspicious, i)
		}
	}
	if len(suspicious) > 0 {
		a.log("info", fmt.Sprintf("广告复核 %s · %d 条", aiProviderLabel(), len(suspicious)))
		if refined, removed, err := cleanSuspiciousSubtitleAds(chatAPIKey(), cleanTexts, suspicious); err == nil {
			cleanTexts = refined
			if removed > 0 {
				a.log("info", fmt.Sprintf("移除广告 %d 条", removed))
			}
		} else {
			a.log("warn", "广告复核失败: "+err.Error())
		}
	}
	var sb strings.Builder
	idx := 0
	for i, e := range entries {
		text := cleanTexts[i]
		if text == "" {
			continue
		}
		idx++
		srtStart := toSRTTimestamp(e.Start, e.Format)
		srtEnd := toSRTTimestamp(e.End, e.Format)
		sb.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", idx, srtStart, srtEnd, text))
	}
	if idx == 0 {
		return "", 0, false
	}
	return sb.String(), idx, true
}

func (a *App) writeCleanSRT(inputFile, outFile string) (int, bool) {
	content, n, ok := a.buildCleanSRT(inputFile)
	if !ok {
		return 0, false
	}
	bom := "\xEF\xBB\xBF"
	if err := os.WriteFile(outFile, []byte(bom+content), 0644); err != nil {
		return 0, false
	}
	return n, true
}

func (a *App) FixSubtitleWithAI(inputFile, outputDir string) bool {
	if inputFile == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if info, err := os.Stat(inputFile); err == nil && info.IsDir() {
		files := findTextSubtitles(inputFile)
		if len(files) == 0 {
			a.log("warn", "目录中未找到字幕文件")
			return false
		}
		a.log("info", fmt.Sprintf("批量 %s 修复字幕: %d 个文件", aiProviderLabel(), len(files)))
		ok := true
		for _, f := range files {
			if !a.fixSubtitleWithAISingle(f, outputDir) {
				ok = false
			}
		}
		return ok
	}
	return a.fixSubtitleWithAISingle(inputFile, outputDir)
}

func (a *App) fixSubtitleWithAISingle(inputFile, outputDir string) bool {
	if outputDir == "" {
		a.log("error", "未设置输出目录")
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
	if chatAPIKey() == "" {
		a.log("error", "未配置 AI Key")
		return false
	}

	entries, removedCredits := cleanSubtitleCreditEntries(entries)
	if removedCredits > 0 {
		a.log("info", fmt.Sprintf("移除组信息 %d 条", removedCredits))
	}
	if len(entries) == 0 {
		a.log("error", "清理后字幕为空")
		return false
	}

	units := planSubtitleFix(entries)
	shape := summarizeFixUnits(units)
	keepStructure := alreadyStackedBilingual(units)
	a.log("info", fmt.Sprintf("字幕语言 %s · %d 条", shape, len(entries)))
	if keepStructure {
		a.log("info", "中外叠显，仅校对")
	}

	var redundant []int
	for i := range units {
		if units[i].drop {
			redundant = append(redundant, i)
		}
	}
	if len(redundant) > 0 {
		entries, units = dropRedundantUnits(entries, units)
		a.log("info", fmt.Sprintf("丢弃冗余外语 %d 条", len(redundant)))
	}
	foreignCount := 0
	for i := range units {
		if units[i].foreignEN || units[i].foreignZH {
			foreignCount++
		}
	}
	if foreignCount > 0 {
		a.log("info", fmt.Sprintf("第三语言改写双语 %d 条", foreignCount))
	}
	applyForeignDrops(units)

	ext := filepath.Ext(inputFile)
	stem := strings.TrimSuffix(filepath.Base(inputFile), ext)
	fromASRPost := strings.Contains(stem, ".AI修正") || strings.Contains(stem, ".AI修复")
	if fromASRPost {
		a.log("info", "英文已是成品，只处理中文")
	}
	isASS := entries[0].Format == "ass" || entries[0].Format == "ssa"
	outExt := ".srt"
	if isASS {
		outExt = filepath.Ext(inputFile)
		if outExt == "" {
			outExt = ".ass"
		}
	}
	outFile := filepath.Join(outputDir, stem+".AI修复"+outExt)

	const batchSize = 50
	const contextLines = 5
	apiKey := chatAPIKey()
	var prevCtxSrc, prevCtxDst []string
	var enAddCtxSrc, enAddCtxDst []string
	var zhAddCtxSrc, zhAddCtxDst []string
	var foCtxSrc, foCtxDst []string
	var foZhCtxSrc, foZhCtxDst []string

	setCtx := func(src, dst *[]string, in, out []string) {
		if len(in) == 0 || len(out) != len(in) {
			return
		}
		start := 0
		if len(in) > contextLines {
			start = len(in) - contextLines
		}
		*src = append([]string{}, in[start:]...)
		*dst = append([]string{}, out[start:]...)
	}

	callWithSplit := func(label string, batch []string, call func([]string) ([]string, error)) ([]string, error) {
		if len(batch) == 0 {
			return nil, nil
		}
		const maxRetry = 3
		const maxSplitDepth = 4
		var run func([]string, int) ([]string, error)
		run = func(part []string, depth int) ([]string, error) {
			var results []string
			var err error
			for attempt := 1; attempt <= maxRetry; attempt++ {
				if a.stopped.Load() {
					return nil, fmt.Errorf("已取消")
				}
				results, err = call(part)
				if err == nil {
					return results, nil
				}
				msg := err.Error()
				if strings.Contains(msg, "余额不足") || strings.Contains(msg, "认证失败") {
					return nil, err
				}
				if attempt < maxRetry {
					time.Sleep(time.Duration(attempt) * 2 * time.Second)
				}
			}
			if depth >= maxSplitDepth || len(part) <= 5 {
				if depth == 0 {
					a.log("warn", fmt.Sprintf("%s %s 失败 %d 条，逐条重试", aiProviderLabel(), label, len(part)))
				}
				fallback := make([]string, len(part))
				kept := 0
				for i, t := range part {
					sr, se := call([]string{t})
					switch {
					case se != nil:
						fallback[i] = t
						kept++
					case len(sr) == 0:
						fallback[i] = t
						kept++
					default:
						fallback[i] = sr[0]
					}
				}
				if kept > 0 {
					a.log("warn", fmt.Sprintf("保留原文 %d 条", kept))
				}
				return fallback, nil
			}
			if depth == 0 {
				a.log("warn", fmt.Sprintf("%s %s 失败 %d 条，拆分重试", aiProviderLabel(), label, len(part)))
			}
			mid := len(part) / 2
			left, errL := run(part[:mid], depth+1)
			if errL != nil {
				return nil, errL
			}
			right, errR := run(part[mid:], depth+1)
			if errR != nil {
				return nil, errR
			}
			return append(left, right...), nil
		}
		return run(batch, 0)
	}

	jobTexts := func(jobs []fixJob) []string {
		out := make([]string, len(jobs))
		for i, j := range jobs {
			out[i] = j.text
		}
		return out
	}

	send := func(label string, in []string, call func([]string) ([]string, error)) ([]string, error) {
		uniq, index := dedupeTexts(in)
		if len(uniq) < len(in) {
			a.log("info", fmt.Sprintf("%s 去重 %d→%d", label, len(in), len(uniq)))
		}
		res, err := callWithSplit(label, uniq, call)
		if err != nil {
			return nil, err
		}
		return expandTexts(res, index), nil
	}

	failOut := func() {
		os.Remove(outFile)
	}

	for start := 0; start < len(units); start += batchSize {
		if a.stopped.Load() {
			a.log("warn", aiProviderLabel()+" 修复已取消")
			failOut()
			return false
		}
		end := start + batchSize
		if end > len(units) {
			end = len(units)
		}

		zhJobs, enJobs, proofJobs := gatherPolishJobs(units, start, end)
		if len(zhJobs) > 0 {
			zhIn := jobTexts(zhJobs)
			zhOut, err := callWithSplit("中文", zhIn, func(part []string) ([]string, error) {
				return polishChineseTexts(apiKey, part, prevCtxSrc, prevCtxDst)
			})
			if err != nil {
				a.log("error", aiProviderLabel()+" 修复终止: "+err.Error())
				failOut()
				return false
			}
			applyZhJobResults(units, zhJobs, zhOut)
			setCtx(&prevCtxSrc, &prevCtxDst, zhIn, zhOut)
		}
		if len(enJobs) > 0 && !fromASRPost {
			enOut, err := callWithSplit("英文", jobTexts(enJobs), func(part []string) ([]string, error) {
				return asrCorrectEnglish(apiKey, part)
			})
			if err != nil {
				a.log("error", aiProviderLabel()+" 修复终止: "+err.Error())
				failOut()
				return false
			}
			applyJobResults(units, enJobs, enOut)
		}
		if len(proofJobs) > 0 && !fromASRPost {
			proIn := jobTexts(proofJobs)
			proOut, err := callWithSplit("英文校对", proIn, func(part []string) ([]string, error) {
				return asrProofreadEnglish(apiKey, part)
			})
			if err != nil {
				a.log("error", aiProviderLabel()+" 修复终止: "+err.Error())
				failOut()
				return false
			}
			applyJobResults(units, proofJobs, proOut)
		}

		foZH, foEN := gatherForeignJobs(units, start, end)
		if len(foZH) > 0 {
			foIn := jobTexts(foZH)
			out, err := send("第三语言译中", foIn, func(part []string) ([]string, error) {
				return asrTranslateToChinese(apiKey, part, foZhCtxSrc, foZhCtxDst)
			})
			if err != nil {
				if a.stopped.Load() || strings.Contains(err.Error(), "已取消") {
					a.log("warn", aiProviderLabel()+" 修复已取消")
					failOut()
					return false
				}
				a.log("warn", fmt.Sprintf("第三语言转中文失败，已删除 %d 条: %s", len(foZH), err.Error()))
				for _, j := range foZH {
					units[j.ui].foreignDrop = true
					units[j.ui].addEN = false
				}
				applyForeignDrops(units)
			} else {
				applyForeignChinese(units, foZH, out)
				setCtx(&foZhCtxSrc, &foZhCtxDst, foIn, out)
			}
		}

		enAdd, zhAdd := gatherAddJobs(units, start, end)
		if len(zhAdd) > 0 {
			zhIn := jobTexts(zhAdd)
			added, err := send("外译中", zhIn, func(part []string) ([]string, error) {
				return asrTranslateToChinese(apiKey, part, zhAddCtxSrc, zhAddCtxDst)
			})
			if err != nil {
				if a.stopped.Load() || strings.Contains(err.Error(), "已取消") {
					a.log("warn", aiProviderLabel()+" 修复已取消")
					failOut()
					return false
				}
				a.log("warn", "补译中文失败，仅保留原文: "+err.Error())
			} else {
				applyAddJobs(units, zhAdd, added, lineLangChinese)
				setCtx(&zhAddCtxSrc, &zhAddCtxDst, zhIn, added)
			}
		}
		if len(enAdd) > 0 {
			enIn := jobTexts(enAdd)
			added, err := send("中译英", enIn, func(part []string) ([]string, error) {
				return asrTranslateToEnglish(apiKey, part, enAddCtxSrc, enAddCtxDst)
			})
			if err != nil {
				if a.stopped.Load() || strings.Contains(err.Error(), "已取消") {
					a.log("warn", aiProviderLabel()+" 修复已取消")
					failOut()
					return false
				}
				a.log("warn", "补译英文失败，仅保留中文: "+err.Error())
			} else {
				applyAddJobs(units, enAdd, added, lineLangLatin)
				setCtx(&enAddCtxSrc, &enAddCtxDst, enIn, added)
			}
		}
		if len(foEN) > 0 {
			foIn := jobTexts(foEN)
			out, err := send("第三语言译英", foIn, func(part []string) ([]string, error) {
				return asrTranslateToEnglish(apiKey, part, foCtxSrc, foCtxDst)
			})
			if err != nil {
				if a.stopped.Load() || strings.Contains(err.Error(), "已取消") {
					a.log("warn", aiProviderLabel()+" 修复已取消")
					failOut()
					return false
				}
				a.log("warn", fmt.Sprintf("第三语言转英文失败，已删除 %d 条: %s", len(foEN), err.Error()))
				for _, j := range foEN {
					units[j.ui].foreignDrop = true
				}
				applyForeignDrops(units)
			} else {
				applyForeignJobs(units, foEN, out)
				setCtx(&foCtxSrc, &foCtxDst, foIn, out)
			}
		}

		a.log("info", fmt.Sprintf("进度: %d / %d", end, len(units)))
	}

	final := make([]SubtitleEntry, 0, len(entries))
	for i := range units {
		if len(units[i].frags) == 0 {
			continue
		}
		text := renderFixUnit(units[i])
		if strings.TrimSpace(stripAllTags(text)) == "" {
			continue
		}
		entries[i].Text = text
		final = append(final, entries[i])
	}
	if len(final) == 0 {
		a.log("error", aiProviderLabel()+" 修复后字幕为空")
		failOut()
		return false
	}
	if cleaned, removed := cleanSubtitleCreditEntries(final); removed > 0 {
		final = cleaned
		a.log("info", fmt.Sprintf("移除无关条目 %d 条", removed))
	}
	if len(final) == 0 {
		a.log("error", aiProviderLabel()+" 修复后字幕为空")
		failOut()
		return false
	}
	if isASS {
		if !a.SaveSubtitle(inputFile, outFile, final) {
			return false
		}
	} else {
		var sb strings.Builder
		for i, e := range final {
			sb.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", i+1,
				toSRTTimestamp(e.Start, e.Format), toSRTTimestamp(e.End, e.Format), e.Text))
		}
		if err := os.WriteFile(outFile, []byte(sb.String()), 0644); err != nil {
			a.log("error", "写入字幕文件失败: "+err.Error())
			failOut()
			return false
		}
	}
	a.log("ok", fmt.Sprintf("%s 修复 %s · %d 条", chatProviderUsed(), filepath.Base(outFile), len(final)))
	return true
}

func (a *App) BatchConvertSubtitles(inputDir, targetFormat string) int {
	if inputDir == "" {
		a.log("error", "未设置目录")
		return 0
	}
	var sourceExts map[string]bool
	var outExt string
	switch strings.ToLower(targetFormat) {
	case "srt":
		sourceExts = map[string]bool{".ass": true, ".ssa": true, ".vtt": true, ".sub": true, ".ttml": true}
		outExt = ".srt"
	case "ass":
		sourceExts = map[string]bool{".srt": true, ".vtt": true, ".sub": true, ".ttml": true}
		outExt = ".ass"
	case "vtt":
		sourceExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".ttml": true}
		outExt = ".vtt"
	default:
		a.log("error", "目标格式不支持: "+targetFormat)
		return 0
	}

	files := findFilesRecursive(inputDir, sourceExts)
	if len(files) == 0 {
		a.log("warn", "未找到可转换的字幕文件")
		return 0
	}
	count := 0
	for _, f := range files {
		stem := strings.TrimSuffix(f, filepath.Ext(f))
		outFile := stem + outExt
		a.log("info", "转换: "+filepath.Base(f))
		cmd := exec.Command("ffmpeg", "-y", "-i", f, outFile)
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			a.log("error", "失败: "+friendlyErrorMsg(decodeGBK(out), "", false))
		} else {
			if outExt == ".srt" {
				cleanSRTFile(outFile)
			}
			count++
		}
	}
	a.log("ok", fmt.Sprintf("批量转换完成: %d / %d 个文件", count, len(files)))
	return count
}

func (a *App) TakeScreenshot(videoFile, outputDir, timestampStr string) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if videoFile == "" {
		a.log("error", "未选择视频文件")
		return false
	}
	if timestampStr == "" {
		timestampStr = "00:00:30"
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}
	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	ts := strings.NewReplacer(":", "-", ".", "-").Replace(timestampStr)
	outFile := filepath.Join(outputDir, fmt.Sprintf("%s_%s.jpg", stem, ts))

	a.log("info", fmt.Sprintf("截图: %s @ %s", filepath.Base(videoFile), timestampStr))
	cmd := exec.Command("ffmpeg", "-y", "-ss", timestampStr, "-i", videoFile,
		"-frames:v", "1", "-q:v", "2", outFile)
	hideWindow(cmd)
	out, err := a.runCancellableCmd(cmd)
	if err != nil {
		if a.stopped.Load() {
			a.log("warn", "截图已取消")
			return false
		}
		errStr := decodeGBK(out)
		if len(errStr) > 300 {
			errStr = errStr[len(errStr)-300:]
		}
		a.log("error", "截图失败: "+errStr)
		return false
	}
	a.log("ok", "截图 → "+filepath.Base(outFile))
	return true
}

type ChapterEntry struct {
	Title string `json:"title"`
	Start string `json:"start"`
}

func (a *App) ReadTextFile(filePath string) []string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		a.log("error", "读取文件失败: "+err.Error())
		return nil
	}
	content := strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n")
	return strings.Split(strings.TrimRight(content, "\n"), "\n")
}

func escapeFFMetadata(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`=`, `\=`,
		`;`, `\;`,
		`#`, `\#`,
		"\n", `\`+"\n",
	)
	return r.Replace(strings.ReplaceAll(s, "\r\n", "\n"))
}

func (a *App) writeShiftedChapterMeta(videoFile string, shiftSec float64) string {
	if shiftSec <= 0 {
		return ""
	}
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_chapters", videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var raw struct {
		Chapters []struct {
			StartTime string            `json:"start_time"`
			EndTime   string            `json:"end_time"`
			Tags      map[string]string `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || len(raw.Chapters) == 0 {
		return ""
	}

	shiftMs := int64(shiftSec*1000 + 0.5)
	var sb strings.Builder
	sb.WriteString(";FFMETADATA1\n")
	for i, c := range raw.Chapters {
		var startSec, endSec float64
		fmt.Sscanf(c.StartTime, "%f", &startSec)
		fmt.Sscanf(c.EndTime, "%f", &endSec)
		startMs := int64(startSec*1000+0.5) + shiftMs
		endMs := int64(endSec*1000+0.5) + shiftMs
		if endMs <= startMs {
			endMs = startMs + 1000
		}
		sb.WriteString("\n[CHAPTER]\nTIMEBASE=1/1000\n")
		sb.WriteString(fmt.Sprintf("START=%d\nEND=%d\n", startMs, endMs))
		title := c.Tags["title"]
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		sb.WriteString("title=" + escapeFFMetadata(title) + "\n")
	}

	p := filepath.Join(os.TempDir(), fmt.Sprintf("muxer-chapters-%d.txt", time.Now().UnixNano()))
	if err := os.WriteFile(p, []byte(sb.String()), 0644); err != nil {
		a.log("warn", "章节偏移元数据写入失败，本次不保留章节: "+err.Error())
		return ""
	}
	a.log("info", fmt.Sprintf("章节后移 %d 个 · %.1fs", len(raw.Chapters), shiftSec))
	return p
}

func (a *App) ReadChapters(videoFile string) []ChapterEntry {
	if videoFile == "" {
		return nil
	}
	cmd := exec.Command("ffprobe", "-v", "quiet",
		"-print_format", "json", "-show_chapters", videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		a.log("warn", "无章节信息")
		return []ChapterEntry{}
	}
	var raw struct {
		Chapters []struct {
			StartTime string            `json:"start_time"`
			Tags      map[string]string `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out, &raw); err != nil || len(raw.Chapters) == 0 {
		return []ChapterEntry{}
	}
	result := make([]ChapterEntry, 0, len(raw.Chapters))
	for _, c := range raw.Chapters {
		var sec float64
		fmt.Sscanf(c.StartTime, "%f", &sec)
		title := c.Tags["title"]
		result = append(result, ChapterEntry{
			Title: title,
			Start: secsToHMS(sec),
		})
	}
	a.log("info", fmt.Sprintf("章节 %d 个", len(result)))
	return result
}

func (a *App) WriteChapters(videoFile, outputDir string, chapters []ChapterEntry) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if videoFile == "" {
		a.log("error", "未选择视频文件")
		return false
	}
	if len(chapters) == 0 {
		a.log("error", "章节列表为空")
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

	sort.Slice(chapters, func(i, j int) bool {
		return hmsToMs(chapters[i].Start) < hmsToMs(chapters[j].Start)
	})

	var totalMs int64
	if info, err := getVideoInfo(videoFile); err == nil && info.Duration > 0 {
		totalMs = int64(info.Duration * 1000)
	}

	tmpMeta := uniqueTempPath("muxer_chapters_meta", ".txt")
	defer os.Remove(tmpMeta)
	extractCmd := exec.Command("ffmpeg", "-y", "-i", videoFile, "-f", "ffmetadata", tmpMeta)
	hideWindow(extractCmd)
	_ = extractCmd.Run()

	baseMetadata := ";FFMETADATA1\n"
	if data, err := os.ReadFile(tmpMeta); err == nil {
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		if idx := strings.Index(content, "\n[CHAPTER]"); idx >= 0 {
			baseMetadata = strings.TrimRight(content[:idx], "\n") + "\n"
		} else if !strings.Contains(content, "[CHAPTER]") {
			baseMetadata = strings.TrimRight(content, "\n") + "\n"
		}
	}

	var sb strings.Builder
	sb.WriteString(baseMetadata)
	for i, ch := range chapters {
		startMs := hmsToMs(ch.Start)
		var endMs int64
		if i+1 < len(chapters) {
			endMs = hmsToMs(chapters[i+1].Start)
		} else if totalMs > startMs {
			endMs = totalMs
		} else {
			endMs = startMs + 60000
		}
		sb.WriteString("\n[CHAPTER]\n")
		sb.WriteString("TIMEBASE=1/1000\n")
		sb.WriteString(fmt.Sprintf("START=%d\n", startMs))
		sb.WriteString(fmt.Sprintf("END=%d\n", endMs))
		title := ch.Title
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		sb.WriteString("title=" + escapeFFMetadata(title) + "\n")
	}

	if err := os.WriteFile(tmpMeta, []byte(sb.String()), 0644); err != nil {
		a.log("error", "写入临时元数据失败: "+err.Error())
		return false
	}

	ext := filepath.Ext(videoFile)
	stem := strings.TrimSuffix(filepath.Base(videoFile), ext)
	outFile := filepath.Join(outputDir, stem+".chapters"+ext)

	a.log("info", fmt.Sprintf("写入 %d 个章节 → %s", len(chapters), filepath.Base(outFile)))
	cmd := exec.Command("ffmpeg", "-y",
		"-i", videoFile,
		"-i", tmpMeta,
		"-map_metadata", "1",
		"-codec", "copy",
		outFile)
	hideWindow(cmd)
	out, err := a.runCancellableCmd(cmd)
	if err != nil {
		if a.stopped.Load() {
			a.log("warn", "章节写入已取消")
			return false
		}
		errStr := decodeGBK(out)
		if len(errStr) > 400 {
			errStr = errStr[len(errStr)-400:]
		}
		a.log("error", "写入失败: "+strings.TrimSpace(errStr))
		return false
	}
	a.log("ok", fmt.Sprintf("章节 %d 个 → %s", len(chapters), filepath.Base(outFile)))
	return true
}

var reSRTInlineTag = regexp.MustCompile(`<[^>]+>`)

var reASSTag = regexp.MustCompile(`\{[^}]*\}`)

func stripAllTags(s string) string {
	s = strings.ReplaceAll(s, `\N`, "\n")
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = reASSTag.ReplaceAllString(s, "")
	s = reSRTInlineTag.ReplaceAllString(s, "")
	return s
}

func isCJKRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x20000 && r <= 0x2FA1F)
}

func containsCJK(s string) bool {
	for _, r := range s {
		if isCJKRune(r) {
			return true
		}
	}
	return false
}

func cjkRuneCount(s string) int {
	n := 0
	for _, r := range s {
		if isCJKRune(r) {
			n++
		}
	}
	return n
}

func looksForeignSubtitle(s string) bool {
	hasLetter := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			break
		}
	}
	return hasLetter && !containsCJK(s)
}

func cleanToCJKOnly(text string) string {
	text = stripAllTags(text)
	lines := strings.Split(text, "\n")
	var kept []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if containsCJK(line) || looksForeignSubtitle(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

const (
	bilingualCNSize  = 66
	bilingualENSize  = 64
	bilingualENMin   = 52
	bilingualMarginV = 57
	bilingualTextW   = 1794
)

func assLineWidth(s string, size int) float64 {
	w := 0.0
	for _, r := range s {
		switch {
		case r == ' ':
			w += 0.19
		case r == 'i', r == 'l', r == 'j', r == 't', r == 'f', r == 'r',
			r == 'I', r == '.', r == ',', r == ':', r == ';', r == '!', r == '\'', r == '|':
			w += 0.235
		case r == 'm', r == 'w', r == 'M', r == 'W':
			w += 0.62
		case r >= 0x4E00 && r <= 0x9FFF,
			r >= 0x3400 && r <= 0x4DBF,
			r >= 0xF900 && r <= 0xFAFF,
			r >= 0x20000 && r <= 0x2FA1F,
			r >= 0x3000 && r <= 0x303F,
			r >= 0xFF00 && r <= 0xFFEF,
			r >= 0x3040 && r <= 0x30FF,
			r >= 0xAC00 && r <= 0xD7AF:
			w += 0.71
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			w += 0.46
		default:
			w += 0.41
		}
	}
	return w * float64(size)
}

func bilingualENBlock(lines []string) string {
	widest := 0.0
	for _, ln := range lines {
		if w := assLineWidth(ln, bilingualENSize); w > widest {
			widest = w
		}
	}
	size := bilingualENSize
	if widest > bilingualTextW {
		size = int(float64(bilingualENSize) * bilingualTextW / widest)
		if size < bilingualENMin {
			size = bilingualENMin
		}
	}
	tag := `{\rEN\blur0.5`
	if size != bilingualENSize {
		tag += fmt.Sprintf(`\fs%d`, size)
	}
	return tag + `}` + strings.Join(lines, `\N`)
}

func buildBilingualASS(srtContent string) (string, bool) {
	entries := parseSRTEntries(srtContent)
	if len(entries) == 0 {
		return "", false
	}
	type row struct{ start, end, style, text string }
	var rows []row
	biCount, total := 0, 0
	for _, e := range entries {
		var zh, en []string
		for _, ln := range strings.Split(strings.ReplaceAll(e.Text, "\r", ""), "\n") {
			ln = strings.TrimSpace(ln)
			if ln == "" {
				continue
			}
			if looksForeignSubtitle(ln) {
				en = append(en, ln)
			} else {
				zh = append(zh, ln)
			}
		}
		if len(zh) == 0 && len(en) == 0 {
			continue
		}
		total++
		style, text := "CN", `{\blur0.5}`
		switch {
		case len(zh) > 0 && len(en) > 0:
			biCount++
			text += strings.Join(zh, `\N`) + `\N` + bilingualENBlock(en)
		case len(zh) > 0:
			text += strings.Join(zh, `\N`)
		default:
			style = "EN"
			text = bilingualENBlock(en)
		}
		rows = append(rows, row{
			start: msToSubTime(subTimeToMs(e.Start, "srt"), "ass"),
			end:   msToSubTime(subTimeToMs(e.End, "srt"), "ass"),
			style: style,
			text:  text,
		})
	}
	if total == 0 || biCount == 0 || biCount*100/total < 25 {
		return "", false
	}
	var sb strings.Builder
	sb.WriteString("[Script Info]\n")
	sb.WriteString("; Muxer 中英双语字幕\n")
	sb.WriteString("ScriptType: v4.00+\n")
	sb.WriteString("WrapStyle: 0\n")
	sb.WriteString("ScaledBorderAndShadow: yes\n")
	sb.WriteString("PlayResX: 1920\n")
	sb.WriteString("PlayResY: 1080\n\n")
	sb.WriteString("[V4+ Styles]\n")
	sb.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&sb, "Style: CN,%s,%d,&H00FFFFFF,&H000000FF,&H00000000,&H66000000,0,0,0,0,100,100,0,0,1,3,3,2,60,60,%d,1\n", bilingualCNFont, bilingualCNSize, bilingualMarginV)
	fmt.Fprintf(&sb, "Style: EN,%s,%d,&H004DD8FF,&H000000FF,&H00000000,&H66000000,0,0,0,0,100,100,0,0,1,3,3,2,60,60,%d,1\n\n", bilingualENFont, bilingualENSize, bilingualMarginV)
	sb.WriteString("[Events]\n")
	sb.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "Dialogue: 0,%s,%s,%s,,0,0,0,,%s\n", r.start, r.end, r.style, r.text)
	}
	return sb.String(), true
}

func subtitleDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF,
			r >= 0x3400 && r <= 0x4DBF,
			r >= 0xF900 && r <= 0xFAFF,
			r >= 0x3000 && r <= 0x303F,
			r >= 0xFF00 && r <= 0xFFEF,
			r >= 0x3040 && r <= 0x30FF,
			r >= 0xAC00 && r <= 0xD7AF:
			w += 2
		default:
			w += 1
		}
	}
	return w
}

func mergeShortLines(text string) string {
	if !strings.Contains(text, "\n") {
		return text
	}
	rawLines := strings.Split(text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) <= 1 {
		return strings.Join(lines, "\n")
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "—") || strings.HasPrefix(l, "–") {
			return strings.Join(lines, "\n")
		}
	}
	hasCJKLine, hasForeignLine := false, false
	for _, l := range lines {
		if containsCJK(l) {
			hasCJKLine = true
		} else if looksForeignSubtitle(l) {
			hasForeignLine = true
		}
	}
	if hasCJKLine && hasForeignLine {
		return strings.Join(lines, "\n")
	}
	merged := strings.Join(lines, " ")
	if subtitleDisplayWidth(merged) <= 36 {
		return merged
	}
	return strings.Join(lines, "\n")
}

var (
	reContactURL = regexp.MustCompile(
		`(?i)` +
			`(?:https?://|ftp://)[^\s\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{F900}-\x{FAFF}]+` +
			`|www\.[^\s\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{F900}-\x{FAFF}]+` +
			`|\b[a-z0-9][\-a-z0-9]*(?:\.[a-z0-9][\-a-z0-9]*)*\.(?:com|net|org|cn|tv|cc|io|me|tw|hk|jp|kr|us|uk|de|fr|info|xyz|top|vip|club|site|online|shop|app|live|fun|pro|biz|asia|art|fans|name|mobi|edu|gov)\b[^\s\x{4E00}-\x{9FFF}\x{3400}-\x{4DBF}\x{F900}-\x{FAFF}]*`,
	)
	reContactQQ = regexp.MustCompile(
		`(?i)(?:加\s*QQ\s*群?|加\s*群|QQ\s*群?|Q\s*群|扣扣\s*群?|官方\s*群|粉丝\s*群|交流\s*群|讨论\s*群|读者\s*群|玩家\s*群|VIP\s*群|微信\s*群|群\s*号)\s*[:：号\-]?\s*\d{5,12}`,
	)
	reContactQQBare = regexp.MustCompile(`(?i)\bQQ[\-:：]?\s*\d{5,12}\b`)
	reContactWeChat = regexp.MustCompile(
		`(?i)(?:加\s*)?(?:微信号|微信ID|微信|v信|vx|wx号|wx|wechat|weixin)\s*[:：\-]\s*[A-Za-z0-9_\-\.]{4,30}`,
	)
	reContactPublic = regexp.MustCompile(
		`(?i)(?:微信公众号|官方公众号|公众号|官方公号|公号)\s*[:：\-]\s*[^\s，。,.；;！!？?]{2,30}`,
	)
	reContactEmail  = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
	reContactSocial = regexp.MustCompile(
		`(?i)(?:新浪微博|微博|bilibili|b站|抖音|douyin|小红书|快手|kuaishou|telegram|tg|电报频道|电报|discord|twitter|推特|youtube|油管|instagram|ins|facebook|脸书|频道)\s*(?:账号|号|ID|id|主页)?\s*[:：@]\s*\S{2,}`,
	)
	reSubtitleAdCertain = regexp.MustCompile(`(?i)(?:最新\s*)?\d+\s*T(?:B)?\s*(?:影视剧|电影|电视剧|动漫|综艺)?\s*(?:大全|合集|资源|片库)|(?:影视剧|电影|电视剧|动漫|综艺)\s*(?:大全|合集|资源|片库)|(?:夸克|阿里云|百度|迅雷|磁力)\s*(?:网盘|云盘|资源|下载)`)
	reSubtitleAdSuspect = regexp.MustCompile(`(?i)(?:资源|合集|大全|网盘|云盘|下载|片库|磁力|免费观看|高清|超清|蓝光|字幕组|压制组|发布页|最新地址|永久地址|关注|加入|赞助|推广|获取|分享)`)
)

func stripURLPreserveDDYS(line string) string {
	own := strings.ToLower(brandURL())
	return reContactURL.ReplaceAllStringFunc(line, func(m string) string {
		if own != "" && strings.Contains(strings.ToLower(m), own) {
			return m
		}
		return ""
	})
}

func stripContactInfo(text string) string {
	lines := strings.Split(text, "\n")
	var kept []string
	for _, line := range lines {
		line = reContactEmail.ReplaceAllString(line, "")
		line = stripURLPreserveDDYS(line)
		line = reContactQQ.ReplaceAllString(line, "")
		line = reContactQQBare.ReplaceAllString(line, "")
		line = reContactWeChat.ReplaceAllString(line, "")
		line = reContactPublic.ReplaceAllString(line, "")
		line = reContactSocial.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		onlyPunct := true
		for _, r := range line {
			if r != '.' && r != ',' && r != '，' && r != '。' &&
				r != ':' && r != '：' && r != '-' && r != '—' &&
				r != '|' && r != '/' && r != '\\' && r != '_' &&
				r != '(' && r != ')' && r != '（' && r != '）' &&
				r != '[' && r != ']' && r != '【' && r != '】' {
				onlyPunct = false
				break
			}
		}
		if !onlyPunct {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func stripSubtitleAdText(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	removedAdLine := false
	for _, line := range lines {
		orig := line
		line = strings.TrimSpace(reSubtitleAdCertain.ReplaceAllString(line, ""))
		line = strings.Trim(line, " \t，。,.；;！!？?：:-—|/\\_()（）[]【】")
		if line == "" && strings.TrimSpace(orig) != "" {
			removedAdLine = true
			continue
		}
		if removedAdLine && isSubtitleAdTailLine(line) {
			continue
		}
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func isSubtitleAdTailLine(line string) bool {
	line = strings.TrimSpace(line)
	switch line {
	case "来啦", "来了", "来咯", "安排", "安排上", "快来", "速来", "点击进入", "复制打开":
		return true
	}
	return false
}

func isSuspiciousSubtitleAd(text string) bool {
	if text == "" {
		return false
	}
	return reSubtitleAdSuspect.MatchString(text)
}

func cleanSRTFile(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	content := strings.ReplaceAll(decodeTextFile(data), "\r\n", "\n")
	entries := parseSRTEntries(content)
	if len(entries) == 0 {
		return
	}
	changed := false
	for i := range entries {
		if text, drop := cleanSubtitleCreditText(entries[i].Text); drop {
			entries[i].Text = ""
			changed = true
			continue
		} else if text != entries[i].Text {
			entries[i].Text = text
			changed = true
		}
		cleaned := stripContactInfo(entries[i].Text)
		cleaned = stripSubtitleAdText(cleaned)
		cleaned = asrCleanSubtitleText(cleaned)
		if cleaned != entries[i].Text {
			entries[i].Text = cleaned
			changed = true
		}
	}
	if !changed {
		return
	}
	var sb strings.Builder
	idx := 0
	for _, e := range entries {
		if strings.TrimSpace(e.Text) == "" {
			continue
		}
		idx++
		sb.WriteString(fmt.Sprintf("%d\n%s --> %s\n%s\n\n", idx, e.Start, e.End, e.Text))
	}
	bom := "\xEF\xBB\xBF"
	_ = os.WriteFile(filePath, []byte(bom+sb.String()), 0644)
}

func ffsubsyncBin() string {
	for _, b := range []string{"ffsubsync", "ffs"} {
		cmd := exec.Command(b, "--version")
		hideWindow(cmd)
		if cmd.Run() == nil {
			return b
		}
	}
	return ""
}

func (a *App) CheckFFSubSync() bool {
	if ffsubsyncBin() != "" {
		a.log("ok", "ffsubsync 已安装")
		return true
	}
	a.log("warn", "未安装 ffsubsync")
	return false
}

func (a *App) AutoSyncSubtitle(videoFile, subFile, outputDir string) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	if videoFile == "" {
		a.log("error", "未选择视频文件")
		return false
	}
	if subFile == "" {
		a.log("error", "未选择字幕文件")
		return false
	}
	if outputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}

	bin := ffsubsyncBin()
	if bin == "" {
		a.log("error", "未安装 ffsubsync")
		return false
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	ext := filepath.Ext(subFile)
	stem := strings.TrimSuffix(filepath.Base(subFile), ext)
	stem = strings.TrimSuffix(stem, ".synced")
	outFile := filepath.Join(outputDir, stem+".synced"+ext)

	a.log("info", fmt.Sprintf("同步 %s + %s", filepath.Base(videoFile), filepath.Base(subFile)))
	a.log("info", "分析音频")

	cmd := exec.Command(bin, videoFile, "-i", subFile, "-o", outFile)
	hideWindow(cmd)

	stderrPipe, stderrErr := cmd.StderrPipe()
	stdoutPipe, stdoutErr := cmd.StdoutPipe()
	if stderrErr != nil || stdoutErr != nil {
		out, err := a.runCancellableCmd(cmd)
		if err != nil {
			if a.stopped.Load() {
				a.log("warn", "同步已取消")
				return false
			}
			errStr := decodeTextFile(out)
			if len(errStr) > 500 {
				errStr = errStr[len(errStr)-500:]
			}
			a.log("error", "同步失败: "+strings.TrimSpace(errStr))
			return false
		}
	} else {
		a.cmdLock.Lock()
		a.cmd = cmd
		a.cmdLock.Unlock()
		if err := cmd.Start(); err != nil {
			a.cmdLock.Lock()
			a.cmd = nil
			a.cmdLock.Unlock()
			a.log("error", "启动 ffsubsync 失败: "+err.Error())
			return false
		}

		var syncOffset, syncScale, syncLastLine string

		collectSync := func(line string) {
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			syncLastLine = line
			lc := strings.ToLower(line)
			if m := reSyncOffset.FindStringSubmatch(line); m != nil {
				syncOffset = m[1]
			}
			if m := reSyncScale.FindStringSubmatch(line); m != nil {
				syncScale = m[1]
			}
			if strings.Contains(lc, "error") || strings.Contains(lc, "warning") {
				a.log("warn", line)
			}
		}

		var wg sync.WaitGroup
		scanLines := func(r io.Reader) {
			defer wg.Done()
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				collectSync(decodeTextFile(scanner.Bytes()))
			}
		}
		wg.Add(2)
		go scanLines(stderrPipe)
		go scanLines(stdoutPipe)
		wg.Wait()

		err := cmd.Wait()
		a.cmdLock.Lock()
		a.cmd = nil
		a.cmdLock.Unlock()
		if err != nil {
			if a.stopped.Load() {
				a.log("warn", "同步已取消")
				return false
			}
			detail := "请检查视频和字幕文件是否有效"
			if syncLastLine != "" {
				detail = syncLastLine
			}
			a.log("error", "同步失败: "+detail)
			return false
		}

		var summary []string
		if syncOffset != "" {
			if f, e := strconv.ParseFloat(syncOffset, 64); e == nil {
				if f >= 0 {
					summary = append(summary, fmt.Sprintf("偏移 +%.2f 秒", f))
				} else {
					summary = append(summary, fmt.Sprintf("偏移 %.2f 秒", f))
				}
			}
		}
		if syncScale != "" && syncScale != "1" && syncScale != "1.0" && syncScale != "1.000" {
			summary = append(summary, fmt.Sprintf("速率缩放 %s", syncScale))
		}
		if len(summary) > 0 {
			a.log("info", "同步参数: "+strings.Join(summary, "，"))
		}
	}

	if strings.ToLower(filepath.Ext(outFile)) == ".srt" {
		cleanSRTFile(outFile)
	}
	a.log("ok", "同步 → "+filepath.Base(outFile))
	return true
}

var reSRTTag = regexp.MustCompile(`<[^>]+>`)

func stripSRTTags(s string) string {
	return reSRTTag.ReplaceAllString(s, "")
}

func (a *App) MergeSubtitles(file1, file2, outputDir string, topFile int, outputFmt string) bool {
	if file1 == "" || file2 == "" {
		a.log("error", "需要两个字幕文件")
		return false
	}
	if outputDir == "" {
		a.log("error", "未设置输出目录")
		return false
	}

	entries1 := a.LoadSubtitle(file1)
	entries2 := a.LoadSubtitle(file2)
	if len(entries1) == 0 {
		a.log("error", "字幕1 解析失败: "+filepath.Base(file1))
		return false
	}
	if len(entries2) == 0 {
		a.log("error", "字幕2 解析失败: "+filepath.Base(file2))
		return false
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败")
		return false
	}

	type msEntry struct {
		start, end int
		text       string
	}
	toMs := func(entries []SubtitleEntry) []msEntry {
		result := make([]msEntry, 0, len(entries))
		for _, e := range entries {
			s := subTimeToMs(e.Start, e.Format)
			en := subTimeToMs(e.End, e.Format)
			if en > s {
				result = append(result, msEntry{s, en, e.Text})
			}
		}
		return result
	}

	ms1 := toMs(entries1)
	ms2 := toMs(entries2)

	timeSet := make(map[int]bool)
	for _, e := range ms1 {
		timeSet[e.start] = true
		timeSet[e.end] = true
	}
	for _, e := range ms2 {
		timeSet[e.start] = true
		timeSet[e.end] = true
	}
	times := make([]int, 0, len(timeSet))
	for t := range timeSet {
		times = append(times, t)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })

	findText := func(entries []msEntry, t, tEnd int) string {
		for _, e := range entries {
			if e.start <= t && e.end >= tEnd {
				return e.text
			}
		}
		return ""
	}

	type mergedSeg struct {
		start, end int
		top, bot   string
	}

	var segments []mergedSeg
	for i := 0; i < len(times)-1; i++ {
		t := times[i]
		tEnd := times[i+1]
		t1 := findText(ms1, t, tEnd)
		t2 := findText(ms2, t, tEnd)
		if t1 == "" && t2 == "" {
			continue
		}
		top, bot := t1, t2
		if topFile == 2 {
			top, bot = t2, t1
		}
		if len(segments) > 0 {
			prev := &segments[len(segments)-1]
			if prev.end == t && prev.top == top && prev.bot == bot {
				prev.end = tEnd
				continue
			}
		}
		segments = append(segments, mergedSeg{t, tEnd, top, bot})
	}

	if len(segments) == 0 {
		a.log("warn", "两个字幕文件无时间重叠")
		return false
	}

	stem1 := strings.TrimSuffix(filepath.Base(file1), filepath.Ext(file1))
	outStem := stem1 + ".bilingual"

	var content, outExt string
	if outputFmt == "ass" {
		outExt = ".ass"
		var sb strings.Builder
		sb.WriteString("[Script Info]\n")
		sb.WriteString("ScriptType: v4.00+\n")
		sb.WriteString("PlayResX: 1920\nPlayResY: 1080\n\n")
		sb.WriteString("[V4+ Styles]\n")
		sb.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
		sb.WriteString("Style: Bottom,Arial,52,&H00FFFFFF,&H000000FF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,2,1,2,10,10,20,1\n")
		sb.WriteString("Style: Top,Arial,52,&H00FFFFFF,&H000000FF,&H00000000,&H80000000,0,0,0,0,100,100,0,0,1,2,1,8,10,10,20,1\n\n")
		sb.WriteString("[Events]\n")
		sb.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
		for _, seg := range segments {
			s := msToSubTime(seg.start, "ass")
			e := msToSubTime(seg.end, "ass")
			if seg.top != "" {
				sb.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Top,,0,0,0,,%s\n", s, e, stripSRTTags(seg.top)))
			}
			if seg.bot != "" {
				sb.WriteString(fmt.Sprintf("Dialogue: 0,%s,%s,Bottom,,0,0,0,,%s\n", s, e, stripSRTTags(seg.bot)))
			}
		}
		content = sb.String()
	} else {
		outExt = ".srt"
		var sb strings.Builder
		for i, seg := range segments {
			var text string
			switch {
			case seg.top != "" && seg.bot != "":
				text = seg.top + "\n" + seg.bot
			case seg.top != "":
				text = seg.top
			default:
				text = seg.bot
			}
			fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n", i+1, msToSubTime(seg.start, "srt"), msToSubTime(seg.end, "srt"), text)
		}
		content = strings.TrimRight(sb.String(), "\n") + "\n"
	}

	outFile := filepath.Join(outputDir, outStem+outExt)
	if err := os.WriteFile(outFile, []byte(content), 0644); err != nil {
		a.log("error", "写入失败: "+err.Error())
		return false
	}
	a.log("ok", fmt.Sprintf("双语合并 %s · %d 条", filepath.Base(outFile), len(segments)))
	return true
}

type FilenameAnalysisItem struct {
	VideoFile   string `json:"videoFile"`
	SubFile     string `json:"subFile"`
	Episode     string `json:"episode"`
	SuggestName string `json:"suggestName"`
	Confidence  string `json:"confidence"`
	Note        string `json:"note"`
}

type FilenameAnalysisResult struct {
	Items       []FilenameAnalysisItem `json:"items"`
	Summary     string                 `json:"summary"`
	HasIssues   bool                   `json:"hasIssues"`
	IssueDetail string                 `json:"issueDetail"`
}

func (a *App) AnalyzeFilenames(inputDir string) *FilenameAnalysisResult {
	if inputDir == "" {
		a.log("error", "未设置输入目录")
		return nil
	}

	if n := a.ensureVideoExtensions(inputDir); n > 0 {
		a.log("info", fmt.Sprintf("补后缀 %d 个", n))
	}

	videoFiles := findFiles(inputDir, videoExts)
	subtitleFiles := findFiles(inputDir, subtitleExts)

	if len(videoFiles) == 0 {
		a.log("error", "未找到视频文件")
		return nil
	}

	vNames := make([]string, len(videoFiles))
	for i, f := range videoFiles {
		vNames[i] = filepath.Base(f)
	}
	sNames := make([]string, len(subtitleFiles))
	for i, f := range subtitleFiles {
		sNames[i] = filepath.Base(f)
	}

	a.log("info", fmt.Sprintf("%s 分析文件名 · %d 视频 / %d 字幕", aiProviderLabel(), len(vNames), len(sNames)))

	localPairs := matchFilePairs(videoFiles, subtitleFiles)
	localPairMap := make(map[string]string)
	for _, p := range localPairs {
		if len(p.subtitles) > 0 {
			localPairMap[filepath.Base(p.video)] = filepath.Base(p.subtitles[0])
		}
	}

	var promptSB strings.Builder
	promptSB.WriteString("以下是视频压制工具的输入目录文件列表，请分析视频和字幕文件的对应关系，提取集数信息，并给出规范化的输出文件名建议。\n\n")
	promptSB.WriteString("【视频文件列表】\n")
	for i, n := range vNames {
		promptSB.WriteString(fmt.Sprintf("%d. %s\n", i+1, n))
	}
	promptSB.WriteString("\n【字幕文件列表】\n")
	if len(sNames) == 0 {
		promptSB.WriteString("（无字幕文件）\n")
	} else {
		for i, n := range sNames {
			promptSB.WriteString(fmt.Sprintf("%d. %s\n", i+1, n))
		}
	}
	promptSB.WriteString("\n【本地算法初步配对结果（供参考）】\n")
	for v, s := range localPairMap {
		promptSB.WriteString(fmt.Sprintf("  %s → %s\n", v, s))
	}

	systemPrompt := "你是" + brandActor() + "的视频文件名解析专家。请分析用户给出的视频和字幕文件名列表，完成以下任务：\n" +
		"1. 识别每个视频文件对应的字幕文件（按集数、文件名相似度匹配）\n" +
		"2. 提取集数信息（如 S01E02、第1集、[01]、EP01 等各种格式，统一转换为 E01 格式，单集/电影留空）\n" +
		"3. 给出规范化的建议输出文件名（去掉发布组、画质标签等冗余信息，保留剧名+集数）\n" +
		"   - 若文件名**只有集数没有剧名**（如 E01.mp4 / 01.mp4 / [01].mkv / 第1集.mp4），suggestName 直接用集数标签本身（如 \"E01\"），**严禁**使用 \"unknown\" / \"未知\" / \"untitled\" 等占位词\n" +
		"   - 若文件名是哈希/数字 ID 等无意义字符串（如 581260369_1、abc123），且无法识别集数，suggestName 保持原文件名（去扩展名）不变，**不要**输出 unknown\n" +
		"4. 判断是否存在命名问题（如文件名不规范、无法匹配字幕、集数重复等）\n\n" +
		"返回格式：严格返回 JSON 对象，格式如下，不要有任何解释或 markdown：\n" +
		"{\"items\":[{\"videoFile\":\"原视频文件名\",\"subFile\":\"匹配的字幕文件名或空字符串\",\"episode\":\"E01或空\",\"suggestName\":\"建议输出名不含扩展名\",\"confidence\":\"high/medium/low\",\"note\":\"备注或空\"},...],\"summary\":\"总体说明\",\"hasIssues\":true/false,\"issueDetail\":\"问题详情或空\"}"

	return a.analyzeFilenamesAI(systemPrompt, promptSB.String(), vNames, sNames, localPairMap)
}

func (a *App) analyzeFilenamesAI(systemPrompt, userContent string, vNames, sNames []string, localPairMap map[string]string) *FilenameAnalysisResult {
	apiKey := chatAPIKey()
	if apiKey == "" {
		a.log("error", "未配置 AI Key")
		return a.analyzeFilenamesLocal(vNames, sNames, localPairMap)
	}

	rawContent, err := chatContentCfg(apiKey, systemPrompt, userContent, 0.3, 4096, chatModelFast)
	if err != nil {
		a.log("error", aiProviderLabel()+" 调用失败: "+err.Error())
		return a.analyzeFilenamesLocal(vNames, sNames, localPairMap)
	}
	rawContent = strings.TrimSpace(rawContent)

	var result FilenameAnalysisResult
	if err := json.Unmarshal([]byte(rawContent), &result); err != nil {
		a.log("warn", aiProviderLabel()+" 响应格式异常，使用本地方案")
		return a.analyzeFilenamesLocal(vNames, sNames, localPairMap)
	}

	sanitizeSuggestName := func(suggest, video, episode string) string {
		s := strings.TrimSpace(suggest)
		lower := strings.ToLower(s)
		looksUnknown := lower == "unknown" || lower == "未知" || lower == "untitled" ||
			strings.HasPrefix(lower, "unknown.") || strings.HasPrefix(lower, "unknown_") ||
			strings.HasPrefix(lower, "unknown-") || strings.HasPrefix(lower, "未知.") ||
			strings.HasPrefix(lower, "untitled.")
		if !looksUnknown {
			return s
		}
		if episode != "" {
			return episode
		}
		return strings.TrimSuffix(video, filepath.Ext(video))
	}
	for i := range result.Items {
		result.Items[i].SuggestName = sanitizeSuggestName(
			result.Items[i].SuggestName,
			result.Items[i].VideoFile,
			result.Items[i].Episode,
		)
	}

	a.log("ok", fmt.Sprintf("%s 分析 %d 对 · 问题 %v", chatProviderUsed(), len(result.Items), result.HasIssues))
	return &result
}

func (a *App) analyzeFilenamesLocal(vNames, sNames []string, localPairMap map[string]string) *FilenameAnalysisResult {
	result := &FilenameAnalysisResult{}
	seenEps := make(map[string]bool)

	for _, v := range vNames {
		stem := strings.TrimSuffix(v, filepath.Ext(v))
		_, epTag := extractEpisodeTag(stem)

		item := FilenameAnalysisItem{
			VideoFile:  v,
			SubFile:    localPairMap[v],
			Episode:    epTag,
			Confidence: "medium",
		}

		suggestStem := cleanFilename(stem)
		item.SuggestName = suggestStem

		if epTag != "" {
			if seenEps[epTag] {
				item.Note = "集数重复，输出文件名可能覆盖"
				item.Confidence = "low"
				result.HasIssues = true
			}
			seenEps[epTag] = true
		}

		if item.SubFile == "" && len(sNames) > 0 {
			if item.Note != "" {
				item.Note += "；未找到匹配字幕"
			} else {
				item.Note = "未找到匹配字幕"
			}
			result.HasIssues = true
		}

		result.Items = append(result.Items, item)
	}

	result.Summary = fmt.Sprintf("本地分析完成：%d 个视频，%d 个字幕", len(vNames), len(sNames))
	if result.HasIssues {
		result.IssueDetail = "存在命名问题，建议先整理文件名再处理"
	}
	return result
}

type RenameEntry struct {
	OldName string `json:"oldName"`
	NewName string `json:"newName"`
}

type RenameResult struct {
	OldName string `json:"oldName"`
	NewName string `json:"newName"`
	OK      bool   `json:"ok"`
	Skipped bool   `json:"skipped"`
	Err     string `json:"err"`
}

func (a *App) ApplyFilenameRenames(inputDir string, entries []RenameEntry) []RenameResult {
	results := make([]RenameResult, 0, len(entries))

	for _, e := range entries {
		res := RenameResult{OldName: e.OldName, NewName: e.NewName}

		if e.OldName == "" || e.NewName == "" {
			res.Skipped = true
			res.Err = "文件名为空，跳过"
			results = append(results, res)
			continue
		}

		if e.OldName == e.NewName {
			res.OK = true
			res.Skipped = true
			results = append(results, res)
			continue
		}

		oldPath := filepath.Join(inputDir, e.OldName)
		newPath := filepath.Join(inputDir, e.NewName)

		if _, err := os.Stat(oldPath); os.IsNotExist(err) {
			res.Err = "源文件不存在"
			results = append(results, res)
			continue
		}

		if _, err := os.Stat(newPath); err == nil {
			res.Skipped = true
			res.Err = "目标文件名已存在，跳过以避免覆盖"
			results = append(results, res)
			continue
		}

		if err := os.Rename(oldPath, newPath); err != nil {
			res.Err = err.Error()
			results = append(results, res)
			continue
		}

		res.OK = true
		a.log("ok", fmt.Sprintf("重命名 %s → %s", e.OldName, e.NewName))
		results = append(results, res)
	}

	okCount := 0
	for _, r := range results {
		if r.OK && r.OldName != r.NewName {
			okCount++
		}
	}
	if okCount > 0 {
		a.log("ok", fmt.Sprintf("批量重命名完成，成功 %d / %d 条", okCount, len(entries)))
	}
	return results
}
