package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const wmPackTargetBins = 10

var wmPackNumPrefixRe = regexp.MustCompile(`^\d+_`)
var wmPackCopySuffixRe = regexp.MustCompile(`\(\d+\)$`)
var wmSepRunRe = regexp.MustCompile(`_{2,}`)

var wmPackContentSuffixes = []string{
	"诱惑特写一字马",
	"极品诱惑特写",
	"勾搭技师啪啪",
	"啪啪第二场",
	"啪啪第一场",
	"自慰第三场",
	"自慰第二场",
	"自慰第一场",
	"啪啪内射",
	"啪啪极品",
	"啪啪颜射",
	"啪啪3P",
	"诱惑跳舞",
	"车震啪啪",
	"野战啪啪",
	"女女自慰",
	"极品自慰",
	"诱惑特写",
	"诱惑",
	"啪啪",
	"自慰",
}

type wmPackCluster struct {
	key  string
	dirs []string
}

func wmPackIsBinName(name string) bool {
	if name == "" || name[0] == '0' {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func wmPackLooksLikeBinName(name string) bool {
	if wmPackIsBinName(name) {
		return true
	}
	i := strings.Index(name, "_")
	if i <= 0 {
		return false
	}
	num := name[:i]
	if num == "" || num[0] == '0' {
		return false
	}
	for _, r := range num {
		if r < '0' || r > '9' {
			return false
		}
	}
	rest := strings.TrimSpace(name[i+1:])
	if rest == "" {
		return false
	}
	for _, suf := range wmPackContentSuffixes {
		if strings.HasSuffix(rest, "_"+suf) {
			return false
		}
	}
	return true
}

func wmSanitizePackPart(s string) string {
	for _, r := range []string{`\`, `/`, `:`, `*`, `?`, `"`, `<`, `>`, `|`} {
		s = strings.ReplaceAll(s, r, "")
	}
	s = strings.Trim(s, " .")
	if s == "" {
		return "其它"
	}
	rs := []rune(s)
	if len(rs) > 40 {
		s = string(rs[:40])
	}
	return s
}

func wmDominantStreamer(dirNames []string) string {
	counts := map[string]int{}
	for _, n := range dirNames {
		counts[wmPackStreamerKey(n)]++
	}
	best, bestN := "", 0
	for k, c := range counts {
		if c > bestN || (c == bestN && (best == "" || k < best)) {
			best, bestN = k, c
		}
	}
	return wmSanitizePackPart(best)
}

func wmPackFolderName(n int, dirNames []string) string {
	return strconv.Itoa(n) + "_" + wmDominantStreamer(dirNames)
}

func wmDirHasTopLevelVideo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if videoExts[strings.ToLower(filepath.Ext(e.Name()))] {
			return true
		}
	}
	return false
}

func wmStampBatchName(now time.Time, count int) string {
	if count < 0 {
		count = 0
	}
	return now.Format("0102") + fmt.Sprintf("%03d", count)
}

func wmIsBatchDirName(name string) bool {
	if m := wmPackCopySuffixRe.FindStringIndex(name); m != nil {
		name = name[:m[0]]
	}
	if len(name) < 7 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	mm, _ := strconv.Atoi(name[0:2])
	dd, _ := strconv.Atoi(name[2:4])
	return mm >= 1 && mm <= 12 && dd >= 1 && dd <= 31
}

func wmUniqueDirName(outputDir, base string) (string, bool) {
	taken := func(name string) bool {
		_, err := os.Stat(filepath.Join(outputDir, name))
		return err == nil
	}
	if !taken(base) {
		return base, false
	}
	for n := 2; n <= 99; n++ {
		cand := fmt.Sprintf("%s(%d)", base, n)
		if !taken(cand) {
			return cand, true
		}
	}
	return base, false
}

func wmTreeIsEmpty(dir string) bool {
	empty := true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			empty = false
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		empty = false
		return fs.SkipAll
	})
	return empty
}

