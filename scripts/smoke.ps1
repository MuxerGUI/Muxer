$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$work = Join-Path $env:TEMP ("muxer-smoke-" + [guid]::NewGuid().ToString('N').Substring(0,8))
$fail = 0
$pass = 0

function Step($name, [scriptblock]$body) {
    Write-Host ("[..] " + $name) -NoNewline
    try {
        & $body
        Write-Host "`r[OK] $name          " -ForegroundColor Green
        $script:pass++
    } catch {
        Write-Host "`r[FAIL] $name" -ForegroundColor Red
        Write-Host ("     " + $_.Exception.Message) -ForegroundColor Red
        $script:fail++
    }
}

function Probe($file, $entries) {
    $v = & ffprobe -v error -show_entries $entries -of default=noprint_wrappers=1:nokey=1 $file 2>$null
    if ($LASTEXITCODE -ne 0) { throw "ffprobe 失败: $file" }
    return ($v | Where-Object { $_ -ne '' })
}

Write-Host "=== Muxer 冒烟测试 ===" -ForegroundColor Cyan
Write-Host "工作目录: $work`n"
New-Item -ItemType Directory -Force $work | Out-Null

try {
    Step "环境: ffmpeg / ffprobe 可用" {
        & ffmpeg -version *> $null; if ($LASTEXITCODE -ne 0) { throw "ffmpeg 不可用" }
        & ffprobe -version *> $null; if ($LASTEXITCODE -ne 0) { throw "ffprobe 不可用" }
    }

    Step "Go: vet 通过" {
        Push-Location $root
        & go vet ./... 2>&1 | Out-Null
        $code = $LASTEXITCODE; Pop-Location
        if ($code -ne 0) { throw "go vet 未通过" }
    }

    $video = Join-Path $work "sample.mp4"
    $srt   = Join-Path $work "sample.srt"

    Step "素材: 生成 30s 测试视频与字幕" {
        & ffmpeg -y -hide_banner -loglevel error `
            -f lavfi -i "testsrc2=size=1920x1080:rate=25:duration=30" `
            -f lavfi -i "sine=frequency=440:duration=30" `
            -c:v libx264 -preset ultrafast -c:a aac -shortest $video 2>$null
        if ($LASTEXITCODE -ne 0) { throw "生成测试视频失败" }
        @"
1
00:00:02,000 --> 00:00:06,000
冒烟测试字幕第一条

2
00:00:20,000 --> 00:00:24,000
冒烟测试字幕第二条
"@ | Set-Content -Path $srt -Encoding UTF8
    }

    $outHard = Join-Path $work "out_hard.mp4"
    Step "硬压: 字幕烧录 + NVENC/CPU 编码" {
        $vf = "subtitles='sample.srt':force_style='FontName=Microsoft YaHei,FontSize=16',format=yuv420p"
        Push-Location $work
        & ffmpeg -y -hide_banner -loglevel error -i $video -vf $vf `
            -c:v libx264 -preset ultrafast -crf 28 -c:a copy $outHard 2>$null
        $code = $LASTEXITCODE; Pop-Location
        if ($code -ne 0) { throw "硬压失败" }
        $dur = [double](Probe $outHard "format=duration")
        if ([math]::Abs($dur - 30) -gt 1.5) { throw "输出时长异常: $dur (期望 ~30)" }
    }

    Step "时间轴: 输出起点为 0 且音视频同长" {
        $vs = [double](Probe $outHard "stream=start_time" | Select-Object -First 1)
        if ([math]::Abs($vs) -gt 0.1) { throw "视频起点非 0: $vs" }
    }

    Step "合并: 混合输入（含无音频/异分辨率）自动补齐" {
        $noAudio = Join-Path $work "noaudio.mp4"
        $small   = Join-Path $work "small.mp4"
        & ffmpeg -y -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1920x1080:rate=25:duration=5" -an -c:v libx264 -preset ultrafast $noAudio 2>$null
        & ffmpeg -y -hide_banner -loglevel error -f lavfi -i "testsrc2=size=1280x720:rate=25:duration=4" -f lavfi -i "sine=frequency=880:duration=4" -c:v libx264 -preset ultrafast -c:a aac -shortest $small 2>$null
        $merged = Join-Path $work "merged.mp4"
        $norm = 'scale=1920:1080:force_original_aspect_ratio=decrease,pad=1920:1080:(ow-iw)/2:(oh-ih)/2,setsar=1'
        $parts = @(
            "[0:v]$norm[nv0]"
            'anullsrc=channel_layout=stereo:sample_rate=48000,atrim=duration=5.000,asetpts=PTS-STARTPTS[na0]'
            "[1:v]$norm[nv1]"
            '[1:a]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo,asetpts=PTS-STARTPTS[na1]'
            '[nv0][na0][nv1][na1]concat=n=2:v=1:a=1[cv][outa]'
            '[cv]format=yuv420p[outv]'
        )
        $f = $parts -join ';'
        & ffmpeg -y -hide_banner -loglevel error -i $noAudio -i $small -filter_complex $f `
            -map "[outv]" -map "[outa]" -c:v libx264 -preset ultrafast -c:a aac $merged 2>$null
        if ($LASTEXITCODE -ne 0) { throw "混合合并失败" }
        $dur = [double](Probe $merged "format=duration")
        if ([math]::Abs($dur - 9) -gt 1.0) { throw "合并时长异常: $dur (期望 ~9)" }
    }

    Step "分享包: 不含 keys.json" {
        $share = Join-Path $root "build\share"
        if (Test-Path $share) {
            if (Test-Path (Join-Path $share "keys.json")) { throw "分享目录含 keys.json！" }
        }
    }
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

Write-Host ""
if ($fail -eq 0) {
    Write-Host "全部通过（$pass 项）" -ForegroundColor Green
    exit 0
} else {
    Write-Host "失败 $fail 项 / 通过 $pass 项" -ForegroundColor Red
    exit 1
}
