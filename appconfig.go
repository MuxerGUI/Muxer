package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

type appKeys struct {
	AIProvider         string `json:"aiProvider"`
	DeepSeekKey        string `json:"deepseekKey"`
	ZhipuKey           string `json:"zhipuKey"`
	DashScopeKey       string `json:"dashscopeKey"`
	OSSAccessKeyID     string `json:"ossAccessKeyID"`
	OSSAccessKeySecret string `json:"ossAccessKeySecret"`
	OSSBucket          string `json:"ossBucket"`
	OSSEndpoint        string `json:"ossEndpoint"`
	ModelDir           string `json:"modelDir"`
	BrandName          string `json:"brandName"`
	BrandURL           string `json:"brandUrl"`
	BrandTag           string `json:"brandTag"`
	WatermarkURL       string `json:"watermarkUrl"`
	WatermarkBrand     string `json:"watermarkBrand"`
	WatermarkTagline   string `json:"watermarkTagline"`
}

var (
	loadedKeys      appKeys
	loadKeysOnce    sync.Once
	keysLoadWarn    string
	keysFilePresent bool
	loadedKeysPath  string
	keysWriteMu     sync.Mutex
)

const (
	defaultBrandName = "低端影视"
	defaultBrandURL  = "ddys.io"
	defaultBrandTag  = "DDYS"
)

func brandName() string {
	if v := strings.TrimSpace(keys().BrandName); v != "" {
		return v
	}
	if keysFilePresent {
		return defaultBrandName
	}
	return ""
}

func brandURL() string {
	if v := strings.TrimSpace(keys().BrandURL); v != "" {
		return v
	}
	if keysFilePresent {
		return defaultBrandURL
	}
	return ""
}

func brandTag() string {
	if v := strings.TrimSpace(keys().BrandTag); v != "" {
		return v
	}
	if keysFilePresent {
		return defaultBrandTag
	}
	return ""
}

func brandMetadataArgs() []string {
	var args []string
	if n := brandName(); n != "" {
		args = append(args, "-metadata", "title="+n)
	}
	if u := brandURL(); u != "" {
		args = append(args, "-metadata", "comment="+u)
	}
	return args
}

func brandActor() string {
	n, u := brandName(), brandURL()
	if n == "" {
		n = defaultBrandName
	}
	if u == "" {
		u = defaultBrandURL
	}
	return n + "（" + u + "）"
}

func configDirKeysPath() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		return ""
	}
	return filepath.Join(d, "Muxer", "keys.json")
}

func keysFileCandidates() []string {
	var paths []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, x := range paths {
			if strings.EqualFold(x, p) {
				return
			}
		}
		paths = append(paths, p)
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "keys.json"))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(filepath.Join(cwd, "keys.json"))
	}
	add(configDirKeysPath())
	return paths
}

func decodeKeysData(data []byte) ([]byte, bool) {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	encodingFixed := false
	if !utf8.Valid(data) {
		if decoded := decodeTextFile(data); utf8.ValidString(decoded) && decoded != "" {
			data = []byte(decoded)
			encodingFixed = true
		}
	}
	return data, encodingFixed
}

func keys() appKeys {
	loadKeysOnce.Do(func() {
		var problems []string
		for _, p := range keysFileCandidates() {
			data, err := os.ReadFile(p)
			if err != nil {
				if !os.IsNotExist(err) {
					problems = append(problems, filepath.Base(p)+" 读取失败: "+err.Error())
				}
				continue
			}
			data, encodingFixed := decodeKeysData(data)
			if json.Unmarshal(data, &loadedKeys) == nil {
				keysFilePresent = true
				loadedKeysPath = p
				if encodingFixed {
					keysLoadWarn = "keys.json 不是 UTF-8 编码（已按 GBK 读取）"
				}
				return
			}
			if backup, berr := os.ReadFile(p + ".bak"); berr == nil {
				if bdata, _ := decodeKeysData(backup); json.Unmarshal(bdata, &loadedKeys) == nil {
					keysFilePresent = true
					loadedKeysPath = p
					keysLoadWarn = filepath.Base(p) + " 格式错误，已从 " + filepath.Base(p) + ".bak 恢复"
					return
				}
			}
			problems = append(problems, filepath.Base(p)+" 格式错误：不是合法 JSON（常见原因：缺少逗号/引号，或用了中文标点）")
		}
		if len(problems) > 0 {
			keysLoadWarn = strings.Join(problems, "；") + " —— 已按「未配置」运行"
		}
	})
	return loadedKeys
}

