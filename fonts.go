package main

import (
	"os"
	"strings"
	"sync"
	"unicode"
)

var (
	cjkFontCandidates = []string{
		`C:\Windows\Fonts\msyhbd.ttc`,
		`C:\Windows\Fonts\msyh.ttc`,
		`C:\Windows\Fonts\msyh.ttf`,
		`C:\Windows\Fonts\simhei.ttf`,
		`C:\Windows\Fonts\simsun.ttc`,
		`C:\Windows\Fonts\simkai.ttf`,
		`C:\Windows\Fonts\arialuni.ttf`,
	}
	latinFontCandidates = []string{
		`C:\Windows\Fonts\arialbd.ttf`,
		`C:\Windows\Fonts\arial.ttf`,
		`C:\Windows\Fonts\segoeuib.ttf`,
		`C:\Windows\Fonts\tahomabd.ttf`,
		`C:\Windows\Fonts\verdanab.ttf`,
	}

	fontOnce      sync.Once
	cachedCJKFont string
	cachedLatin   string
)

func firstExistingFont(candidates []string) string {
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return escapeFFmpegPath(p)
		}
	}
	return ""
}

func resolveFonts() {
	fontOnce.Do(func() {
		cachedCJKFont = firstExistingFont(cjkFontCandidates)
		cachedLatin = firstExistingFont(latinFontCandidates)
		if cachedCJKFont == "" {
			cachedCJKFont = cachedLatin
		}
		if cachedLatin == "" {
			cachedLatin = cachedCJKFont
		}
	})
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

func drawtextFontOpt(text string) string {
	resolveFonts()
	f := cachedLatin
	if hasCJK(text) {
		f = cachedCJKFont
	}
	if f == "" {
		return ":expansion=none"
	}
	return ":expansion=none:fontfile='" + f + "'"
}

func fontFileOrEmpty(cjk bool) string {
	resolveFonts()
	if cjk {
		return cachedCJKFont
	}
	return cachedLatin
}

func missingSystemFonts() bool {
	resolveFonts()
	return cachedCJKFont == "" && cachedLatin == ""
}

func scaledFontSize(base1080 int, outHeight int) int {
	if outHeight <= 0 {
		return base1080
	}
	size := base1080 * outHeight / 1080
	minSize := base1080 / 2
	maxSize := base1080 * 3
	if size < minSize {
		size = minSize
	}
	if size > maxSize {
		size = maxSize
	}
	return size
}

func outputHeightOf(scaleHeight int, info *videoInfo) int {
	if scaleHeight > 0 {
		return scaleHeight
	}
	if info != nil && info.Height > 0 {
		return info.Height
	}
	return 1080
}

func escapeDrawtext(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ":", "\\:")
	s = strings.ReplaceAll(s, "'", "\u2019")
	return s
}
