package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed embed/dynad_bumper.mp4
var dynAdBumperMP4 []byte

const dynAdSegmentFallback = 4.3

func dynAdPads(headAd, tailAd bool, seg float64) (headPad, tailPad float64) {
	if seg <= 0 {
		seg = dynAdSegmentFallback
	}
	if headAd {
		headPad = seg
	}
	if tailAd {
		tailPad = seg
	}
	return
}

func dynAdPadFilter(headPad, tailPad float64) string {
	var opts []string
	if headPad > 0 {
		opts = append(opts, fmt.Sprintf("start_duration=%.3f", headPad), "start_mode=add")
	}
	if tailPad > 0 {
		opts = append(opts, fmt.Sprintf("stop_duration=%.3f", tailPad), "stop_mode=add")
	}
	if len(opts) == 0 {
		return ""
	}
	opts = append(opts, "color=black")
	return "tpad=" + strings.Join(opts, ":")
}

func dynAdOverlayStage(prevLabel, outLabel string, bumperIdx int, headPad, tailPad, contentDur float64, outH int, hdrFix string) string {
	seg := headPad
	if tailPad > seg {
		seg = tailPad
	}
	if seg <= 0 {
		return ""
	}
	base := fmt.Sprintf("trim=0:%.3f,setpts=PTS-STARTPTS", seg)
	if outH > 0 {
		base += fmt.Sprintf(",scale=-2:%d", outH&^1)
	}
	if hdrFix != "" {
		base += "," + hdrFix
	}
	base += ",setsar=1,tpad=stop_mode=clone:stop_duration=1"
	ov := "x=(W-w)/2:y=(H-h)/2:eof_action=pass"
	var parts []string
	switch {
	case headPad > 0 && tailPad > 0:
		tailStart := headPad + contentDur
		parts = append(parts,
			fmt.Sprintf("[%d:v]split=2[db0][db1]", bumperIdx),
			fmt.Sprintf("[db0]%s[dbh]", base),
			fmt.Sprintf("[db1]%s,setpts=PTS-STARTPTS+%.3f/TB[dbt]", base, tailStart),
			fmt.Sprintf("%s[dbh]overlay=%s:enable='lt(t,%.3f)'[dbx]", prevLabel, ov, headPad),
			fmt.Sprintf("[dbx][dbt]overlay=%s:enable='gte(t,%.3f)'[%s]", ov, tailStart, outLabel),
		)
	case headPad > 0:
		parts = append(parts,
			fmt.Sprintf("[%d:v]%s[dbh]", bumperIdx, base),
			fmt.Sprintf("%s[dbh]overlay=%s:enable='lt(t,%.3f)'[%s]", prevLabel, ov, headPad, outLabel),
		)
	default:
		tailStart := headPad + contentDur
		parts = append(parts,
			fmt.Sprintf("[%d:v]%s,setpts=PTS-STARTPTS+%.3f/TB[dbt]", bumperIdx, base, tailStart),
			fmt.Sprintf("%s[dbt]overlay=%s:enable='gte(t,%.3f)'[%s]", prevLabel, ov, tailStart, outLabel),
		)
	}
	return strings.Join(parts, ";")
}

func dynAdBumperHDRFix(info *videoInfo, videoFile string) string {
	primaries, transfer, matrix := hdrMetaOf(info, videoFile)
	if transfer != "smpte2084" && transfer != "arib-std-b67" {
		return ""
	}
	if primaries == "" || primaries == "unknown" {
		primaries = "bt2020"
	}
	if matrix == "" || matrix == "unknown" {
		matrix = "bt2020nc"
	}
	return fmt.Sprintf("zscale=pin=bt709:tin=bt709:min=bt709:p=bt709:t=linear:npl=100,format=gbrpf32le,zscale=p=%s:t=%s:m=%s,format=yuv420p10le", primaries, transfer, matrix)
}

func dynAdAudioPadFilters(headPad, tailPad float64) []string {
	var f []string
	if headPad > 0 {
		f = append(f, fmt.Sprintf("adelay=%d:all=1", int(headPad*1000+0.5)))
	}
	if tailPad > 0 {
		f = append(f, fmt.Sprintf("apad=pad_dur=%.3f", tailPad))
	}
	return f
}

