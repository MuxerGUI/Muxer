package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

type WatermarkOptions struct {
	InputDir       string `json:"inputDir"`
	OutputDir      string `json:"outputDir"`
	Notify         bool   `json:"notify"`
	AutoOpenOutput bool   `json:"autoOpenOutput"`
	WebVersion     bool   `json:"webVersion"`
	WebBitrateKbps int    `json:"webBitrateKbps"`
	WebCodec       string `json:"webCodec"`
}

const (
	wmCornerWin  = 30.0
	wmCornerMinS = 6.0
	wmCornerMaxS = 10.0
	wmCenterMinG = 90.0
	wmCenterMaxG = 180.0
	wmCenterMinS = 5.0
	wmCenterMaxS = 8.0
	wmShotCount  = 15
)

func wmFontFor(text string) string { return fontFileOrEmpty(hasCJK(text)) }

func wmCJKFontFile() string { return fontFileOrEmpty(true) }

const (
	defaultWatermarkBrand = "寻芳"
	defaultWatermarkURL   = "xunfang.io"
)

func wmURL() string {
	if v := strings.TrimSpace(keys().WatermarkURL); v != "" {
		return v
	}
	if keysFilePresent {
		return defaultWatermarkURL
	}
	return ""
}
func wmBrand() string {
	if v := strings.TrimSpace(keys().WatermarkBrand); v != "" {
		return v
	}
	if keysFilePresent {
		return defaultWatermarkBrand
	}
	return ""
}
func wmTagline() string { return strings.TrimSpace(keys().WatermarkTagline) }

func wmHasBranding() bool { return wmURL() != "" || wmBrand() != "" || wmTagline() != "" }

func wmArtist() string {
	b, u := wmBrand(), wmURL()
	switch {
	case b != "" && u != "":
		return b + " " + u
	case b != "":
		return b
	default:
		return u
	}
}

func wmComment() string {
	head := wmBrand() + wmURL()
	t := wmTagline()
	switch {
	case head != "" && t != "":
		return head + " " + t
	case head != "":
		return head
	default:
		return t
	}
}

func wmTags() []string {
	var tags []string
	if u := wmURL(); u != "" {
		tags = append(tags, u)
	}
	if b := wmBrand(); b != "" {
		tags = append(tags, b)
	}
	return tags
}

func wmWriteJPEGBranding(jpgPath, title string) {
	if !wmHasBranding() {
		return
	}
	_ = writeJPEGXMP(jpgPath, title, wmComment(), wmArtist(), wmURL(), wmTags())
}

