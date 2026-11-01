package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed subfonts/*.ttf subfonts/*.txt
var subFontFS embed.FS

const (
	bilingualCNFont = "Smiley Sans Oblique"
	bilingualENFont = "Montserrat SemiBold"
)

var (
	subFontOnce sync.Once
	subFontDir  string
)

func bundledSubFontDir() string {
	subFontOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		dir := filepath.Join(base, "Muxer", "subfonts")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return
		}
		entries, err := fs.ReadDir(subFontFS, "subfonts")
		if err != nil {
			return
		}
		for _, e := range entries {
			data, err := subFontFS.ReadFile("subfonts/" + e.Name())
			if err != nil {
				return
			}
			dst := filepath.Join(dir, e.Name())
			if st, err := os.Stat(dst); err == nil && st.Size() == int64(len(data)) {
				continue
			}
			tmp := dst + ".tmp"
			if err := os.WriteFile(tmp, data, 0644); err != nil {
				return
			}
			if err := os.Rename(tmp, dst); err != nil {
				os.Remove(tmp)
				return
			}
		}
		subFontDir = dir
	})
	return subFontDir
}

func bundledSubFontFiles() []string {
	dir := bundledSubFontDir()
	if dir == "" {
		return nil
	}
	entries, _ := fs.ReadDir(subFontFS, "subfonts")
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".ttf") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func assFontsDirOpt() string {
	dir := bundledSubFontDir()
	if dir == "" {
		return ""
	}
	return ":fontsdir='" + escapeFFmpegPath(dir) + "'"
}

func isBilingualCleanASS(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".muxer-clean.ass")
}
