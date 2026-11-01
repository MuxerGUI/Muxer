package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const extFixMinBytes = 10 * 1024 * 1024

const extFixDefaultExt = ".mkv"

var extFixSkipExts = map[string]bool{
	".nfo": true, ".txt": true, ".md": true, ".url": true, ".torrent": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true, ".webp": true,
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".sup": true,
	".vtt": true, ".ttml": true, ".idx": true, ".mks": true,
	".ttf": true, ".otf": true, ".ttc": true,
	".zip": true, ".rar": true, ".7z": true, ".exe": true, ".dll": true,
	".db": true, ".log": true, ".xml": true, ".html": true, ".json": true,
	".!qb": true, ".part": true, ".crdownload": true, ".partial": true,
	".downloading": true, ".tmp": true, ".aria2": true, ".bc!": true,
}

func isVideoExtFixCandidate(name string, size int64) bool {
	if size < extFixMinBytes || isMuxerTempArtifact(name) {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	if videoExts[ext] {
		return false
	}
	if ext != "" && extFixSkipExts[ext] {
		return false
	}
	return true
}

func videoExtFromProbeFormat(formatName string) string {
	f := strings.ToLower(formatName)
	switch {
	case strings.Contains(f, "matroska"):
		return ".mkv"
	case strings.Contains(f, "webm"):
		return ".webm"
	case strings.Contains(f, "mp4"), strings.Contains(f, "mov"),
		strings.Contains(f, "3gp"), strings.Contains(f, "3g2"), strings.Contains(f, "mj2"):
		return ".mp4"
	case strings.Contains(f, "mpegts"), strings.Contains(f, "m2ts"):
		return ".ts"
	case strings.Contains(f, "avi"):
		return ".avi"
	case strings.Contains(f, "asf"), strings.Contains(f, "wmv"):
		return ".wmv"
	case strings.Contains(f, "flv"):
		return ".flv"
	case strings.Contains(f, "mpeg"):
		return ".mpg"
	default:
		return ""
	}
}

type extFixDecision struct {
	ext      string
	fallback bool
	reason   string
}

func extFixDecide(probedExt string, hasVideo bool, probeErr error) extFixDecision {
	switch {
	case probeErr != nil:
		return extFixDecision{ext: extFixDefaultExt, fallback: true, reason: "ffprobe 探测失败（可能未下完）"}
	case !hasVideo:
		return extFixDecision{reason: "没有视频流（不是视频文件）"}
	case probedExt != "":
		return extFixDecision{ext: probedExt}
	default:
		return extFixDecision{ext: extFixDefaultExt, fallback: true, reason: "ffprobe 未识别容器"}
	}
}

func probeVideoContainer(path string) (ext string, hasVideo bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-print_format", "json", "-show_format", "-show_streams", path)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", false, err
	}
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecType   string `json:"codec_type"`
			CodecName   string `json:"codec_name"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return "", false, err
	}
	for _, s := range raw.Streams {
		if s.CodecType != "video" || s.Disposition.AttachedPic == 1 {
			continue
		}
		switch strings.ToLower(s.CodecName) {
		case "mjpeg", "png", "bmp":
			continue
		}
		return videoExtFromProbeFormat(raw.Format.FormatName), true, nil
	}
	return "", false, nil
}

func (a *App) ensureVideoExtensions(inputDir string) int {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		a.log("warn", "ffprobe 不可用，跳过无后缀视频探测")
		return 0
	}
	var candidates []string
	_ = filepath.WalkDir(inputDir, func(path string, d fs.DirEntry, err error) error {
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
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if !isVideoExtFixCandidate(d.Name(), info.Size()) {
			return nil
		}
		candidates = append(candidates, path)
		return nil
	})
	if len(candidates) == 0 {
		return 0
	}
	a.log("info", fmt.Sprintf("探测无后缀文件 %d 个", len(candidates)))
	fixed := 0
	for _, src := range candidates {
		if a.stopped.Load() {
			break
		}
		base := filepath.Base(src)
		probed, hasVideo, perr := probeVideoContainer(src)
		decision := extFixDecide(probed, hasVideo, perr)
		if decision.ext == "" {
			a.log("warn", fmt.Sprintf("跳过 %s · %s", base, decision.reason))
			continue
		}
		if decision.fallback {
			a.log("warn", fmt.Sprintf("%s，按 MKV 处理: %s", decision.reason, base))
		}
		dst := strings.TrimRight(src, ". ") + decision.ext
		if _, err := os.Stat(dst); err == nil {
			a.log("warn", "目标已存在，跳过: "+filepath.Base(dst))
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			a.log("error", "补后缀失败 "+base+": "+err.Error())
			continue
		}
		a.log("ok", fmt.Sprintf("补后缀 %s → %s", base, filepath.Base(dst)))
		fixed++
	}
	return fixed
}
