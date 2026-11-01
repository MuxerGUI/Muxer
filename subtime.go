package main

import (
	"fmt"
	"math"
	"strings"
)

func subTimeToMs(ts, format string) int {
	ts = strings.TrimSpace(ts)
	whole, frac := ts, ""
	if i := strings.IndexAny(ts, ".,"); i >= 0 {
		whole, frac = ts[:i], ts[i+1:]
	}
	var h, m, s int
	switch strings.Count(whole, ":") {
	case 2:
		fmt.Sscanf(whole, "%d:%d:%d", &h, &m, &s)
	case 1:
		fmt.Sscanf(whole, "%d:%d", &m, &s)
	default:
		fmt.Sscanf(whole, "%d", &s)
	}
	return (h*3600+m*60+s)*1000 + fracToMs(frac, subFracDigits(format))
}

func subFracDigits(format string) int {
	if format == "ass" || format == "ssa" {
		return 2
	}
	return 3
}

func fracToMs(frac string, digits int) int {
	val, n := 0, 0
	for i := 0; i < len(frac) && n < digits; i++ {
		c := frac[i]
		if c < '0' || c > '9' {
			break
		}
		val = val*10 + int(c-'0')
		n++
	}
	if n == 0 {
		return 0
	}
	for ; n < digits; n++ {
		val *= 10
	}
	if digits == 2 {
		return val * 10
	}
	return val
}

func hmsToMs(s string) int64 {
	return int64(subTimeToMs(s, "srt"))
}

func parseClockSec(ts string) float64 {
	return float64(subTimeToMs(ts, "srt")) / 1000
}

func parseTTMLTimeSec(ts string, fps float64) float64 {
	if fps <= 0 {
		fps = 23.976
	}
	ts = strings.TrimSpace(ts)
	if strings.HasSuffix(ts, "s") {
		var sec float64
		fmt.Sscanf(ts, "%f", &sec)
		return sec
	}
	if strings.HasSuffix(ts, "t") {
		var ticks float64
		fmt.Sscanf(ts, "%f", &ticks)
		return ticks / 10000000.0
	}
	if parts := strings.Split(strings.ReplaceAll(ts, ",", "."), ":"); len(parts) == 4 {
		var h, m, s, f float64
		fmt.Sscanf(parts[0], "%f", &h)
		fmt.Sscanf(parts[1], "%f", &m)
		fmt.Sscanf(parts[2], "%f", &s)
		fmt.Sscanf(parts[3], "%f", &f)
		return h*3600 + m*60 + s + f/fps
	}
	return parseClockSec(ts)
}

const subDefaultFPS = 23.976

func subFramesToMs(frames int, fps float64) int {
	if fps <= 0 {
		fps = subDefaultFPS
	}
	return int(math.Round(float64(frames) / fps * 1000))
}

func subMsToFrames(ms int, fps float64) int {
	if fps <= 0 {
		fps = subDefaultFPS
	}
	if ms < 0 {
		ms = 0
	}
	return int(math.Round(float64(ms) / 1000 * fps))
}

func msToSubTime(ms int, format string) string {
	if ms < 0 {
		ms = 0
	}
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	frac := ms % 1000
	switch format {
	case "ass", "ssa":
		return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, frac/10)
	case "vtt", "ttml":
		return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, frac)
	default:
		return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, frac)
	}
}

func secToSRTMs(sec float64) int { return int(math.Round(sec * 1000)) }

func secToASSMs(sec float64) int { return int(math.Round(sec*100)) * 10 }

func formatTTMLTime(sec float64) string { return msToSubTime(int(sec*1000), "ttml") }

func secsToHMS(sec float64) string {
	t := int(sec)
	return fmt.Sprintf("%02d:%02d:%02d", t/3600, (t%3600)/60, t%60)
}

func secondsToFileTime(sec float64) string {
	total := int(sec + 0.5)
	return fmt.Sprintf("%02d-%02d-%02d", total/3600, (total%3600)/60, total%60)
}

func toSRTTimestamp(ts, format string) string {
	switch format {
	case "ass", "ssa":
		return msToSubTime(subTimeToMs(ts, format), "srt")
	case "vtt", "ttml":
		return strings.Replace(ts, ".", ",", 1)
	default:
		return ts
	}
}

func shiftSRTTime(ts string, offsetSec float64) string {
	return msToSubTime(subTimeToMs(ts, "srt")+int(offsetSec*1000), "srt")
}

func shiftASSTime(ts string, offsetSec float64) string {
	cs := subTimeToMs(ts, "ass")/10 + int(offsetSec*100)
	if cs < 0 {
		cs = 0
	}
	return msToSubTime(cs*10, "ass")
}

func shiftSubtitleContent(content, ext string, offsetSec float64) string {
	if ext == ".srt" {
		return reSRTTimestamp.ReplaceAllStringFunc(content, func(ts string) string {
			return shiftSRTTime(ts, offsetSec)
		})
	}
	return reASSDialogueTS.ReplaceAllStringFunc(content, func(match string) string {
		parts := reASSDialogueTS.FindStringSubmatch(match)
		return parts[1] + shiftASSTime(parts[2], offsetSec) + parts[3] + shiftASSTime(parts[4], offsetSec)
	})
}
