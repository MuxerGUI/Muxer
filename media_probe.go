package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var (
	probeCacheMu sync.Mutex
	probeCache   = map[string]any{}
)

const probeCacheLimit = 1024

func probeCacheKey(kind, path string) string {
	key := kind + "|" + strings.ToLower(filepath.Clean(path))
	if st, err := os.Stat(path); err == nil {
		key += fmt.Sprintf("|%d|%d", st.Size(), st.ModTime().UnixNano())
	}
	return key
}

func cachedProbe[T any](kind, path string, compute func() T) T {
	key := probeCacheKey(kind, path)

	probeCacheMu.Lock()
	if v, ok := probeCache[key]; ok {
		probeCacheMu.Unlock()
		return v.(T)
	}
	probeCacheMu.Unlock()

	val := compute()

	probeCacheMu.Lock()
	if len(probeCache) >= probeCacheLimit {
		probeCache = map[string]any{}
	}
	probeCache[key] = val
	probeCacheMu.Unlock()
	return val
}

type hdrMeta struct{ primaries, transfer, space string }

func hdrMetaOf(info *videoInfo, videoFile string) (primaries, transfer, space string) {
	if info != nil && info.ColorPrimaries != "" {
		return info.ColorPrimaries, info.ColorTransfer, info.ColorSpace
	}
	return detectHDRMeta(videoFile)
}

func detectHDRMeta(videoFile string) (primaries, transfer, space string) {
	m := cachedProbe("hdr", videoFile, func() hdrMeta {
		p, t, s := detectHDRMetaUncached(videoFile)
		return hdrMeta{p, t, s}
	})
	return m.primaries, m.transfer, m.space
}

func detectHDRMetaUncached(videoFile string) (primaries, transfer, space string) {
	cmd := exec.Command("ffprobe", "-v", "quiet",
		"-select_streams", "v:0",
		"-show_entries", "stream=color_primaries,color_transfer,color_space",
		"-of", "default=noprint_wrappers=1",
		videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(decodeGBK(out), "\n") {
		kv := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "color_primaries":
			primaries = kv[1]
		case "color_transfer":
			transfer = kv[1]
		case "color_space":
			space = kv[1]
		}
	}
	return
}

func detectDolbyVision(videoFile string) bool {
	return cachedProbe("dovi", videoFile, func() bool {
		return detectDolbyVisionUncached(videoFile)
	})
}

func detectDolbyVisionUncached(videoFile string) bool {
	cmd := exec.Command("ffprobe", "-v", "quiet",
		"-select_streams", "v:0",
		"-show_side_data",
		"-of", "json",
		videoFile)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), `"side_data_type":"DOVI"`) ||
		strings.Contains(string(out), "Dolby Vision")
}

type cropSample struct {
	w, h, x, y int
	ok         bool
}

func detectCrop(videoFile string, duration float64, srcW, srcH int) string {
	kind := fmt.Sprintf("crop|%dx%d", srcW, srcH)
	return cachedProbe(kind, videoFile, func() string {
		return detectCropUncached(videoFile, duration, srcW, srcH)
	})
}

func detectCropUncached(videoFile string, duration float64, srcW, srcH int) string {
	positions := []float64{0.10, 0.30, 0.50}
	if duration < 10 {
		positions = []float64{0.10}
	}

	samples := make([]cropSample, len(positions))
	var wg sync.WaitGroup
	for i, pos := range positions {
		wg.Add(1)
		go func(i int, pos float64) {
			defer wg.Done()
			samples[i] = cropDetectAt(videoFile, pos*duration)
		}(i, pos)
	}
	wg.Wait()

	best := cropSample{}
	for _, s := range samples {
		if s.ok && (!best.ok || s.w*s.h > best.w*best.h) {
			best = s
		}
	}
	if !best.ok {
		return ""
	}

	if srcW > 0 && srcH > 0 {
		origArea := srcW * srcH
		if float64(origArea-best.w*best.h)/float64(origArea) < 0.02 {
			return ""
		}
	}

	return fmt.Sprintf("crop=%d:%d:%d:%d", best.w, best.h, best.x, best.y)
}

func cropDetectAt(videoFile string, seekSec float64) cropSample {
	cmd := exec.Command("ffmpeg",
		"-ss", fmt.Sprintf("%.1f", seekSec),
		"-i", videoFile,
		"-vframes", "10",
		"-vf", "cropdetect=round=2",
		"-f", "null", "-")
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput()

	lastCrop := ""
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "crop="); i >= 0 {
			lastCrop = line[i+5:]
		}
	}
	if lastCrop == "" {
		return cropSample{}
	}
	parts := strings.SplitN(lastCrop, ":", 4)
	if len(parts) != 4 {
		return cropSample{}
	}
	var s cropSample
	for i, dst := range []*int{&s.w, &s.h, &s.x, &s.y} {
		if _, err := fmt.Sscanf(strings.TrimSpace(parts[i]), "%d", dst); err != nil {
			return cropSample{}
		}
	}
	s.ok = true
	return s
}
