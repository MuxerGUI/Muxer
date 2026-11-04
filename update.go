package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultUpdateRepo = "MuxerGUI/Muxer"
	updateCacheTTL    = 10 * time.Minute
	updateTimeout     = 8 * time.Second
	updateTotalBudget = 12 * time.Second
	updateRetryBudget = 4 * time.Second
)

type UpdateInfo struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	HasUpdate   bool   `json:"hasUpdate"`
	PublishedAt string `json:"publishedAt"`
	URL         string `json:"url"`
	Repo        string `json:"repo"`
	Error       string `json:"error"`
	CheckedAt   string `json:"checkedAt"`
	Cached      bool   `json:"cached"`
}

type updateCacheFile struct {
	Repo        string `json:"repo"`
	Latest      string `json:"latest"`
	PublishedAt string `json:"publishedAt"`
	URL         string `json:"url"`
	CheckedAt   string `json:"checkedAt"`
}

var updateCheckMu sync.Mutex

func updateRepoID() string {
	if r := strings.TrimSpace(keys().UpdateRepo); r != "" {
		return strings.Trim(strings.TrimSpace(r), "/")
	}
	return defaultUpdateRepo
}

func releasePageURL(repo string) string {
	return "https://github.com/" + repo + "/releases/latest"
}

func updateCachePath() string {
	d := filepath.Dir(configDirKeysPath())
	if d == "" || d == "." {
		c, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		d = filepath.Join(c, "Muxer")
	}
	return filepath.Join(d, "update.json")
}

func loadUpdateCache() (updateCacheFile, bool) {
	var c updateCacheFile
	p := updateCachePath()
	if p == "" {
		return c, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c, false
	}
	if json.Unmarshal(data, &c) != nil {
		return c, false
	}
	return c, c.Latest != ""
}

func saveUpdateCache(c updateCacheFile) {
	p := updateCachePath()
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	os.WriteFile(p, data, 0644)
}

func updateCacheFresh(stamp string) bool {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return false
	}
	return time.Since(t) < updateCacheTTL
}

func (a *App) CheckUpdate(force bool) UpdateInfo {
	repo := updateRepoID()
	info := UpdateInfo{
		Current: AppVersion,
		Repo:    repo,
		URL:     releasePageURL(repo),
	}

	updateCheckMu.Lock()
	defer updateCheckMu.Unlock()

	cached, hasCache := loadUpdateCache()
	if hasCache && cached.Repo == repo {
		info.Latest = cached.Latest
		info.PublishedAt = cached.PublishedAt
		info.URL = cached.URL
		info.CheckedAt = cached.CheckedAt
		if !force && updateCacheFresh(cached.CheckedAt) {
			info.Cached = true
			info.HasUpdate = versionNewer(cached.Latest, AppVersion)
			return info
		}
	}

	latest, published, page, err := fetchLatestRelease(repo)
	if err != nil {
		info.Error = err.Error()
		if info.Latest != "" {
			info.HasUpdate = versionNewer(info.Latest, AppVersion)
		}
		return info
	}

	info.Latest = latest
	info.PublishedAt = published
	info.URL = page
	info.CheckedAt = time.Now().Format(time.RFC3339)
	info.HasUpdate = versionNewer(latest, AppVersion)
	saveUpdateCache(updateCacheFile{
		Repo:        repo,
		Latest:      latest,
		PublishedAt: published,
		URL:         page,
		CheckedAt:   info.CheckedAt,
	})
	return info
}