func wmPackIsExistingBin(full, name string) bool {
	if !wmPackLooksLikeBinName(name) {
		return false
	}
	return !wmDirHasTopLevelVideo(full)
}

func wmRemoveIfEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return false
	}
	return os.Remove(dir) == nil
}

func wmRemoveEmptyDirs(root string) int {
	removed := 0
	for _, d := range wmDirDepthFirst(root) {
		if wmRemoveIfEmpty(d) {
			removed++
		}
	}
	return removed
}

func wmDirDepthFirst(root string) []string {
	type deepDir struct {
		path  string
		depth int
	}
	var dirs []deepDir
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		dirs = append(dirs, deepDir{p, strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator))})
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].depth > dirs[j].depth })
	paths := make([]string, len(dirs))
	for i, d := range dirs {
		paths[i] = d.path
	}
	return paths
}

var wmLogFileExts = map[string]bool{".log": true}

func wmIsLogFileName(name string) bool {
	return wmLogFileExts[strings.ToLower(filepath.Ext(name))]
}

func wmIsLatinLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func wmStripFillerLetters(s string) string {
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); {
		if !wmIsLatinLetter(runes[i]) {
			out = append(out, runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && wmIsLatinLetter(runes[j]) {
			j++
		}
		if j-i > 1 {
			out = append(out, runes[i:j]...)
			i = j
			continue
		}
		prevCJK := len(out) > 0 && unicode.Is(unicode.Han, out[len(out)-1])
		nextCJK := j < len(runes) && unicode.Is(unicode.Han, runes[j])
		if !prevCJK && !nextCJK {
			out = append(out, runes[i])
		}
		i = j
	}
	return string(out)
}

func wmKeepNameRune(r rune) bool {
	switch r {
	case '_', '-', '.', '(', ')':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func wmCleanSpecialChars(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if wmKeepNameRune(r) {
			sb.WriteRune(r)
			continue
		}
		sb.WriteRune('_')
	}
	s := wmSepRunRe.ReplaceAllString(sb.String(), "_")
	s = wmStripFillerLetters(s)
	s = strings.Trim(s, " .-_")
	if s == "" {
		return name
	}
	return s
}

func wmCleanNameKeepExt(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		return name
	}
	clean := wmCleanSpecialChars(stem)
	if clean == stem {
		return name
	}
	return clean + ext
}

func wmUniqueRename(src, wantName string) (string, error) {
	if wantName == "" || wantName == filepath.Base(src) {
		return src, nil
	}
	dir := filepath.Dir(src)
	ext := filepath.Ext(wantName)
	stem := strings.TrimSuffix(wantName, ext)
	cand := wantName
	for n := 2; n <= 999; n++ {
		dst := filepath.Join(dir, cand)
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			if err := os.Rename(src, dst); err != nil {
				return src, err
			}
			return dst, nil
		}
		cand = fmt.Sprintf("%s(%d)%s", stem, n, ext)
	}
	return src, fmt.Errorf("同名文件过多，无法重命名")
}

type wmProductScan struct {
	name       string
	filesMoved int
	failed     int
	logs       int
	temps      int
	tempKept   int
	hasContent bool
}

