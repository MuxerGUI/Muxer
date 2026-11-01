package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	webNameSuffix  = ".在线版"
	webDefaultKbps = 3000
	webMinKbps     = 500
	webMaxKbps     = 30000
	webLongEdge    = 1920
	webShortEdge   = 1080
	webAudioKbps   = "128k"
)

func webFileName(stem string) string {
	return stem + webNameSuffix + ".mp4"
}

func isWebRenditionPath(path string) bool {
	base := filepath.Base(path)
	if !strings.EqualFold(filepath.Ext(base), ".mp4") {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(base, filepath.Ext(base)), webNameSuffix)
}

func dropWebRenditions(files []string) ([]string, int) {
	kept := make([]string, 0, len(files))
	for _, f := range files {
		if isWebRenditionPath(f) {
			continue
		}
		kept = append(kept, f)
	}
	return kept, len(files) - len(kept)
}

func chooseWebBitrate(info *videoInfo, want int) int {
	src := 0
	if info != nil {
		src = info.VideoBitrateKbps
		if src <= 0 {
			src = info.SizeBitrateKbps
		}
	}
	kbps := want
	if kbps <= 0 {
		if src > 0 {
			kbps = src
			if kbps > webDefaultKbps {
				kbps = webDefaultKbps
			}
		} else {
			kbps = webDefaultKbps
		}
	}
	if kbps > webMaxKbps {
		kbps = webMaxKbps
	}
	if src > 0 && kbps > src {
		kbps = src
	}
	if kbps < webMinKbps {
		kbps = webMinKbps
	}
	return kbps
}

type webSpec struct {
	Path        string
	Width       int
	Height      int
	BitrateKbps int
	Codec       string
	GOP         int
}

func evenDim(v float64) int {
	n := int(v + 0.5)
	if n%2 != 0 {
		n--
	}
	if n < 2 {
		n = 2
	}
	return n
}

func webScale(w, h int) (int, int, bool) {
	if w <= 0 || h <= 0 {
		return 0, 0, false
	}
	long, short := w, h
	if short > long {
		long, short = short, long
	}
	ratio := math.Min(float64(webLongEdge)/float64(long), float64(webShortEdge)/float64(short))
	if ratio >= 1 {
		return 0, 0, false
	}
	return evenDim(float64(w) * ratio), evenDim(float64(h) * ratio), true
}

func webEncArgs(web *webSpec) []string {
	cv, pix := "hevc_nvenc", "p010le"
	if web.Codec == "h264" {
		cv, pix = "h264_nvenc", "yuv420p"
	}
	gop := fmt.Sprintf("%d", web.GOP)
	args := []string{
		"-c:v", cv,
		"-preset", "p4",
	}
	args = append(args, nvencTuneArgs(cv)...)
	return append(args,
		"-rc", "vbr",
		"-b:v", fmt.Sprintf("%dk", web.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", web.BitrateKbps*3/2),
		"-bufsize", fmt.Sprintf("%dk", web.BitrateKbps*3),
		"-spatial-aq", "1",
		"-aq-strength", "8",
		"-rc-lookahead", "32",
		"-g", gop,
		"-keyint_min", gop,
		"-pix_fmt", pix,
	)
}

func webAudioArgs() []string {
	return []string{"-c:a", "aac", "-b:a", webAudioKbps, "-ac", "2"}
}

func webSplitFilter(chain, inputLabel, srcLabel string, web *webSpec) string {
	return webSplitFilterEx(chain, inputLabel, srcLabel, web, "")
}

func webSplitFilterEx(chain, inputLabel, srcLabel string, web *webSpec, extra string) string {
	var graph string
	if srcLabel == "" {
		graph = fmt.Sprintf("[%s]%s,split=2[vmaster][vweb]", inputLabel, chain)
	} else {
		graph = fmt.Sprintf("%s;[%s]split=2[vmaster][vweb]", chain, srcLabel)
	}
	var webChain []string
	if extra = strings.TrimSpace(extra); extra != "" {
		webChain = append(webChain, extra)
	}
	if web != nil && web.Width > 0 {
		webChain = append(webChain, fmt.Sprintf("scale=%d:%d:flags=lanczos", web.Width, web.Height))
	}
	if len(webChain) == 0 {
		webChain = append(webChain, "null")
	}
	return graph + ";[vweb]" + strings.Join(webChain, ",") + "[vwebout]"
}

func (a *App) buildWebSpec(outputDir, baseName string, info *videoInfo, bitrateKbps int, codec string) *webSpec {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		a.log("warn", "在线版目录创建失败: "+err.Error())
		return nil
	}

	kbps := chooseWebBitrate(info, bitrateKbps)
	if codec != "h264" {
		codec = "h265"
	}

	web := &webSpec{
		Path:        filepath.Join(outputDir, webFileName(baseName)),
		BitrateKbps: kbps,
		Codec:       codec,
		GOP:         gopForFPS(info.FPS),
	}
	res := fmt.Sprintf("%dx%d", info.Width, info.Height)
	if w, h, scaled := webScale(info.Width, info.Height); scaled {
		web.Width, web.Height = w, h
		res = fmt.Sprintf("%dx%d", w, h)
	}

	a.log("info", fmt.Sprintf("在线版 %s · %s · %d kbps · 预估 %s",
		res, strings.ToUpper(web.Codec), web.BitrateKbps,
		friendlyFileSize(int64(float64(web.BitrateKbps+128)*info.Duration*1000/8))))
	return web
}

func webOutputValid(path string) bool {
	return webOutputAcceptable(path, 0)
}