func dynAdMainAudioLabel(opts ProcessOptions) string {
	if opts.AudioTrackIndex >= 0 {
		return fmt.Sprintf("0:a:%d", opts.AudioTrackIndex)
	}
	return "0:a:0"
}

func dynAdBracket(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "[0:a:0]"
	}
	if strings.HasPrefix(label, "[") {
		return label
	}
	return "[" + label + "]"
}

func dynAdBumperAudioGraph(mainSrc string, bumperIdx int, mainChain string, headPad, tailPad, contentDur float64, splitWeb bool) (graph, masterOut, webOut string) {
	masterOut = "[aout]"
	webOut = "[aweb]"
	if mainChain == "" {
		mainChain = "anull"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s%s[amain];", dynAdBracket(mainSrc), mainChain)

	needHead := headPad > 0.001
	needTail := tailPad > 0.001
	seg := headPad
	if tailPad > seg {
		seg = tailPad
	}
	trim := fmt.Sprintf("atrim=0:%.3f,asetpts=PTS-STARTPTS", seg)
	bumperIn := fmt.Sprintf("[%d:a]", bumperIdx)

	switch {
	case needHead && needTail:
		delay := int((headPad+contentDur)*1000 + 0.5)
		fmt.Fprintf(&b, "%sasplit=2[s0][s1];", bumperIn)
		fmt.Fprintf(&b, "[s0]%s[ash];", trim)
		fmt.Fprintf(&b, "[s1]%s,adelay=%d:all=1[ast];", trim, delay)
		b.WriteString("[amain][ash][ast]amix=inputs=3:duration=first:dropout_transition=0:normalize=0")
	case needHead:
		fmt.Fprintf(&b, "%s%s[ash];", bumperIn, trim)
		b.WriteString("[amain][ash]amix=inputs=2:duration=first:dropout_transition=0:normalize=0")
	case needTail:
		delay := int((headPad+contentDur)*1000 + 0.5)
		fmt.Fprintf(&b, "%s%s,adelay=%d:all=1[ast];", bumperIn, trim, delay)
		b.WriteString("[amain][ast]amix=inputs=2:duration=first:dropout_transition=0:normalize=0")
	default:
		b.WriteString("[amain]anull")
	}

	if splitWeb {
		fmt.Fprintf(&b, "[amix];[amix]asplit=2%s%s", masterOut, webOut)
	} else {
		b.WriteString(masterOut)
	}
	return b.String(), masterOut, webOut
}

func dynAdPadOtherAudio(info *videoInfo, audioTrackIndex int, mainChain string) (graph string, maps []string) {
	if audioTrackIndex >= 0 || info == nil || len(info.AudioCodecs) < 2 {
		return "", nil
	}
	if mainChain == "" {
		mainChain = "anull"
	}
	var b strings.Builder
	for i := 1; i < len(info.AudioCodecs); i++ {
		label := fmt.Sprintf("[aextra%d]", i)
		fmt.Fprintf(&b, "[0:a:%d]%s%s;", i, mainChain, label)
		maps = append(maps, label)
	}
	return strings.TrimSuffix(b.String(), ";"), maps
}

func writeDynAdBumper() (string, float64, error) {
	if len(dynAdBumperMP4) == 0 {
		return "", 0, fmt.Errorf("未内置厂标片段")
	}
	p := filepath.Join(os.TempDir(), fmt.Sprintf("muxer-dynad-bumper-%d.mp4", time.Now().UnixNano()))
	if err := os.WriteFile(p, dynAdBumperMP4, 0644); err != nil {
		return "", 0, err
	}
	info, err := getVideoInfo(p)
	if err != nil || info == nil || info.Duration <= 0 {
		os.Remove(p)
		return "", 0, fmt.Errorf("无法读取厂标片段时长")
	}
	return p, info.Duration, nil
}

func dynAdBumperIndex(opts ProcessOptions, isBitmapSub, subEmbedded bool) int {
	idx := 1
	if isBitmapSub && !subEmbedded {
		idx++
	}
	if opts.DynAdChapterMeta != "" {
		idx++
	}
	return idx
}

func dynAdDescribe(headAd, tailAd bool) string {
	switch {
	case headAd && tailAd:
		return "片头+片尾"
	case headAd:
		return "片头"
	default:
		return "片尾"
	}
}