func wmScanProduct(dirPath, name string) wmProductScan {
	type deepDir struct {
		path  string
		depth int
	}
	var files []string
	var dirs []deepDir
	logs := 0
	temps := 0
	tempKept := 0
	hasContent := false
	_ = filepath.WalkDir(dirPath, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			hasContent = true
			return nil
		}
		if d.IsDir() {
			if p != dirPath {
				dirs = append(dirs, deepDir{p, strings.Count(strings.TrimPrefix(p, dirPath), string(filepath.Separator))})
			}
			return nil
		}
		if wmIsLogFileName(d.Name()) {
			if os.Remove(p) == nil {
				logs++
			}
			return nil
		}
		if isMuxerTempArtifact(d.Name()) {
			if muxerTempIsSoleCopy(filepath.Dir(p), d.Name()) {
				tempKept++
				hasContent = true
				return nil
			}
			if os.Remove(p) == nil {
				temps++
			}
			return nil
		}
		hasContent = true
		if !strings.HasPrefix(d.Name(), ".") {
			files = append(files, p)
		}
		return nil
	})
	if !hasContent {
		return wmProductScan{name: name, logs: logs, temps: temps, tempKept: tempKept}
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].depth > dirs[j].depth })
	for _, d := range dirs {
		wmRemoveIfEmpty(d.path)
	}
	sc := wmProductScan{name: name, logs: logs, temps: temps, tempKept: tempKept, hasContent: true}
	for _, p := range files {
		newPath, rerr := wmUniqueRename(p, wmCleanNameKeepExt(filepath.Base(p)))
		if rerr != nil {
			sc.failed++
			continue
		}
		if newPath != p {
			sc.filesMoved++
		}
	}
	newDir, rerr := wmUniqueRename(dirPath, wmCleanSpecialChars(name))
	if rerr != nil {
		sc.failed++
		return sc
	}
	sc.name = filepath.Base(newDir)
	return sc
}

type wmBatchStats struct {
	logsRemoved  int
	tempsRemoved int
	tempsKept    int
	dirsRemoved  int
	dirsRenamed  int
	filesRenamed int
	cleanFailed  int
	existing     int
	legacyBins   int
}

func wmCollectBatch(outputDir string) ([]string, wmBatchStats) {
	var st wmBatchStats
	var products []string
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return nil, st
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name := e.Name()
		full := filepath.Join(outputDir, name)
		if wmIsBatchDirName(name) {
			if wmTreeIsEmpty(full) {
				if os.RemoveAll(full) == nil {
					st.dirsRemoved++
				}
				continue
			}
			st.existing++
			continue
		}
		if wmPackIsExistingBin(full, name) {
			st.legacyBins++
			continue
		}
		sc := wmScanProduct(full, name)
		st.logsRemoved += sc.logs
		st.tempsRemoved += sc.temps
		st.tempsKept += sc.tempKept
		st.filesRenamed += sc.filesMoved
		st.cleanFailed += sc.failed
		if !sc.hasContent {
			if os.RemoveAll(full) == nil {
				st.dirsRemoved++
			}
			continue
		}
		if sc.name != name {
			st.dirsRenamed++
		}
		products = append(products, sc.name)
	}
	sort.Strings(products)
	return products, st
}

func wmRemoveRootLogFiles(outputDir string) int {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !wmLogFileExts[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		if os.Remove(filepath.Join(outputDir, e.Name())) == nil {
			removed++
		}
	}
	return removed
}

func wmAllocPackFolders(outputDir string, start int, bins [][]string) []string {
	if len(bins) == 0 {
		return nil
	}
	n := start
	if n < 1 {
		n = 1
	}
	names := make([]string, 0, len(bins))
	used := map[string]bool{}
	for _, bin := range bins {
		ok := false
		for guard := 0; guard < 10000; guard++ {
			name := wmPackFolderName(n, bin)
			p := filepath.Join(outputDir, name)
			_, err := os.Stat(p)
			if err != nil && os.IsNotExist(err) && !used[name] {
				names = append(names, name)
				used[name] = true
				n++
				ok = true
				break
			}
			n++
		}
		if !ok {
			return nil
		}
	}
	return names
}

func wmPackStreamerKey(dirName string) string {
	s := strings.TrimSpace(dirName)
	s = wmPackNumPrefixRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(wmPackCopySuffixRe.ReplaceAllString(s, ""))
	for _, suf := range wmPackContentSuffixes {
		if strings.HasSuffix(s, "_"+suf) {
			s = strings.TrimSuffix(s, "_"+suf)
			break
		}
	}
	s = strings.Trim(s, " .-_\t")
	if s == "" {
		return dirName
	}
	return s
}

