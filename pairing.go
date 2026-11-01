package main

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type filePair struct {
	video     string
	subtitles []string
}

func dedupBasenames(files []string, onSkip func(kept, dropped string)) []string {
	seen := make(map[string]string, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		key := strings.ToLower(filepath.Base(f))
		if first, ok := seen[key]; ok {
			if onSkip != nil {
				onSkip(first, f)
			}
			continue
		}
		seen[key] = f
		out = append(out, f)
	}
	return out
}

func groupByDir(files []string) map[string][]string {
	m := make(map[string][]string)
	for _, f := range files {
		m[filepath.Dir(f)] = append(m[filepath.Dir(f)], f)
	}
	return m
}

func pairVideosAndSubsByDir(videoFiles, subtitleFiles []string, multiSub bool, onUnmatched func(videoPath string)) []filePair {
	var pairs []filePair
	vByDir := groupByDir(videoFiles)
	sByDir := groupByDir(subtitleFiles)
	dirs := make([]string, 0, len(vByDir))
	for d := range vByDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		vlist := vByDir[d]
		slist := sByDir[d]
		if len(slist) == 0 {
			if onUnmatched != nil {
				for _, vf := range vlist {
					onUnmatched(vf)
				}
			}
			continue
		}
		var dirPairs []filePair
		if multiSub {
			dirPairs = matchFilePairsMulti(vlist, slist)
			if onUnmatched != nil {
				for _, vf := range vlist {
					found := false
					for _, p := range dirPairs {
						if p.video == vf {
							found = true
							break
						}
					}
					if !found {
						onUnmatched(vf)
					}
				}
			}
		} else {
			var unmatched []string
			dirPairs, unmatched = matchFilePairsReport(vlist, slist)
			if onUnmatched != nil {
				for _, vf := range unmatched {
					onUnmatched(vf)
				}
			}
		}
		pairs = append(pairs, dirPairs...)
	}
	return pairs
}

func matchFilePairs(videoFiles, subtitleFiles []string) []filePair {
	pairs, _ := matchFilePairsReport(videoFiles, subtitleFiles)
	return pairs
}

func matchFilePairsReport(videoFiles, subtitleFiles []string) (pairs []filePair, unmatched []string) {
	if len(videoFiles) == 0 {
		return nil, nil
	}
	if len(subtitleFiles) == 0 {
		return nil, videoFiles
	}
	if len(videoFiles) == 1 && len(subtitleFiles) == 1 {
		return []filePair{{videoFiles[0], []string{subtitleFiles[0]}}}, nil
	}
	subBases := make([]string, len(subtitleFiles))
	for i, s := range subtitleFiles {
		subBases[i] = nameForMatch(s)
	}
	used := make([]bool, len(subtitleFiles))
	for _, video := range videoFiles {
		vBase := nameForMatch(video)
		bestIdx, bestScore := -1, -1
		for i := range subtitleFiles {
			if used[i] || !canPairNames(vBase, subBases[i]) {
				continue
			}
			if score := commonPrefixLen(vBase, subBases[i]); score > bestScore {
				bestScore, bestIdx = score, i
			}
		}
		if bestIdx < 0 {
			unmatched = append(unmatched, video)
			continue
		}
		pairs = append(pairs, filePair{video, []string{subtitleFiles[bestIdx]}})
		used[bestIdx] = true
	}
	return pairs, unmatched
}

func findAllMatchingSubs(videoFile string, allSubs []string) []string {
	vBase := nameForMatch(videoFile)
	var matches []string
	for _, s := range allSubs {
		sBase := nameForMatch(s)
		if sBase == vBase || strings.HasPrefix(sBase, vBase+".") || strings.HasPrefix(sBase, vBase+"_") {
			matches = append(matches, s)
		}
	}
	return matches
}

func matchFilePairsMulti(videoFiles, subtitleFiles []string) []filePair {
	var pairs []filePair
	for _, video := range videoFiles {
		subs := findAllMatchingSubs(video, subtitleFiles)
		if len(subs) > 0 {
			pairs = append(pairs, filePair{video, subs})
		}
	}
	return pairs
}

func detectSubLang(filename string) (lang, title string) {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	for _, sep := range []string{".", "_", "-"} {
		for _, seg := range strings.Split(base, sep) {
			switch seg {
			case "sc", "chs", "zh", "chi", "zhs", "chn", "gb":
				return "chi", "简体中文"
			case "tc", "cht", "zht", "big5", "hk", "tw":
				return "zho", "繁體中文"
			case "en", "eng":
				return "eng", "English"
			case "jp", "jpn", "ja":
				return "jpn", "日本語"
			case "kr", "kor", "ko":
				return "kor", "한국어"
			}
		}
	}
	return "und", ""
}

func nameForMatch(path string) string {
	return strings.ToLower(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
}

func commonPrefixLen(a, b string) int {
	n := 0
	for {
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if sa == 0 || sb == 0 || ra != rb {
			return n
		}
		a, b = a[sa:], b[sb:]
		n++
	}
}

func explicitEpisode(name string) (int, bool) {
	upper := strings.ToUpper(name)
	for _, re := range []*regexp.Regexp{reSeasonEp, reEP, reE} {
		if m := re.FindStringSubmatch(upper); m != nil {
			n, err := strconv.Atoi(m[1])
			return n, err == nil
		}
	}
	if m := reCNEp.FindStringSubmatch(name); m != nil {
		n, err := strconv.Atoi(m[1])
		return n, err == nil
	}
	return 0, false
}

const minPairPrefixRatio = 0.5

func canPairNames(videoBase, subBase string) bool {
	if vEp, vOK := explicitEpisode(videoBase); vOK {
		if sEp, sOK := explicitEpisode(subBase); sOK {
			return vEp == sEp
		}
	}
	shorter := utf8.RuneCountInString(videoBase)
	if n := utf8.RuneCountInString(subBase); n < shorter {
		shorter = n
	}
	if shorter == 0 {
		return false
	}
	return float64(commonPrefixLen(videoBase, subBase))/float64(shorter) >= minPairPrefixRatio
}