func writeKeysJSON(target string, raw map[string]interface{}) error {
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0644); err != nil {
		return err
	}
	if old, rerr := os.ReadFile(target); rerr == nil && len(old) > 0 {
		_ = os.WriteFile(target+".bak", old, 0644)
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (a *App) saveKeysJSON(target string, raw map[string]interface{}) (string, bool) {
	keysWriteMu.Lock()
	defer keysWriteMu.Unlock()
	if err := writeKeysJSON(target, raw); err == nil {
		return target, true
	} else {
		a.log("warn", "写入 "+filepath.Base(target)+" 失败: "+err.Error())
	}
	alt := configDirKeysPath()
	if alt != "" && !strings.EqualFold(alt, target) {
		if err := writeKeysJSON(alt, raw); err == nil {
			return alt, true
		} else {
			a.log("error", "写入 "+alt+" 失败: "+err.Error())
			return "", false
		}
	}
	return "", false
}

func keysRawFromStruct() map[string]interface{} {
	k := keys()
	return map[string]interface{}{
		"aiProvider":         aiProvider(),
		"deepseekKey":        k.DeepSeekKey,
		"zhipuKey":           k.ZhipuKey,
		"dashscopeKey":       k.DashScopeKey,
		"ossAccessKeyID":     k.OSSAccessKeyID,
		"ossAccessKeySecret": k.OSSAccessKeySecret,
		"ossBucket":          k.OSSBucket,
		"ossEndpoint":        k.OSSEndpoint,
		"modelDir":           k.ModelDir,
		"brandName":          k.BrandName,
		"brandUrl":           k.BrandURL,
		"brandTag":           k.BrandTag,
		"watermarkUrl":       k.WatermarkURL,
		"watermarkBrand":     k.WatermarkBrand,
		"watermarkTagline":   k.WatermarkTagline,
	}
}

func parseKeysFile(p string) (map[string]interface{}, bool) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	data, _ = decodeKeysData(data)
	var raw map[string]interface{}
	if json.Unmarshal(data, &raw) != nil {
		return nil, false
	}
	return raw, true
}

func readKeysRaw(paths []string) (map[string]interface{}, string) {
	if loadedKeysPath != "" {
		if raw, ok := parseKeysFile(loadedKeysPath); ok {
			return raw, loadedKeysPath
		}
	}
	for _, p := range paths {
		if raw, ok := parseKeysFile(p); ok {
			return raw, p
		}
		if bak, ok := parseKeysFile(p + ".bak"); ok {
			return bak, p
		}
	}
	if len(paths) > 0 {
		return keysRawFromStruct(), paths[0]
	}
	return keysRawFromStruct(), loadedKeysPath
}

type EffectiveBrand struct {
	BrandName      string `json:"brandName"`
	BrandURL       string `json:"brandUrl"`
	BrandTag       string `json:"brandTag"`
	WatermarkURL   string `json:"watermarkUrl"`
	WatermarkBrand string `json:"watermarkBrand"`
}

type BrandConfig struct {
	BrandName        string         `json:"brandName"`
	BrandURL         string         `json:"brandUrl"`
	BrandTag         string         `json:"brandTag"`
	WatermarkURL     string         `json:"watermarkUrl"`
	WatermarkBrand   string         `json:"watermarkBrand"`
	WatermarkTagline string         `json:"watermarkTagline"`
	Effective        EffectiveBrand `json:"effective"`
	KeysFilePath     string         `json:"keysFilePath"`
	HasKeysFile      bool           `json:"hasKeysFile"`
}