func webOutputAcceptable(path string, duration float64) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	minSize := int64(1) << 20
	if duration > 0 {
		if byDur := int64(duration * 20 * 1024); byDur < minSize {
			minSize = byDur
		}
	}
	return st.Size() >= minSize
}

func webOutputUsable(path string, duration float64) bool {
	return webOutputAcceptable(path, duration) && containerHasDuration(path)
}

func videoStreamLabel(info *videoInfo) string {
	if info != nil && info.VideoStreamIndex >= 0 {
		return fmt.Sprintf("0:%d", info.VideoStreamIndex)
	}
	return "0:v:0"
}

func parseCropWH(expr string) (int, int, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, 0, false
	}
	expr = strings.TrimPrefix(expr, "crop=")
	parts := strings.Split(expr, ":")
	if len(parts) < 2 {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || w < 2 || h < 2 {
		return 0, 0, false
	}
	return w, h, true
}

func hardsubFrameSize(info *videoInfo, opts ProcessOptions) (int, int) {
	if info == nil {
		return 0, 0
	}
	w, h := info.Width, info.Height
	if cw, ch, ok := parseCropWH(opts.CropExpr); ok {
		w, h = cw, ch
	}
	if opts.ScaleHeight > 0 && h > 0 {
		w = evenDim(float64(w) * float64(opts.ScaleHeight) / float64(h))
		h = opts.ScaleHeight
	}
	return w, h
}

func webPathForMaster(masterPath string) string {
	stem := strings.TrimSuffix(masterPath, filepath.Ext(masterPath))
	return stem + webNameSuffix + ".mp4"
}

func (a *App) avoidWebNameCollision(outputPath string) string {
	if _, err := os.Stat(webPathForMaster(outputPath)); err != nil {
		return outputPath
	}
	a.log("warn", "在线版已存在，改名: "+filepath.Base(webPathForMaster(outputPath)))
	ext := filepath.Ext(outputPath)
	stem := strings.TrimSuffix(outputPath, ext)
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(cand); err == nil {
			continue
		}
		if _, err := os.Stat(webPathForMaster(cand)); err == nil {
			continue
		}
		a.log("info", "本次输出: "+filepath.Base(cand))
		return cand
	}
	return fmt.Sprintf("%s.%d%s", stem, time.Now().Unix(), ext)
}

func (a *App) resolveWebCodec(codec string) string {
	if codec == "h264" {
		if a.checkEncoderAvailable("h264_nvenc") {
			return "h264"
		}
		a.log("warn", "无 h264_nvenc，不出在线版")
		return ""
	}
	if a.checkEncoderAvailable("hevc_nvenc") {
		return "h265"
	}
	if a.checkEncoderAvailable("h264_nvenc") {
		a.log("warn", "无 hevc_nvenc，在线版用 h264")
		return "h264"
	}
	a.log("warn", "无 NVENC，不出在线版")
	return ""
}

func (a *App) attachWebSpec(opts *ProcessOptions, outputPath string, info *videoInfo, subtitleFile string) {
	if opts == nil || !opts.WebVersion {
		return
	}
	if !opts.Hardsub {
		a.log("warn", "软封不出在线版")
		opts.WebVersion = false
		return
	}
	if info == nil {
		opts.WebVersion = false
		return
	}
	codec := a.resolveWebCodec(opts.WebCodec)
	if codec == "" {
		opts.WebVersion = false
		return
	}
	opts.WebCodec = codec
	stem := strings.TrimSuffix(filepath.Base(outputPath), filepath.Ext(outputPath))
	sizeOpts := *opts
	if isBitmapSubPath(subtitleFile) {
		sizeOpts.CropExpr = ""
	}
	webInfo := *info
	if w, h := hardsubFrameSize(info, sizeOpts); w > 0 {
		webInfo.Width, webInfo.Height = w, h
	}
	opts.web = a.buildWebSpec(filepath.Dir(outputPath), stem, &webInfo, opts.WebBitrateKbps, codec)
}

func appendWebOutput(args []string, web *webSpec, audioMap, af, chapterMap string, bitmapDuration float64, writeSDR bool) []string {
	if web == nil {
		return args
	}
	args = append(args, webEncArgs(web)...)
	args = append(args,
		"-map", "[vwebout]",
		"-map", audioMap,
	)
	args = append(args, webAudioArgs()...)
	if af != "" {
		args = append(args, "-af", af)
	}
	args = append(args,
		"-map_chapters", chapterMap,
		"-map_metadata", "-1",
	)
	args = append(args, brandMetadataArgs()...)
	if writeSDR {
		args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	}
	args = append(args, "-movflags", "+faststart")
	if bitmapDuration > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", bitmapDuration))
	}
	return append(args, web.Path)
}

func (a *App) finalizeWebOutput(wantWeb bool, webPath string, duration float64) {
	if !wantWeb {
		return
	}
	if webOutputUsable(webPath, duration) {
		if st, err := os.Stat(webPath); err == nil {
			a.log("ok", fmt.Sprintf("在线版 %s · %s",
				filepath.Base(webPath), friendlyFileSize(st.Size())))
		}
		return
	}
	if webPath != "" {
		if st, err := os.Stat(webPath); err == nil && st.Size() > 0 {
			a.log("warn", "在线版异常，保留: "+filepath.Base(webPath))
		} else {
			os.Remove(webPath)
		}
	}
	a.keepSourceFlag.Store(true)
	a.log("warn", "在线版未产出，保留源文件")
}