var wmDomainPrefixRe = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9\-]*(\.[a-z0-9\-]+)+@`)
var wmNumericIDRe = regexp.MustCompile(`[_\-]\d{5,}(?:[_\-]\d+)*`)
var wmBracketTagRe = regexp.MustCompile(`\[.*?\]|\(.*?\)|【.*?】|（.*?）`)
var wmTrashWordsRe = regexp.MustCompile(`(?i)\b(UNRATED|REMASTERED|PROPER|BluRay|WEB-?DL|HDTV|HEVC|x265|x264|AAC|FLAC|5\.1|10bit|HDR|SDR|DDP?\d*)\b`)
var wmMultiDotRe = regexp.MustCompile(`[\s_.\-]{2,}`)

func wmCleanFilename(name string) string {
	s := name
	s = wmDomainPrefixRe.ReplaceAllString(s, "")
	s = wmBracketTagRe.ReplaceAllString(s, "")
	s = strings.TrimLeft(s, ".-_ ")
	s = wmNumericIDRe.ReplaceAllString(s, "")
	s = wmTrashWordsRe.ReplaceAllString(s, "")
	s = wmMultiDotRe.ReplaceAllString(s, ".")
	s = strings.Trim(s, ".-_ ")
	if s == "" {
		s = name
	}
	return s
}

func wmJitter(baseFS int, baseOp, baseShOp float64) (fs int, op, shOp float64, shx, shy int) {
	fs = int(float64(baseFS) * (0.90 + rand.Float64()*0.20))
	if fs < 12 {
		fs = baseFS
	}
	op = baseOp - 0.04 + rand.Float64()*0.08
	if op < 0.10 {
		op = 0.10
	}
	shOp = baseShOp - 0.05 + rand.Float64()*0.10
	if shOp < 0.10 {
		shOp = 0.10
	}
	shx = 1 + rand.Intn(2)
	shy = 1 + rand.Intn(2)
	if rand.Intn(2) == 0 {
		shx = -shx
	}
	if rand.Intn(2) == 0 {
		shy = -shy
	}
	return
}

func (a *App) buildWatermarkFilter(duration float64, srcHeight int) string {
	if duration <= 5 {
		return ""
	}
	wt := wmURL()
	if wt == "" {
		a.log("info", "未配置水印文字，跳过屏幕水印")
		return ""
	}
	wt = dtEscape(wt)
	if srcHeight <= 0 {
		srcHeight = 1080
	}

	cornerFS := srcHeight / 40
	if cornerFS < 20 {
		cornerFS = 20
	}
	centerFS := srcHeight / 16
	if centerFS < 48 {
		centerFS = 48
	}

	parts := make([]string, 0, 64)

	cornerCnt := 0
	tnow := 0.0
	cutoff := duration * 0.97
	for tnow+wmCornerMinS < cutoff {
		showLen := wmCornerMinS + rand.Float64()*(wmCornerMaxS-wmCornerMinS)
		maxStart := wmCornerWin - showLen
		if maxStart < 0 {
			maxStart = 0
		}
		st := tnow + rand.Float64()*maxStart
		ed := st + showLen
		if ed > cutoff {
			ed = cutoff
		}
		if ed-st < 1 {
			tnow += wmCornerWin
			continue
		}
		idx := rand.Intn(4)
		offX := 36 + rand.Intn(25)
		offY := 36 + rand.Intn(25)
		var xExpr, yExpr string
		switch idx {
		case 0:
			xExpr, yExpr = fmt.Sprintf("%d", offX), fmt.Sprintf("%d", offY)
		case 1:
			xExpr, yExpr = fmt.Sprintf("w-tw-%d", offX), fmt.Sprintf("%d", offY)
		case 2:
			xExpr, yExpr = fmt.Sprintf("%d", offX), fmt.Sprintf("h-th-%d", offY)
		case 3:
			xExpr, yExpr = fmt.Sprintf("w-tw-%d", offX), fmt.Sprintf("h-th-%d", offY)
		}
		fs, op, shOp, shx, shy := wmJitter(cornerFS, 0.55, 0.45)
		parts = append(parts, fmt.Sprintf(
			"drawtext=text='%s':fontcolor=white@%.2f:fontsize=%d:x=%s:y=%s:shadowcolor=black@%.2f:shadowx=%d:shadowy=%d:enable='between(t\\,%.2f\\,%.2f)':expansion=none:fontfile='%s'",
			wt, op, fs, xExpr, yExpr, shOp, shx, shy, st, ed, wmFontFor(wt)))
		cornerCnt++
		tnow += wmCornerWin
	}

	centerCnt := 0
	t := 25.0 + rand.Float64()*15.0
	for t < cutoff {
		showLen := wmCenterMinS + rand.Float64()*(wmCenterMaxS-wmCenterMinS)
		st := t
		ed := st + showLen
		if ed > cutoff {
			break
		}
		xRatio := 0.20 + rand.Float64()*0.55
		yRatio := 0.22 + rand.Float64()*0.50
		xExpr := fmt.Sprintf("(w-tw)*%.3f", xRatio)
		yExpr := fmt.Sprintf("(h-th)*%.3f", yRatio)
		fs, op, shOp, shx, shy := wmJitter(centerFS, 0.20, 0.25)
		parts = append(parts, fmt.Sprintf(
			"drawtext=text='%s':fontcolor=white@%.2f:fontsize=%d:x=%s:y=%s:shadowcolor=black@%.2f:shadowx=%d:shadowy=%d:enable='between(t\\,%.2f\\,%.2f)':expansion=none:fontfile='%s'",
			wt, op, fs, xExpr, yExpr, shOp, shx, shy, st, ed, wmFontFor(wt)))
		centerCnt++
		gap := wmCenterMinG + rand.Float64()*(wmCenterMaxG-wmCenterMinG)
		t = ed + gap
	}

	a.log("info", fmt.Sprintf("水印 %d 角落 / %d 中央", cornerCnt, centerCnt))
	return strings.Join(parts, ",")
}

func wmTonemapFilter(info *videoInfo, videoFile string) string {
	primaries, transfer, matrix := hdrMetaOf(info, videoFile)
	if transfer != "smpte2084" && transfer != "arib-std-b67" {
		return ""
	}
	if primaries == "" || primaries == "unknown" {
		primaries = "bt2020"
	}
	if matrix == "" || matrix == "unknown" {
		matrix = "bt2020nc"
	}
	return fmt.Sprintf(
		"zscale=t=linear:npl=100:tin=%s:pin=%s:min=%s,format=gbrpf32le,zscale=p=bt709:t=bt709:m=bt709,format=yuv420p",
		transfer, primaries, matrix)
}

func (a *App) buildWatermarkVF(info *videoInfo, videoFile string) string {
	var parts []string
	if tm := wmTonemapFilter(info, videoFile); tm != "" {
		a.log("info", "HDR→SDR")
		parts = append(parts, tm)
	}
	if wm := a.buildWatermarkFilter(info.Duration, info.Height); wm != "" {
		parts = append(parts, wm)
	}
	return strings.Join(append(parts, "format=yuv420p"), ",")
}

func wmVideoLabel(info *videoInfo) string { return videoStreamLabel(info) }

func wmMasterEncArgs(fps float64) []string {
	return []string{
		"-c:v", "h264_nvenc",
		"-preset", "p1",
		"-rc", "vbr",
		"-cq", "28",
		"-b:v", "0",
		"-maxrate", "12M",
		"-bufsize", "24M",
		"-g", fmt.Sprintf("%d", gopForFPS(fps)),
		"-pix_fmt", "yuv420p",
	}
}

func wmMetadataArgs(title string) []string {
	args := []string{"-metadata", "title=" + title}
	if v := wmArtist(); v != "" {
		args = append(args, "-metadata", "artist="+v)
	}
	if v := wmComment(); v != "" {
		args = append(args, "-metadata", "comment="+v, "-metadata", "description="+v)
	}
	if v := wmURL(); v != "" {
		args = append(args, "-metadata", "copyright="+v)
	}
	return args
}

func (a *App) buildWatermarkCmd(videoFile, outputPath, vf, title string, info *videoInfo, useCuda bool, web *webSpec) (*exec.Cmd, string) {
	graph := vf
	if web != nil {
		graph = webSplitFilter(vf, wmVideoLabel(info), "", web)
	}

	tmpPath := ""
	if tmpFile, err := os.CreateTemp("", "muxer-wm-*.txt"); err == nil {
		_, _ = tmpFile.WriteString(graph)
		tmpFile.Close()
		tmpPath = tmpFile.Name()
	}

	args := []string{"ffmpeg", "-y"}
	if useCuda {
		args = append(args, "-hwaccel", "cuda")
	}
	args = append(args, "-i", videoFile)
	switch {
	case web != nil && tmpPath != "":
		args = append(args, "-filter_complex_script", tmpPath)
	case web != nil:
		args = append(args, "-filter_complex", graph)
	case tmpPath != "":
		args = append(args, "-filter_script:v", tmpPath)
	default:
		args = append(args, "-vf", graph)
	}

	args = append(args, wmMasterEncArgs(info.FPS)...)
	args = append(args, timestampRepairArgs(info)...)
	if web != nil {
		args = append(args, "-map", "[vmaster]")
	} else if info.VideoStreamIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", info.VideoStreamIndex))
	} else {
		args = append(args, "-map", "0:V:0")
	}
	args = append(args,
		"-map", "0:a?",
		"-map_chapters", "0",
		"-map_metadata", "-1",
		"-c:a", "copy",
	)
	args = append(args, wmMetadataArgs(title)...)
	args = append(args, outputPath)

	if web != nil {
		args = append(args, webEncArgs(web)...)
		args = append(args, timestampRepairArgs(info)...)
		args = append(args,
			"-map", "[vwebout]",
			"-map", "0:a:0?",
		)
		args = append(args, webAudioArgs()...)
		args = append(args,
			"-map_chapters", "0",
			"-map_metadata", "-1",
			"-movflags", "+faststart",
		)
		args = append(args, wmMetadataArgs(title)...)
		args = append(args, web.Path)
	}

	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	return cmd, tmpPath
}

func formatTimestamp(seconds float64) string {
	total := int(seconds)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func xmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

func writeJPEGXMP(jpgPath, title, comment, author, copyright string, tags []string) error {
	data, err := os.ReadFile(jpgPath)
	if err != nil {
		return err
	}
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 {
		return fmt.Errorf("not a JPEG")
	}

	tagXML := ""
	for _, t := range tags {
		tagXML += "<rdf:li>" + xmlEsc(t) + "</rdf:li>"
	}

	creatorToolXML := ""
	if u := wmURL(); u != "" {
		creatorToolXML = "<xmp:CreatorTool>" + xmlEsc(u) + "</xmp:CreatorTool>"
	}

	xmp := "<?xpacket begin='\xEF\xBB\xBF' id='W5M0MpCehiHzreSzNTczkc9d'?>" +
		"<x:xmpmeta xmlns:x='adobe:ns:meta/'>" +
		"<rdf:RDF xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'>" +
		"<rdf:Description rdf:about=''" +
		" xmlns:dc='http://purl.org/dc/elements/1.1/'" +
		" xmlns:xmp='http://ns.adobe.com/xap/1.0/'>" +
		"<dc:title><rdf:Alt><rdf:li xml:lang='x-default'>" + xmlEsc(title) + "</rdf:li></rdf:Alt></dc:title>" +
		"<dc:description><rdf:Alt><rdf:li xml:lang='x-default'>" + xmlEsc(comment) + "</rdf:li></rdf:Alt></dc:description>" +
		"<dc:creator><rdf:Seq><rdf:li>" + xmlEsc(author) + "</rdf:li></rdf:Seq></dc:creator>" +
		"<dc:rights><rdf:Alt><rdf:li xml:lang='x-default'>" + xmlEsc(copyright) + "</rdf:li></rdf:Alt></dc:rights>" +
		"<dc:subject><rdf:Bag>" + tagXML + "</rdf:Bag></dc:subject>" +
		creatorToolXML +
		"</rdf:Description>" +
		"</rdf:RDF>" +
		"</x:xmpmeta>" +
		"<?xpacket end='w'?>"

	ns := "http://ns.adobe.com/xap/1.0/\x00"
	payload := append([]byte(ns), []byte(xmp)...)
	segLen := 2 + len(payload)
	seg := make([]byte, 4+len(payload))
	seg[0] = 0xFF
	seg[1] = 0xE1
	seg[2] = byte(segLen >> 8)
	seg[3] = byte(segLen & 0xFF)
	copy(seg[4:], payload)

	out := make([]byte, 0, len(data)+len(seg))
	out = append(out, data[:2]...)
	out = append(out, seg...)
	out = append(out, data[2:]...)
	return os.WriteFile(jpgPath, out, 0644)
}

type coverInfo struct {
	Width      int
	Height     int
	VideoCodec string
	PixFmt     string
	ColorPri   string
	ColorTRC   string
	AudioCodec string
	Channels   int
	SampleRate int
	ABitrateK  int
	VBitrateK  int
	FPS        float64
	Duration   float64
	FileSize   int64
	Container  string
}

func probeCoverInfo(videoFile string) *coverInfo {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_format", "-show_streams", videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil
	}
	ci := &coverInfo{}
	ci.Container = strings.TrimPrefix(strings.ToLower(filepath.Ext(videoFile)), ".")
	if st, err := os.Stat(videoFile); err == nil {
		ci.FileSize = st.Size()
	}
	if fmtMap, ok := raw["format"].(map[string]interface{}); ok {
		if dur, ok := fmtMap["duration"].(string); ok {
			fmt.Sscanf(dur, "%f", &ci.Duration)
		}
		if ci.VBitrateK == 0 {
			if br, ok := fmtMap["bit_rate"].(string); ok {
				var bps int
				fmt.Sscanf(br, "%d", &bps)
				ci.VBitrateK = bps / 1000
			}
		}
	}
	if streams, ok := raw["streams"].([]interface{}); ok {
		for _, s := range streams {
			stream, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			ct, _ := stream["codec_type"].(string)
			switch ct {
			case "video":
				if ci.VideoCodec != "" {
					continue
				}
				ci.VideoCodec, _ = stream["codec_name"].(string)
				ci.PixFmt, _ = stream["pix_fmt"].(string)
				ci.ColorPri, _ = stream["color_primaries"].(string)
				ci.ColorTRC, _ = stream["color_transfer"].(string)
				if w, ok := stream["width"].(float64); ok {
					ci.Width = int(w)
				}
				if h, ok := stream["height"].(float64); ok {
					ci.Height = int(h)
				}
				if br, ok := stream["bit_rate"].(string); ok {
					var bps int
					fmt.Sscanf(br, "%d", &bps)
					ci.VBitrateK = bps / 1000
				}
				rRate, _ := stream["r_frame_rate"].(string)
				avgRate, _ := stream["avg_frame_rate"].(string)
				ci.FPS, _ = chooseVideoFPS(rRate, avgRate)
			case "audio":
				if ci.AudioCodec != "" {
					continue
				}
				ci.AudioCodec, _ = stream["codec_name"].(string)
				if ch, ok := stream["channels"].(float64); ok {
					ci.Channels = int(ch)
				}
				if sr, ok := stream["sample_rate"].(string); ok {
					fmt.Sscanf(sr, "%d", &ci.SampleRate)
				}
				if br, ok := stream["bit_rate"].(string); ok {
					var bps int
					fmt.Sscanf(br, "%d", &bps)
					ci.ABitrateK = bps / 1000
				}
			}
		}
	}
	return ci
}

func coverCodecName(c string) string {
	switch strings.ToLower(c) {
	case "h264", "libx264", "h264_nvenc":
		return "H.264"
	case "hevc", "h265", "libx265", "hevc_nvenc":
		return "H.265"
	case "av1", "libsvtav1", "av1_nvenc":
		return "AV1"
	case "vp9", "libvpx-vp9":
		return "VP9"
	case "aac":
		return "AAC"
	case "opus":
		return "Opus"
	case "flac":
		return "FLAC"
	case "ac3", "eac3":
		return strings.ToUpper(c)
	case "dts":
		return "DTS"
	case "mp3", "mp2":
		return strings.ToUpper(c)
	case "pcm_s16le", "pcm_s24le", "pcm_f32le":
		return "PCM"
	default:
		if c == "" {
			return ""
		}
		return strings.ToUpper(c)
	}
}

func coverBitDepth(pixFmt string) string {
	if strings.Contains(pixFmt, "10le") || strings.Contains(pixFmt, "10be") {
		return "10bit"
	}
	if strings.Contains(pixFmt, "12le") || strings.Contains(pixFmt, "12be") {
		return "12bit"
	}
	if pixFmt != "" {
		return "8bit"
	}
	return ""
}

func coverHDR(colorTRC string) string {
	switch strings.ToLower(colorTRC) {
	case "smpte2084", "arib-std-b67":
		return "HDR"
	case "":
		return ""
	default:
		return "SDR"
	}
}

func coverChannels(ch int) string {
	switch ch {
	case 1:
		return "单声道"
	case 2:
		return "立体声"
	case 6:
		return "5.1声道"
	case 8:
		return "7.1声道"
	default:
		if ch > 0 {
			return fmt.Sprintf("%d声道", ch)
		}
		return ""
	}
}

func coverFileSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes > 0:
		return fmt.Sprintf("%.0f KB", float64(bytes)/float64(1<<10))
	default:
		return ""
	}
}

func (a *App) extractPreviewShots(videoFile, outDir, baseName string) {
	ci := probeCoverInfo(videoFile)
	if ci == nil {
		a.log("warn", "无法读取输出视频信息，跳过封面生成")
		return
	}
	if ci.Duration < 30 {
		return
	}
	shotDir := filepath.Join(outDir, "screenshots")
	if err := os.MkdirAll(shotDir, 0755); err != nil {
		a.log("warn", "创建截图目录失败: "+err.Error())
		return
	}
	var shotPaths []string
	for i := 1; i <= wmShotCount; i++ {
		ts := ci.Duration * float64(i) / float64(wmShotCount+1)
		shotPath := filepath.Join(shotDir, fmt.Sprintf("%s 截图%d.jpg", baseName, i))
		timeLabel := strings.ReplaceAll(formatTimestamp(ts), ":", `\:`)
		vf := fmt.Sprintf("drawtext=text='%s':fontcolor=white:fontsize=28:x=16:y=h-th-16:shadowcolor=black@0.6:shadowx=1:shadowy=1:expansion=none:fontfile='%s'", timeLabel, fontFileOrEmpty(false))
		cmd := exec.Command("ffmpeg", "-y", "-ss", fmt.Sprintf("%.3f", ts),
			"-i", videoFile, "-vf", vf, "-frames:v", "1", "-q:v", "2", shotPath)
		hideWindow(cmd)
		if err := cmd.Run(); err != nil {
			a.log("warn", fmt.Sprintf("截图 %d/%d 失败: %s", i, wmShotCount, err.Error()))
			continue
		}
		shotPaths = append(shotPaths, shotPath)
		shotTitle := fmt.Sprintf("%s 截图%d", baseName, i)
		wmWriteJPEGBranding(shotPath, shotTitle)
	}
	a.log("ok", fmt.Sprintf("预览 %d 张", len(shotPaths)))

	if len(shotPaths) == wmShotCount {
		gridPath := filepath.Join(outDir, baseName+".封面.jpg")
		a.buildPreviewGrid(shotPaths, gridPath, baseName, ci)

		pureGridPath := filepath.Join(shotDir, baseName+" 精品封面.jpg")
		a.buildPureGrid(shotPaths, pureGridPath, baseName)
	}
}

func dtEscape(s string) string { return escapeDrawtext(s) }

func buildCoverLine(baseName string, ci *coverInfo) string {
	displayName := baseName
	if runeLen := len([]rune(displayName)); runeLen > 28 {
		displayName = string([]rune(displayName)[:26]) + "…"
	}

	var parts []string
	head := displayName
	if b := wmBrand(); b != "" {
		head = b + "（" + displayName + "）"
	}
	if u := wmURL(); u != "" {
		head = head + u
	}
	parts = append(parts, head)
	if ci.Container != "" {
		parts = append(parts, strings.ToUpper(ci.Container))
	}
	if ci.Width > 0 && ci.Height > 0 {
		parts = append(parts, fmt.Sprintf("%dx%d", ci.Width, ci.Height))
	}
	vcodec := coverCodecName(ci.VideoCodec)
	if vcodec != "" {
		tag := vcodec
		if bd := coverBitDepth(ci.PixFmt); bd != "" {
			tag += " " + bd
		}
		if hdr := coverHDR(ci.ColorTRC); hdr != "" {
			tag += " " + hdr
		}
		parts = append(parts, tag)
	}
	if ci.FPS > 0 {
		fpsStr := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", ci.FPS), "0"), ".")
		parts = append(parts, fpsStr+"fps")
	}
	acodec := coverCodecName(ci.AudioCodec)
	if acodec != "" {
		atag := acodec
		if ch := coverChannels(ci.Channels); ch != "" {
			atag += " " + ch
		}
		if ci.SampleRate > 0 {
			atag += fmt.Sprintf(" %dKHz", ci.SampleRate/1000)
		}
		if ci.ABitrateK > 0 {
			atag += fmt.Sprintf(" %dKbps", ci.ABitrateK)
		}
		parts = append(parts, atag)
	}
	if ci.VBitrateK > 0 {
		parts = append(parts, fmt.Sprintf("%dKbps", ci.VBitrateK))
	}
	if ci.Duration > 0 {
		parts = append(parts, formatTimestamp(ci.Duration))
	}
	if fs := coverFileSize(ci.FileSize); fs != "" {
		parts = append(parts, fs)
	}
	return dtEscape(strings.Join(parts, " | "))
}

func (a *App) buildPreviewGrid(shotPaths []string, gridPath, baseName string, ci *coverInfo) {
	n := len(shotPaths)
	const gridCols = 5
	const gridRows = 3
	const thumbW = 384
	gridW := thumbW * gridCols

	line := buildCoverLine(baseName, ci)

	var parts []string

	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("[%d]scale=%d:-1[s%d]", i, thumbW, i))
	}
	var labels string
	for i := 0; i < n; i++ {
		labels += fmt.Sprintf("[s%d]", i)
	}
	var layoutParts []string
	for row := 0; row < gridRows; row++ {
		for col := 0; col < gridCols; col++ {
			x := "0"
			if col > 0 {
				xs := make([]string, col)
				for c := 0; c < col; c++ {
					xs[c] = fmt.Sprintf("w%d", c)
				}
				x = strings.Join(xs, "+")
			}
			y := "0"
			if row > 0 {
				ys := make([]string, row)
				for r := 0; r < row; r++ {
					ys[r] = fmt.Sprintf("h%d", r*gridCols)
				}
				y = strings.Join(ys, "+")
			}
			layoutParts = append(layoutParts, x+"_"+y)
		}
	}
	layout := strings.Join(layoutParts, "|")
	parts = append(parts, fmt.Sprintf("%sxstack=inputs=%d:layout=%s[grid]", labels, n, layout))

	headerH := 48
	parts = append(parts, fmt.Sprintf("[%d]scale=%d:%d[hdr]", n, gridW, headerH))
	parts = append(parts, fmt.Sprintf(
		"[hdr]drawtext=text='%s':fontcolor=white:fontsize=20:x=24:y=(h-text_h)/2:shadowcolor=black@0.5:shadowx=1:shadowy=1:expansion=none:fontfile='%s'[hdr1]",
		line, wmCJKFontFile()))

	parts = append(parts, "[hdr1][grid]vstack=inputs=2")

	filter := strings.Join(parts, ";")

	args := []string{"ffmpeg", "-y"}
	for _, p := range shotPaths {
		args = append(args, "-i", p)
	}
	args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x1a1a2e:s=%dx%d:d=1", gridW, headerH))
	args = append(args, "-filter_complex", filter, "-frames:v", "1", "-q:v", "2", gridPath)

	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		a.log("warn", "拼图预览生成失败: "+err.Error())
		return
	}
	wmWriteJPEGBranding(gridPath, baseName)
	a.log("ok", "拼图预览")
}

func (a *App) buildPureGrid(shotPaths []string, gridPath, baseName string) {
	n := len(shotPaths)
	const gridCols = 5
	const gridRows = 3
	const thumbW = 384

	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("[%d]scale=%d:-1[s%d]", i, thumbW, i))
	}
	var labels string
	for i := 0; i < n; i++ {
		labels += fmt.Sprintf("[s%d]", i)
	}
	var layoutParts []string
	for row := 0; row < gridRows; row++ {
		for col := 0; col < gridCols; col++ {
			x := "0"
			if col > 0 {
				xs := make([]string, col)
				for c := 0; c < col; c++ {
					xs[c] = fmt.Sprintf("w%d", c)
				}
				x = strings.Join(xs, "+")
			}
			y := "0"
			if row > 0 {
				ys := make([]string, row)
				for r := 0; r < row; r++ {
					ys[r] = fmt.Sprintf("h%d", r*gridCols)
				}
				y = strings.Join(ys, "+")
			}
			layoutParts = append(layoutParts, x+"_"+y)
		}
	}
	layout := strings.Join(layoutParts, "|")
	parts = append(parts, fmt.Sprintf("%sxstack=inputs=%d:layout=%s", labels, n, layout))

	filter := strings.Join(parts, ";")

	args := []string{"ffmpeg", "-y"}
	for _, p := range shotPaths {
		args = append(args, "-i", p)
	}
	args = append(args, "-filter_complex", filter, "-frames:v", "1", "-q:v", "2", gridPath)

	cmd := exec.Command(args[0], args[1:]...)
	hideWindow(cmd)
	if err := cmd.Run(); err != nil {
		a.log("warn", "精品封面生成失败: "+err.Error())
		return
	}
	wmWriteJPEGBranding(gridPath, baseName)
	a.log("ok", "精品封面")
}

func (a *App) processWatermarkOne(videoFile string, opts WatermarkOptions) bool {
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

	srcExt := strings.ToLower(filepath.Ext(videoFile))
	if srcExt == "" {
		srcExt = ".mp4"
	}
	outExt := srcExt
	if (outExt == ".mp4" || outExt == ".m4v") && hasMP4IncompatAudio(info) {
		outExt = ".mkv"
		a.log("warn", "容器 MKV: 音轨不兼容 MP4")
	}
	if outExt != ".mp4" && outExt != ".mkv" {
		outExt = ".mp4"
		if hasMP4IncompatAudio(info) {
			outExt = ".mkv"
		}
	}

	stem := strings.TrimSuffix(filepath.Base(videoFile), filepath.Ext(videoFile))
	cleaned := wmCleanFilename(stem)

	taken := func(name string) bool {
		_, err := os.Stat(filepath.Join(opts.OutputDir, name))
		return err == nil
	}
	if taken(cleaned) {
		for n := 2; n <= 999; n++ {
			candidate := fmt.Sprintf("%s(%d)", cleaned, n)
			if !taken(candidate) {
				cleaned = candidate
				break
			}
		}
	}
	subDir := filepath.Join(opts.OutputDir, cleaned)
	if err := os.MkdirAll(subDir, 0755); err != nil {
		a.log("error", "创建输出子目录失败: "+err.Error())
		return false
	}

	outputPath := filepath.Join(subDir, cleaned+outExt)
	a.log("info", "输出: "+filepath.Base(outputPath))

	var web *webSpec
	if opts.WebVersion {
		web = a.buildWebSpec(subDir, cleaned, info, opts.WebBitrateKbps, opts.WebCodec)
	}
	webPath := ""
	if web != nil {
		webPath = web.Path
	}
	cleanupOutputs := func() {
		os.Remove(outputPath)
		if webPath != "" {
			os.Remove(webPath)
		}
	}

	keepSource := false

	vf := a.buildWatermarkVF(info, videoFile)
	if info.BrokenTimestamps {
		a.log("warn", "时间戳异常，按恒定帧率封装")
	}

	type wmAttempt struct {
		useCuda bool
		web     *webSpec
		note    string
	}
	attempts := []wmAttempt{
		{useCuda: true, web: web},
		{useCuda: false, web: web, note: "CUDA 解码失败，改用软解"},
	}
	if web != nil {
		attempts = append(attempts, wmAttempt{useCuda: false, web: nil,
			note: "双路失败，只出母版"})
	}

	var (
		errMsg      string
		elapsed     float64
		fullCmdLine string
	)
	activeWeb := web
	lastAt := attempts[0]
	runAttempt := func(at wmAttempt) {
		cmd, tmpPath := a.buildWatermarkCmd(videoFile, outputPath, vf, cleaned, info, at.useCuda, at.web)
		if tmpPath != "" {
			defer os.Remove(tmpPath)
		}
		fullCmdLine = strings.Join(cmd.Args, " ")
		activeWeb = at.web

		start := time.Now()
		errMsg, err = a.runWithProgress(cmd, info.Duration)
		elapsed = time.Since(start).Seconds()
	}
	for i, at := range attempts {
		if i > 0 {
			if elapsed >= 3 || a.stopped.Load() {
				break
			}
			a.log("warn", at.note)
			cleanupOutputs()
		}
		lastAt = at
		runAttempt(at)
		if err == nil {
			break
		}
	}
	if err != nil && !a.stopped.Load() && isMuxTimestampError(errMsg) {
		if !info.BrokenTimestamps {
			info.BrokenTimestamps = true
			a.log("warn", "时间戳异常，改用恒定帧率重试")
			cleanupOutputs()
			runAttempt(lastAt)
		}
		if err != nil && !a.stopped.Load() && isMuxTimestampError(errMsg) &&
			(outExt == ".mp4" || outExt == ".m4v") {
			oldPath := outputPath
			outExt = ".mkv"
			outputPath = filepath.Join(subDir, cleaned+outExt)
			a.log("warn", "MP4 仍写入失败，改用 MKV")
			os.Remove(oldPath)
			cleanupOutputs()
			runAttempt(lastAt)
		}
	}
	web = activeWeb

	if err != nil {
		if a.stopped.Load() {
			cleanupOutputs()
			a.emitHistory(filepath.Base(videoFile), "水印/"+strings.ToUpper(strings.TrimPrefix(outExt, ".")), friendlyDuration(elapsed), false)
			return false
		}
		if ok2, reason := isAcceptableTailExit(outputPath, info.Duration, errMsg); ok2 {
			a.log("warn", "收尾非 0，输出完整: "+reason)
			if fixed, ok := a.repairUnfinalizedOutput(outputPath); ok {
				outputPath = fixed
				a.log("info", "重封装修复索引")
			} else {
				keepSource = true
				a.log("warn", "成品未收尾，重封装失败，保留源文件")
			}
		} else {
			logPath := strings.TrimSuffix(outputPath, filepath.Ext(outputPath)) + ".error.log"
			a.writeFFmpegLog(logPath, fullCmdLine, errMsg, err.Error(), elapsed, true)
			a.log("error", fmt.Sprintf("失败 %s\n%s", friendlyDuration(elapsed), friendlyErrorMsg(errMsg, "nvenc", true)))
			a.emitHistory(filepath.Base(videoFile), "水印/"+strings.ToUpper(strings.TrimPrefix(outExt, ".")), friendlyDuration(elapsed), false)
			cleanupOutputs()
			return false
		}
	}

	if web == nil && webPath != "" {
		os.Remove(webPath)
		webPath = ""
	}

	a.log("ok", "成功 "+friendlyDuration(elapsed))
	if webPath != "" {
		if st, serr := os.Stat(webPath); serr == nil {
			a.log("ok", fmt.Sprintf("在线版 %s · %s",
				filepath.Base(webPath), friendlyFileSize(st.Size())))
		}
	}

	if !a.stopped.Load() {
		a.log("info", "生成预览截图")
		a.extractPreviewShots(outputPath, subDir, cleaned)
	}

	a.emitHistory(filepath.Base(videoFile), "水印/"+strings.ToUpper(strings.TrimPrefix(outExt, ".")), friendlyDuration(elapsed), true)

	if keepSource {
		a.log("warn", "成品未收尾，保留源文件: "+filepath.Base(videoFile))
	} else if pathWithinDir(filepath.Dir(videoFile), opts.OutputDir) || sameDir(filepath.Dir(videoFile), subDir) {
		a.log("info", "源文件在输出目录内，保留: "+filepath.Base(videoFile))
	} else if st, serr := os.Stat(outputPath); serr != nil || st.Size() < 1<<20 {
		a.log("warn", "输出校验异常，保留源文件: "+filepath.Base(videoFile))
	} else if opts.WebVersion && !webOutputUsable(webPath, info.Duration) {
		a.log("warn", "在线版异常，保留源文件: "+filepath.Base(videoFile))
	} else {
		if err := os.Remove(videoFile); err != nil {
			a.log("warn", "删除源文件失败: "+err.Error())
		} else {
			a.log("info", "删除源文件 "+filepath.Base(videoFile))
		}
	}
	return true
}

func (a *App) ProcessWatermark(opts WatermarkOptions) bool {
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
	if opts.WebVersion && opts.WebCodec != "h264" && !a.checkEncoderAvailable("hevc_nvenc") {
		a.log("warn", "无 hevc_nvenc，在线版用 h264")
		opts.WebCodec = "h264"
	}
	if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
		a.log("error", "创建输出目录失败: "+err.Error())
		return false
	}

	videos := findFilesRecursive(opts.InputDir, videoExts)
	if kept, n := dropWebRenditions(videos); n > 0 {
		videos = kept
		a.log("warn", fmt.Sprintf("跳过 %d 个在线版", n))
	}
	if len(videos) == 0 {
		a.log("error", "未找到视频文件")
		return false
	}
	mode := "NVENC h264"
	if opts.WebVersion {
		mode = "h264 + 在线版"
	}
	a.log("info", fmt.Sprintf("共 %d 个 · %s", len(videos), mode))
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
		if a.processWatermarkOne(v, opts) {
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

func (a *App) checkNvencAvailable() bool { return a.checkEncoderAvailable("h264_nvenc") }

func (a *App) checkEncoderAvailable(encoder string) bool {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=black:s=320x240:d=0.1",
		"-c:v", encoder, "-f", "null", "-")
	hideWindow(cmd)
	return cmd.Run() == nil
}