func (a *App) GetBrandConfig() BrandConfig {
	k := keys()
	var c BrandConfig
	c.BrandName, c.BrandURL, c.BrandTag = k.BrandName, k.BrandURL, k.BrandTag
	c.WatermarkURL, c.WatermarkBrand, c.WatermarkTagline = k.WatermarkURL, k.WatermarkBrand, k.WatermarkTagline
	c.Effective.BrandName, c.Effective.BrandURL, c.Effective.BrandTag = brandName(), brandURL(), brandTag()
	c.Effective.WatermarkURL, c.Effective.WatermarkBrand = wmURL(), wmBrand()
	c.HasKeysFile = keysFilePresent
	c.KeysFilePath = loadedKeysPath
	if c.KeysFilePath == "" {
		if paths := keysFileCandidates(); len(paths) > 0 {
			c.KeysFilePath = paths[0]
		}
	}
	return c
}

func (a *App) SaveBrandConfig(c BrandConfig) bool {
	paths := keysFileCandidates()
	if len(paths) == 0 {
		a.log("error", "无法确定 keys.json 路径")
		return false
	}
	raw, target := readKeysRaw(paths)

	brandName := strings.TrimSpace(c.BrandName)
	brandURL := strings.TrimSpace(c.BrandURL)
	brandTag := strings.TrimSpace(c.BrandTag)
	wmURL := strings.TrimSpace(c.WatermarkURL)
	wmBrand := strings.TrimSpace(c.WatermarkBrand)

	for key, val := range map[string]string{
		"brandName":        brandName,
		"brandUrl":         brandURL,
		"brandTag":         brandTag,
		"watermarkUrl":     wmURL,
		"watermarkBrand":   wmBrand,
		"watermarkTagline": c.WatermarkTagline,
	} {
		raw[key] = val
	}

	saved, ok := a.saveKeysJSON(target, raw)
	if !ok {
		return false
	}

	loadedKeys.BrandName, loadedKeys.BrandURL, loadedKeys.BrandTag = brandName, brandURL, brandTag
	loadedKeys.WatermarkURL, loadedKeys.WatermarkBrand, loadedKeys.WatermarkTagline = wmURL, wmBrand, c.WatermarkTagline
	keysFilePresent = true
	loadedKeysPath = saved

	a.log("ok", "品牌配置已保存 → "+saved)
	return true
}

type ConfigStatus struct {
	AIProvider     string `json:"aiProvider"`
	HasDeepSeekKey bool   `json:"hasDeepSeekKey"`
	HasZhipuKey    bool   `json:"hasZhipuKey"`
	HasWatermark   bool   `json:"hasWatermark"`
	HasQwenASR     bool   `json:"hasQwenASR"`
	KeysWarning    string `json:"keysWarning"`
}

func (a *App) GetConfigStatus() ConfigStatus {
	k := keys()
	return ConfigStatus{
		AIProvider:     aiProvider(),
		HasDeepSeekKey: strings.TrimSpace(k.DeepSeekKey) != "",
		HasZhipuKey:    strings.TrimSpace(k.ZhipuKey) != "",
		HasWatermark: strings.TrimSpace(k.WatermarkURL) != "" ||
			strings.TrimSpace(k.WatermarkBrand) != "" ||
			strings.TrimSpace(k.WatermarkTagline) != "",
		HasQwenASR: strings.TrimSpace(k.DashScopeKey) != "" &&
			strings.TrimSpace(k.OSSAccessKeyID) != "" &&
			strings.TrimSpace(k.OSSAccessKeySecret) != "",
		KeysWarning: keysLoadWarn,
	}
}

type AIConfig struct {
	AIProvider     string `json:"aiProvider"`
	HasDeepSeekKey bool   `json:"hasDeepSeekKey"`
	HasZhipuKey    bool   `json:"hasZhipuKey"`
	KeysFilePath   string `json:"keysFilePath"`
	HasKeysFile    bool   `json:"hasKeysFile"`
}

type AIConfigSave struct {
	AIProvider  string `json:"aiProvider"`
	DeepSeekKey string `json:"deepseekKey"`
	ZhipuKey    string `json:"zhipuKey"`
}