func wmPackBinCount(clusterCount int) int {
	if clusterCount <= 0 {
		return 0
	}
	if clusterCount > wmPackTargetBins {
		return wmPackTargetBins
	}
	return clusterCount
}

func wmGroupByStreamer(dirNames []string) []wmPackCluster {
	index := make(map[string]int)
	var clusters []wmPackCluster
	for _, name := range dirNames {
		key := wmPackStreamerKey(name)
		if i, ok := index[key]; ok {
			clusters[i].dirs = append(clusters[i].dirs, name)
			continue
		}
		index[key] = len(clusters)
		clusters = append(clusters, wmPackCluster{key: key, dirs: []string{name}})
	}
	for i := range clusters {
		sort.Strings(clusters[i].dirs)
	}
	sort.Slice(clusters, func(i, j int) bool {
		if len(clusters[i].dirs) != len(clusters[j].dirs) {
			return len(clusters[i].dirs) > len(clusters[j].dirs)
		}
		return clusters[i].key < clusters[j].key
	})
	return clusters
}

func wmAssignPacks(clusters []wmPackCluster, k int) [][]string {
	if k < 1 {
		return nil
	}
	if k > len(clusters) {
		k = len(clusters)
	}
	bins := make([][]string, k)
	sizes := make([]int, k)
	for _, c := range clusters {
		best := 0
		for i := 1; i < k; i++ {
			if sizes[i] < sizes[best] {
				best = i
			}
		}
		bins[best] = append(bins[best], c.dirs...)
		sizes[best] += len(c.dirs)
	}
	return bins
}

func wmClusterSummary(clusters []wmPackCluster, names []string) string {
	count := make(map[string]int, len(clusters))
	for _, c := range clusters {
		count[c.key] = len(c.dirs)
	}
	var parts []string
	seen := map[string]bool{}
	for _, name := range names {
		key := wmPackStreamerKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		n := count[key]
		if n > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", key, n))
		} else {
			parts = append(parts, key)
		}
		if len(parts) >= 8 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "、")
}

