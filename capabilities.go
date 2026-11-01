package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type CapabilityCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Fatal  bool   `json:"fatal"`
	Detail string `json:"detail"`
	Impact string `json:"impact"`
}

var (
	filterSetOnce sync.Once
	filterSet     map[string]bool
)

func ffmpegFilters() map[string]bool {
	filterSetOnce.Do(func() {
		filterSet = map[string]bool{}
		cmd := exec.Command("ffmpeg", "-hide_banner", "-filters")
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return
		}
		for _, line := range strings.Split(decodeGBK(out), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 3 || !strings.Contains(fields[2], "->") {
				continue
			}
			filterSet[fields[1]] = true
		}
	})
	return filterSet
}

func hasFilters(names ...string) (bool, []string) {
	set := ffmpegFilters()
	var missing []string
	for _, n := range names {
		if !set[n] {
			missing = append(missing, n)
		}
	}
	return len(missing) == 0, missing
}

var (
	optProbeMu    sync.Mutex
	optProbeCache = map[string]bool{}
)

func ffmpegAcceptsOpts(key string, extra ...string) bool {
	optProbeMu.Lock()
	if v, ok := optProbeCache[key]; ok {
		optProbeMu.Unlock()
		return v
	}
	optProbeMu.Unlock()

	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=black:s=64x64:d=0.04"}
	args = append(args, extra...)
	args = append(args, "-f", "null", "-")
	cmd := exec.Command("ffmpeg", args...)
	hideWindow(cmd)
	ok := cmd.Run() == nil

	optProbeMu.Lock()
	optProbeCache[key] = ok
	optProbeMu.Unlock()
	return ok
}

func supportsFilterScript() bool {
	f, err := os.CreateTemp("", "muxer-capcheck-*.txt")
	if err != nil {
		return false
	}
	path := f.Name()
	_, _ = f.WriteString("null")
	f.Close()
	defer os.Remove(path)
	return ffmpegAcceptsOpts("filter_script", "-filter_script:v", path)
}

func (a *App) CheckCapabilities() []CapabilityCheck {
	var out []CapabilityCheck
	add := func(name string, ok, fatal bool, detail, impact string) {
		out = append(out, CapabilityCheck{Name: name, OK: ok, Fatal: fatal, Detail: detail, Impact: impact})
	}
	miss := func(ok bool, missing []string) string {
		if ok {
			return "可用"
		}
		return "缺少滤镜：" + strings.Join(missing, "、")
	}

	if !a.CheckFFmpeg() {
		add("FFmpeg", false, true, "未找到或无法执行", "所有功能均不可用。请安装 FFmpeg 并加入 PATH")
		return out
	}
	add("FFmpeg", true, true, firstLine(a.GetFFmpegInfo()), "")

	probeCmd := exec.Command("ffprobe", "-version")
	hideWindow(probeCmd)
	probeOK := probeCmd.Run() == nil
	add("ffprobe", probeOK, true, boolText(probeOK, "可用", "未找到"),
		"缺失则无法读取视频信息，扫描/配对/时长判断全部失效")

	ok, m := hasFilters("subtitles", "ass")
	add("字幕烧录（libass）", ok, true, miss(ok, m),
		"缺失则硬压字幕完全不可用，只能软封")

	ok, m = hasFilters("drawtext")
	add("文字绘制（libfreetype）", ok, true, miss(ok, m),
		"缺失则屏上水印、片头片尾文字标注、封面信息栏均不可用")

	fontOK := !missingSystemFonts()
	add("系统字体", fontOK, true, boolText(fontOK, "已找到可用字体", "未找到任何可用字体"),
		"缺失则 drawtext 无法渲染，水印与标注会失败或显示为方块")

	scriptOK := supportsFilterScript()
	add("滤镜链写入文件（-filter_script）", scriptOK, true,
		boolText(scriptOK, "可用", "不被接受（FFmpeg 7.1 起已弃用，可能已被移除）"),
		"缺失则长视频的水印链（几百个 drawtext）会超出 Windows 命令行长度限制，水印任务不可用")

	encs := a.DetectEncoders()
	var availEnc []string
	for name, avail := range encs {
		if avail {
			availEnc = append(availEnc, name)
		}
	}
	add("可用编码器", len(availEnc) > 0, true,
		fmt.Sprintf("%d 个可用", len(availEnc)),
		"一个都没有则无法硬压。详见下方编码器列表")

	for _, enc := range []string{"hevc_nvenc", "av1_nvenc"} {
		if !encs[enc] {
			continue
		}
		uhq := nvencSupportsTune(enc, "uhq")
		add(enc+" 画质档位", uhq, false,
			boolText(uhq, "支持 -tune uhq（最高档）", "仅支持 -tune hq（需 FFmpeg 7.0+ 才有 uhq）"),
			"不支持时自动降级为 hq，同码率下画质略低，不影响能否压制")
	}

	ok, m = hasFilters("zscale")
	add("HDR 色调映射（libzimg）", ok, false, miss(ok, m),
		"缺失则 HDR 片源无法转 SDR。注意后果不是「跳过」而是成品颜色发灰发暗")

	ok, m = hasFilters("tpad", "adelay", "apad", "amix", "overlay", "trim", "split")
	add("厂标独立片段", ok, false, miss(ok, m),
		"缺失则无法把厂标片段接在正片前后")

	ok, m = hasFilters("noise", "eq", "boxblur", "atempo", "equalizer", "amix", "anoisesrc", "afade")
	add("去重破指纹链路", ok, false, miss(ok, m),
		"缺失则「去重（YouTube 原创化）」不可用")

	ok, m = hasFilters("concat", "anullsrc", "overlay", "split")
	add("多段合并 / 双路输出", ok, false, miss(ok, m),
		"缺失则多段视频合并、水印双路输出不可用")

	ok, _ = hasFilters("libvmaf")
	add("VMAF 质量评分", ok, false, boolText(ok, "可用", "当前 FFmpeg 未包含 libvmaf"),
		"缺失时仍可计算 PSNR / SSIM，只是没有 VMAF 分数")

	ok, m = hasFilters("cropdetect")
	add("自动裁黑边", ok, false, miss(ok, m), "缺失则无法自动检测黑边")

	ok, m = hasFilters("loudnorm")
	add("音量标准化", ok, false, miss(ok, m), "缺失则「音量标准化」选项无效")

	fsOK := ffmpegAcceptsOpts("faststart", "-c:v", "libx264", "-preset", "ultrafast",
		"-movflags", "+faststart", "-f", "mp4")
	add("MP4 faststart", fsOK, false, boolText(fsOK, "可用", "不被接受"),
		"缺失则 MP4 的 moov 索引留在文件尾，网页/服务器拖动进度会很难受")

	fmOK := ffmpegAcceptsOpts("fps_mode", "-fps_mode", "passthrough")
	add("时间戳直通（-fps_mode）", fmOK, false,
		boolText(fmOK, "可用", "不被接受（FFmpeg 5.0 起才有，旧版为 -vsync）"),
		"缺失则图形字幕（PGS/SUP）硬压可能出现字幕错位")

	return out
}

func boolText(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