func (a *App) GetAIConfig() AIConfig {
	k := keys()
	c := AIConfig{
		AIProvider:     aiProvider(),
		HasDeepSeekKey: strings.TrimSpace(k.DeepSeekKey) != "",
		HasZhipuKey:    strings.TrimSpace(k.ZhipuKey) != "",
		HasKeysFile:    keysFilePresent,
	}
	c.KeysFilePath = loadedKeysPath
	if c.KeysFilePath == "" {
		if paths := keysFileCandidates(); len(paths) > 0 {
			c.KeysFilePath = paths[0]
		}
	}
	return c
}

func (a *App) SaveAIConfig(c AIConfigSave) bool {
	paths := keysFileCandidates()
	if len(paths) == 0 {
		a.log("error", "无法确定 keys.json 路径")
		return false
	}
	raw, target := readKeysRaw(paths)

	rawProvider := strings.TrimSpace(c.AIProvider)
	provider := normalizeProviderID(rawProvider)
	if provider == "" {
		if rawProvider != "" {
			a.log("error", "AI 提供商不支持: "+rawProvider)
			return false
		}
		provider = aiProvider()
	}
	raw["aiProvider"] = provider

	newDeepSeek := strings.TrimSpace(c.DeepSeekKey)
	newZhipu := strings.TrimSpace(c.ZhipuKey)
	if newDeepSeek != "" {
		raw["deepseekKey"] = newDeepSeek
	}
	if newZhipu != "" {
		raw["zhipuKey"] = newZhipu
	}

	strFromRaw := func(key string) string {
		if v, ok := raw[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	effectiveDeepSeek := strFromRaw("deepseekKey")
	if effectiveDeepSeek == "" {
		effectiveDeepSeek = strings.TrimSpace(loadedKeys.DeepSeekKey)
	}
	effectiveZhipu := strFromRaw("zhipuKey")
	if effectiveZhipu == "" {
		effectiveZhipu = strings.TrimSpace(loadedKeys.ZhipuKey)
	}
	if provider == "zhipu" && effectiveZhipu == "" {
		a.log("error", "GLM 未配置 API Key")
		return false
	}
	if provider == "deepseek" && effectiveDeepSeek == "" {
		a.log("error", "DeepSeek 未配置 API Key")
		return false
	}

	saved, ok := a.saveKeysJSON(target, raw)
	if !ok {
		return false
	}
	loadedKeys.AIProvider = provider
	if newDeepSeek != "" {
		loadedKeys.DeepSeekKey = newDeepSeek
	}
	if newZhipu != "" {
		loadedKeys.ZhipuKey = newZhipu
	}
	keysFilePresent = true
	loadedKeysPath = saved
	a.log("ok", "AI 配置 → "+saved+" · "+aiProviderLabel())
	return true
}

func normalizeProviderID(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "zhipu", "glm", "bigmodel":
		return "zhipu"
	case "deepseek":
		return "deepseek"
	}
	return ""
}

func aiProvider() string {
	if p := normalizeProviderID(keys().AIProvider); p != "" {
		return p
	}
	return "deepseek"
}

func aiProviderLabel() string {
	if aiProvider() == "zhipu" {
		return "GLM"
	}
	return "DeepSeek"
}

func chatProviderUsed() string {
	if v, ok := chatCallProvider.Load().(string); ok && v != "" {
		return v
	}
	return aiProviderLabel()
}

func zhipuKey() string {
	return strings.TrimSpace(keys().ZhipuKey)
}

func chatAPIKey() string {
	if aiProvider() == "zhipu" {
		return zhipuKey()
	}
	return deepseekKey()
}

func chatCompletionsURL() string {
	if aiProvider() == "zhipu" {
		return "https://open.bigmodel.cn/api/paas/v4/chat/completions"
	}
	return "https://api.deepseek.com/chat/completions"
}

func chatModelName(configured string) string {
	if aiProvider() == "zhipu" {
		return "glm-5.3-flash"
	}
	if strings.TrimSpace(configured) == "" {
		return "deepseek-flash"
	}
	return configured
}
