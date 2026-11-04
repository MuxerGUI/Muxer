package main

import (
	"os"
	"runtime"
	"strings"
	"time"
)

var (
	AppVersion = "1.6.65"
	BuildTime  = "2026年11月1日"
)

type AppInfo struct {
	Version     string `json:"version"`
	BuildTime   string `json:"buildTime"`
	GoVersion   string `json:"goVersion"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	ExePath     string `json:"exePath"`
	HasKeysFile bool   `json:"hasKeysFile"`
	BrandName   string `json:"brandName"`
	BrandURL    string `json:"brandUrl"`
}

func (a *App) GetAppInfo() AppInfo {
	keys()

	bt := strings.TrimSpace(BuildTime)
	if bt == "" {
		if exe, err := os.Executable(); err == nil {
			if st, serr := os.Stat(exe); serr == nil {
				bt = st.ModTime().Format("2006-01-02")
			}
		}
	}
	exePath, _ := os.Executable()

	return AppInfo{
		Version:     AppVersion,
		BuildTime:   formatBuildTime(bt),
		GoVersion:   runtime.Version(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		ExePath:     exePath,
		HasKeysFile: keysFilePresent,
		BrandName:   brandName(),
		BrandURL:    brandURL(),
	}
}

func formatBuildTime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	layouts := []string{
		"2006年1月2日",
		"2006年01月02日",
		"2006-01-02_15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t.Format("2006年1月2日")
		}
	}
	return raw
}

type DiskUsage struct {
	FreeBytes  uint64 `json:"freeBytes"`
	FreeText   string `json:"freeText"`
	Available  bool   `json:"available"`
	LowWarning bool   `json:"lowWarning"`
}

func (a *App) GetDiskUsage(dir string) DiskUsage {
	if dir == "" {
		return DiskUsage{}
	}
	free := freeDiskBytes(dir)
	if free == 0 {
		return DiskUsage{}
	}
	return DiskUsage{
		FreeBytes:  free,
		FreeText:   friendlyFileSize(int64(free)),
		Available:  true,
		LowWarning: free < 20*1024*1024*1024,
	}
}