func (a *App) PackWatermarkOutput(outputDir string) bool {
	if !a.beginJob() {
		return false
	}
	defer a.endJob()

	outputDir = filepath.Clean(strings.TrimSpace(outputDir))
	if outputDir == "" || outputDir == "." {
		a.log("error", "未设置输出目录")
		return false
	}
	if abs, err := filepath.Abs(outputDir); err == nil {
		outputDir = abs
	}
	st, err := os.Stat(outputDir)
	if err != nil || !st.IsDir() {
		a.log("error", "输出目录不存在: "+outputDir)
		return false
	}

	if n := wmRemoveRootLogFiles(outputDir); n > 0 {
		a.log("info", fmt.Sprintf("清理根目录日志 %d 个", n))
	}

	products, stats := wmCollectBatch(outputDir)
	if stats.logsRemoved > 0 {
		a.log("info", fmt.Sprintf("清理日志 %d 个", stats.logsRemoved))
	}
	if stats.tempsRemoved > 0 {
		a.log("info", fmt.Sprintf("清理临时文件 %d 个", stats.tempsRemoved))
	}
	if stats.tempsKept > 0 {
		a.log("warn", fmt.Sprintf("临时文件 %d 个无对应成品，已保留", stats.tempsKept))
	}
	if stats.dirsRemoved > 0 {
		a.log("info", fmt.Sprintf("清理空目录 %d 个", stats.dirsRemoved))
	}
	if stats.dirsRenamed > 0 || stats.filesRenamed > 0 {
		a.log("info", fmt.Sprintf("清理特殊字符 目录 %d / 文件 %d", stats.dirsRenamed, stats.filesRenamed))
	}
	if stats.cleanFailed > 0 {
		a.log("warn", fmt.Sprintf("改名冲突 %d 个，保持原名", stats.cleanFailed))
	}
	if stats.existing > 0 {
		a.log("info", fmt.Sprintf("已有日期目录 %d 个，跳过", stats.existing))
	}
	if stats.legacyBins > 0 {
		a.log("warn", fmt.Sprintf("旧式包目录 %d 个，原样保留", stats.legacyBins))
	}
	if len(products) == 0 {
		a.log("warn", "无待汇总成品")
		return false
	}

	clusters := wmGroupByStreamer(products)
	k := wmPackBinCount(len(clusters))
	bins := wmAssignPacks(clusters, k)
	if len(bins) == 0 {
		a.log("error", "无成品目录可归类")
		return false
	}

	batchName, bumped := wmUniqueDirName(outputDir, wmStampBatchName(time.Now(), len(products)))
	batchDir := filepath.Join(outputDir, batchName)
	if err := os.MkdirAll(batchDir, 0755); err != nil {
		a.log("error", "创建日期目录 "+batchName+" 失败: "+err.Error())
		return false
	}
	if bumped {
		a.log("warn", "同名日期目录已存在，改用 "+batchName)
	}
	packNames := wmAllocPackFolders(batchDir, 1, bins)
	if len(packNames) != len(bins) {
		os.Remove(batchDir)
		a.log("error", "目录名冲突，无法分配")
		return false
	}

	a.log("info", fmt.Sprintf("汇总 %d 个成品 · %d 个主播 · %s\\ / %d 包",
		len(products), len(clusters), batchName, len(bins)))
	for i, bin := range bins {
		sum := wmClusterSummary(clusters, bin)
		if sum != "" {
			a.log("info", fmt.Sprintf("  %s · %d 个 · %s", packNames[i], len(bin), sum))
		} else {
			a.log("info", fmt.Sprintf("  %s · %d 个", packNames[i], len(bin)))
		}
	}

	moved := 0
	failed := 0
	total := len(products)
	done := 0
moving:
	for i, bin := range bins {
		if a.stopped.Load() {
			a.log("warn", "已中止")
			break
		}
		packName := packNames[i]
		packDir := filepath.Join(batchDir, packName)
		if err := os.MkdirAll(packDir, 0755); err != nil {
			a.log("error", "创建目录 "+packName+" 失败: "+err.Error())
			failed += len(bin)
			done += len(bin)
			continue
		}
		movedHere := 0
		for _, name := range bin {
			if a.stopped.Load() {
				a.log("warn", "已中止")
				if movedHere == 0 {
					wmRemoveIfEmpty(packDir)
				}
				break moving
			}
			src := filepath.Join(outputDir, name)
			dst := filepath.Join(packDir, name)
			if _, err := os.Stat(dst); err == nil {
				a.log("error", "目标已存在，跳过: "+packName+"\\"+name)
				failed++
				done++
				a.emit("progress", map[string]interface{}{"pct": done * 100 / total, "speed": ""})
				continue
			}
			if err := os.Rename(src, dst); err != nil {
				a.log("error", "移动失败 "+name+" → "+packName+": "+err.Error())
				failed++
			} else {
				moved++
				movedHere++
			}
			done++
			a.emit("progress", map[string]interface{}{"pct": done * 100 / total, "speed": ""})
		}
		if movedHere == 0 {
			wmRemoveIfEmpty(packDir)
		}
	}

	if a.stopped.Load() {
		wmRemoveEmptyDirs(batchDir)
		wmRemoveIfEmpty(batchDir)
		a.log("warn", fmt.Sprintf("中止 · 移动 %d / 失败 %d", moved, failed))
		return moved > 0
	}
	wmRemoveEmptyDirs(batchDir)
	if _, serr := os.Stat(batchDir); serr != nil {
		os.Remove(batchDir)
		a.log("error", fmt.Sprintf("无成品移入 %s · 失败 %d", batchName, failed))
		return false
	}
	if failed > 0 {
		a.log("warn", fmt.Sprintf("汇总 %s · 移动 %d / 失败 %d", batchName, moved, failed))
		return moved > 0
	}
	a.log("ok", fmt.Sprintf("汇总 %s\\ · %d 个成品 / %d 包", batchName, moved, len(packNames)))
	return true
}