func newUpdateClient(followRedirect bool) *http.Client {
	c := &http.Client{
		Timeout: updateTimeout,
		Transport: &http.Transport{
			Proxy: updateProxyFunc(),
		},
	}
	if !followRedirect {
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return c
}

func fetchLatestRelease(repo string) (string, string, string, error) {
	start := time.Now()
	tag, published, page, err := fetchLatestViaAPI(repo)
	if err == nil {
		return tag, published, page, nil
	}
	left := updateTotalBudget - time.Since(start)
	if left < time.Second {
		return "", "", "", err
	}
	if left > updateRetryBudget {
		left = updateRetryBudget
	}
	tag, page, rerr := fetchLatestViaRedirect(repo, left)
	if rerr == nil {
		return tag, "", page, nil
	}
	return "", "", "", err
}

func fetchLatestViaAPI(repo string) (string, string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	endpoint := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("请求构造失败")
	}
	req.Header.Set("User-Agent", "Muxer/"+AppVersion)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := newUpdateClient(true)
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", updateNetworkError(err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 200:
	case 404:
		return "", "", "", fmt.Errorf("仓库不存在或没有 Release")
	case 403, 429:
		return "", "", "", fmt.Errorf("GitHub 接口限流")
	default:
		return "", "", "", fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}

	var doc struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", "", "", fmt.Errorf("返回内容无法解析")
	}

	tag := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(doc.TagName), "v"), "V")
	if tag == "" {
		return "", "", "", fmt.Errorf("返回内容缺少版本号")
	}

	page := releasePageURL(repo)
	if validGitHubPage(doc.HTMLURL) {
		page = doc.HTMLURL
	}
	return tag, formatReleaseDate(doc.PublishedAt), page, nil
}

func fetchLatestViaRedirect(repo string, budget time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	endpoint := releasePageURL(repo)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return "", "", fmt.Errorf("请求构造失败")
	}
	req.Header.Set("User-Agent", "Muxer/"+AppVersion)

	client := newUpdateClient(false)
	client.Timeout = budget
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if err != nil {
		return "", "", updateNetworkError(err)
	}
	resp.Body.Close()

	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if loc == "" {
		return "", "", fmt.Errorf("GitHub 未返回跳转")
	}
	page := loc
	if strings.HasPrefix(page, "/") {
		page = "https://github.com" + page
	}
	if !validGitHubPage(page) {
		return "", "", fmt.Errorf("返回内容无法解析")
	}
	idx := strings.LastIndex(page, "/tag/")
	if idx < 0 {
		return "", "", fmt.Errorf("返回内容缺少版本号")
	}
	tag := strings.Trim(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(page[idx+5:]), "v"), "V"), "/")
	if tag == "" {
		return "", "", fmt.Errorf("返回内容缺少版本号")
	}
	return tag, page, nil
}

func validGitHubPage(u string) bool {
	return strings.HasPrefix(u, "https://github.com/") && !strings.ContainsAny(u, " \t\r\n\"'<>")
}

func formatReleaseDate(raw string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return t.Local().Format("2006-01-02")
}

func updateNetworkError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return fmt.Errorf("连接 GitHub 超时")
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("检测已取消")
	}
	return fmt.Errorf("无法连接 GitHub")
}

func updateProxyFunc() func(*http.Request) (*url.URL, error) {
	if envSchemeProxySet() {
		return http.ProxyFromEnvironment
	}
	for _, raw := range []string{envAllProxy(), systemProxyURL()} {
		if u := parseProxyURL(raw); u != nil {
			return http.ProxyURL(u)
		}
	}
	return nil
}

func envSchemeProxySet() bool {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

func envAllProxy() string {
	for _, k := range []string{"ALL_PROXY", "all_proxy"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func parseProxyURL(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if idx := strings.Index(raw, "://"); idx >= 0 {
		scheme := strings.ToLower(raw[:idx])
		switch scheme {
		case "socks5h":
			raw = "socks5" + raw[idx:]
		case "http", "https", "socks5":
		default:
			return nil
		}
	} else {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

func versionNewer(latest, current string) bool {
	return compareVersion(latest, current) > 0
}

func compareVersion(a, b string) int {
	as, bs := versionSegments(a), versionSegments(b)
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		switch {
		case av < bv:
			return -1
		case av > bv:
			return 1
		}
	}
	return 0
}

func versionSegments(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	out := make([]int, 0, 4)
	for _, part := range strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '-' || r == '+' || r == '_' || r == '/'
	}) {
		digits := 0
		for digits < len(part) && part[digits] >= '0' && part[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			break
		}
		n, err := strconv.Atoi(part[:digits])
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
