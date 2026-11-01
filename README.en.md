# Muxer — Video Subtitle Muxing Toolkit

[简体中文](README.md) · [繁體中文](README.zh-TW.md) · **English**

**Muxer** is a Windows desktop app built on [Wails v2](https://wails.io/) for muxing external subtitles into video — either as a soft subtitle track or burned into the picture — bundled with a full set of video and subtitle utilities. The UI is in Chinese, and it is aimed at fansub groups and anyone organizing a local library who wants to batch-process files on their own machine.

Every bit of media work is handled by the **FFmpeg** already on your system. Your video never leaves the machine; only the optional speech-to-subtitle and AI subtitle features talk to whatever cloud API you configure yourself.

Project: <https://github.com/MuxerGUI/Muxer> · License: [MIT](LICENSE)

---

## Download

Grab builds from this repository's Releases page: <https://github.com/MuxerGUI/Muxer/releases>. If you would rather produce your own, follow [Build & Run](#build--run).

The copies on the cloud drives are older builds that trail the repo in both features and UI, kept only as an archive. The walkthrough, user guide, model notes, the FFmpeg build, and the WhisperX models still live there — any one of these four links will do:

| Drive | Link | Access code |
|-------|------|-------------|
| Baidu Pan | [Mirror 1](https://pan.baidu.com/s/1sCehvSHpIP06HcXzJ24flg?pwd=ddys) | `ddys` |
| Baidu Pan | [Mirror 2](https://pan.baidu.com/s/1UVV83F17sPLpwLRDhtUnYQ?pwd=ddys) | `ddys` |
| Quark Pan | [Mirror 3](https://pan.quark.cn/s/3e11a7385037?pwd=nbGp) | `nbGp` |
| Quark Pan | [Mirror 4](https://pan.quark.cn/s/7bc33fa63771) | none |

`MuxerModels` is the WhisperX model cache: the `large-v3` transcription model (about 3 GB, shared by every language) plus per-language alignment models. The full set is roughly **76 GB**, and there is no reason to take all of it.

- Take only the languages you actually work with. Alignment models are per language and most weigh 1.2–3.6 GB each
- Simplified and Traditional Chinese share the same `zh` model (about 2.4 GB), so there is no need to grab two. Chinese subtitles usually mix in English, so pulling `zh` and `en` together works best, and `en` is only about 0.35 GB — that puts `large-v3` + `zh` + `en` at roughly 5.6 GB
- If you only work in English, `en` on its own is enough; skip the Chinese model
- The app runs in offline mode and never fetches missing models on its own. Add a language with `scripts/download_align_models.py` whenever you need one: `python download_align_models.py E:\MuxerModels zh en ja`. Run it with no language arguments to pull everything
- Keep the `huggingface` and `torch` subdirectories intact, drop the folder anywhere, then mount it under Settings → model directory; the default path is `E:\MuxerModels`

The `模型说明.md` shipped with the drive bundle has the details.

---

## Contents

- [Download](#download)
- [Features](#features)
- [Requirements](#requirements)
- [Configuration: keys.json](#configuration-keysjson)
- [Build & Run](#build--run)
- [Workflow](#workflow)
- [Project Layout](#project-layout)
- [Architecture](#architecture)
- [Sound Cues](#sound-cues)
- [Caveats](#caveats)
- [License](#license)

---

## Features

### Muxing (core)

- **Soft mux (embedded subtitle track)** — the video stream is copied as-is and the subtitle is written as a separate track, so quality is untouched
- **Burn-in** — H.264 / H.265 / AV1 on CPU or GPU (NVENC / AMF / QSV). If the hardware encoder is unavailable, the job is retried once on CPU
- **Keyframe cap** — burn-in output gets a ~2 second keyframe interval based on the frame rate, which keeps seeking accurate and lets HLS segments line up
- **Web copy in the same pass** (burn-in only) — a single FFmpeg invocation outputs both the master and a lightweight 1080p MP4, sharing one decode and one subtitle burn. The web copy sits next to the master with `.在线版.mp4` appended to the name: stereo AAC, `+faststart`, ~2 second keyframes, and a bitrate that follows the source when left blank (capped at 3000 kbps). Soft muxing doesn't re-encode, so this option has no effect there
- **Automatic `+faststart` for MP4** — the moov index goes up front, so a direct HTTP link plays while downloading and seeks without stalling, and media servers skip the usual remux. MKV has no such mechanism; the container decides
- **Subtitle offset** — shift the whole subtitle timeline, for both soft mux and burn-in
- **Output containers** — MP4 / MKV
- **Presets** — soft mux recommended, 4K high quality, 1080p standard, fast turnaround, dedicated GPU efficiency (H.265 · NVENC), and integrated GPU (Intel QSV), one click each
- **Advanced options** — 10-bit, HDR passthrough, audio bitrate, track selection and delay, loudness normalization, custom FFmpeg arguments
- **Batch processing** — pairs videos with subtitles by filename, with multi-track subtitle support
- **AI filename analysis** — parses a batch of video and subtitle filenames, works out episode numbers and likely pairings, and suggests clean output names. Large files missing a real extension get one from ffprobe first (extension only, original name untouched); only files that actually contain a video stream are probed, and hidden files or unfinished downloads are skipped
- **Bilingual styling** — when a subtitle turns out to be Chinese/English bilingual, it's converted to a styled ASS with normal-size Chinese and smaller grey English, used for both burn-in and soft mux
- **Compression preview** — samples at 25% / 50% / 75%, trial-encodes each, and estimates the output size with side-by-side screenshots
- **Studio bumper (opening/closing ident)** — when enabled, the bundled bumper is appended as a **separate segment before or after the feature**, never covering the picture — the way cinemas and streaming services do it (also called an ident or bumper)
  - **Subtitle timing is unaffected** — subtitles are burned into the frames first, then black is padded and the bumper overlaid; padding only shifts everything later
  - **Chapters shift automatically** — source chapters move back by the bumper duration, so chapter jumps don't land on the black lead-in
  - **Duration read at runtime** — the clip is built into the binary and measured with ffprobe each run, so swapping the source needs no config change
  - **The clip keeps its own audio** — it plays from zero at the head and follows the feature at the tail, mixed over generated silence so the feature's volume is untouched. With "all tracks" selected, it's mixed into the main track only and the rest are just padded to length; a source with no audio gets black padding only
  - **Audio is re-encoded** — silence padding requires going through the filter graph. Lossless sources become FLAC in MKV (sample-exact, losing only Atmos/DTS:X object metadata); everything else becomes AAC
  - **Bitmap subtitles (PGS/SUP) work too**, as do multi-segment merges. Soft mux doesn't re-encode, so it falls back to a text label

### Watermarking

A burn-in watermark job that runs independently of the muxing pipeline (branding comes from the `watermark*` fields in `keys.json`). Each source video gets its own subfolder containing the burned output, a 5×3 cover sheet, and 15 preview stills.

- **Randomized placement** — two layers (rotating corners plus a brief centre flash) with per-instance jitter in size, opacity, and shadow direction, which defeats template matching and fixed cropping
- **Automatic HDR tone mapping** — PQ / HLG sources get an HDR→SDR pass inserted (this pipeline outputs 8-bit, and skipping it yields a washed-out, dim result)
- **Web copy alongside** (optional) — shares the spec and encoder settings with burn-in muxing, producing the master plus `<title>.在线版.mp4` in the same pass
- **Output bundling** — strips odd symbols from folder and file names, removes filler letters used to dodge moderation, deletes `.error.log` files and empty folders left by aborted runs, then groups everything by streamer into at most 10 archives inside one dated folder, ready to upload
  - **Dated folder** — `month + day + 3-digit count` (zero-padded), e.g. 194 outputs on 09/21 → `E:\output\0921194\`, containing `1_主播名\` … `10_主播名\`. The count reflects only non-empty output folders from this batch
  - **Incremental batches** — only outputs still sitting at the top of the output directory are bundled; folders from earlier dates are left alone, so you can bundle → upload → keep processing → bundle again without re-uploading. With nothing new, the run only tidies up
  - **Incidental cleanup** — stray logs at the top level, logs inside output folders, and temp artifacts left by interrupted runs are removed, except where the corresponding output no longer exists (those are kept with a warning)
  - **Filler letters** (Chinese anti-moderation padding, e.g. `你n的d女n友y已y上s线x` → `你的女友已上线`) — only isolated single letters wedged between Chinese characters are stripped; English words (`Yuki`, `Miss`) and letters adjacent to digits or underscores stay
  - **Empty folders** — leftover shells from aborted jobs (including ones holding only an empty `screenshots` folder or a log) are removed recursively and never counted. Hidden files keep their names

### Utilities

- **Media info** — resolution, codecs, duration, audio and subtitle track listing
- **Subtitle format conversion** — ASS ↔ SRT, single file or a whole folder at once
- **Subtitle quality check** — flags empty cues, durations that are too short, overlapping timings, and overlong lines
- **Bilingual merge** — aligns two subtitle files on the timeline and merges them, outputting SRT or ASS
- **Automatic translation** — DeepSeek / GLM built in, with optional DeepL, Baidu, and Tencent APIs; translates or produces a bilingual track in one click. Line lengths follow industry norms (42 characters for English, 35 for Chinese), and the English side is rewritten to read like natural spoken English rather than a literal rendering
- **Automatic sync** — uses ffsubsync to align subtitles with the audio
- **Speech-to-subtitle** — two engines:
  - **Qwen cloud ASR** (Alibaba DashScope) — audio is extracted locally and uploaded to OSS for the duration of the request, then deleted; returns word-level timestamps and segments sentences automatically
  - **WhisperX local ASR** (`large-v3` + GPU) — fully offline, with built-in VAD and multilingual wav2vec2 alignment models
  - Both engines feed a cleanup pipeline (collapse repetitions, strip hallucinated opening/closing lines, merge fragments, fit cue lengths to reading speed) before the built-in AI does semantic correction and translates foreign speech into Chinese, producing a **bilingual SRT** with Chinese on top and English below
  - With no AI key configured, the correction and translation steps are skipped but the cleaned-up subtitle is still written
  - Output filenames carry `.AI修正` and are already bilingual
- **AI subtitle repair** — corrects and de-ads existing subtitles, producing bilingual output. Behaviour depends on what it's given:
  - **Chinese only** → polished Simplified Chinese plus a fresh English translation (Traditional is converted along the way)
  - **English only** → proofread English plus a fresh Chinese translation
  - **Already bilingual** → Chinese gets polished, English gets conservative proofreading only (homophone errors, doubled particles, restarted half-sentences; wording, punctuation, capitalisation, and fillers are left alone)
  - **Japanese / Korean / Russian and other third languages** → translated into Chinese first, then English, so both sides stay aligned
  - Inline `<i>` / `{\i1}` style tags survive untouched; failed batches retry and split automatically, and lines fall back to the original rather than being dropped
- **Smart file pairing** — AI matches videos to subtitles by episode number and similarity, then renames them consistently
- **Clip trimming** — cut by start and end time, lossless or re-encoded
- **Multi-segment merge** — drag to reorder, merge losslessly or with transcoding
- **Thumbnail sheet** — evenly spaced frames rendered into one contact sheet
- **Chapter editing** — read, add, import, and write chapter markers
- **Audio extraction** — AAC / FLAC / MP3
- **Frame capture** — export a JPG at a given timecode
- **Metadata editing** — write title, date, and comment
- **VMAF quality scoring** — quantitative comparison via VMAF, PSNR, and SSIM
- **FFmpeg environment check** — reports version, GPU, and available encoders, plus a capability report that checks each filter and option the code actually relies on (libass, libfreetype, system fonts, `-filter_script`, libzimg, `tpad`, the dedup chain, libvmaf, faststart, `-fps_mode`, and the `-tune` presets on each NVENC encoder), noting what breaks if one is missing. The same check runs silently at startup and only speaks up on something fatal

### Subtitle editor

- Loads SRT / ASS and edits timing and text in a table
- Find and replace, insert and delete rows
- **Visual ASS style editor** — font, colour picker, outline, shadow, alignment, with live preview

### Task queue

- Jobs run one after another in a queue
- Optional start time (say, 2:00 AM)
- Optional shutdown or sleep when finished
- **Persistent queue** — the schedule is written to `queue.json` next to the executable and restored after a crash, power loss, or reboot, so an unattended long batch doesn't lose the whole plan
  - A job that was running when the app died is marked failed with an explanation (its output may be incomplete); confirm and hit reset to run it again
  - Start time and post-run action are not persisted and must be re-entered after a restart

### Settings

- Translation API key management (DeepSeek built in or entered in the UI; DeepL, Baidu, and Tencent optional)
- Qwen ASR credentials (DashScope + OSS) can be entered directly on the speech-to-subtitle page
- Watermark text, brand, and tagline can be entered directly on the watermark page
- WhisperX `python.exe` path, HTTP proxy, and **model directory** (browse to mount), with a one-click check
- ffsubsync install guidance and a one-click check
- Copy install commands with one click

### History

- Records each muxing job — filename, mode, duration, success or failure — up to 100 entries

---

## Requirements

- **Go** 1.23+
- **Node.js** (for `npm install` / `npm run build`)
- **FFmpeg** (including ffprobe) installed and on your **PATH**
- Currently configured for **Windows**

### Optional dependencies

| Feature | Dependency | Install / notes |
|---------|------------|-----------------|
| Automatic sync | ffsubsync | `pip install ffsubsync` |
| VMAF scoring | libvmaf | Requires an FFmpeg build with `--enable-libvmaf` |
| WhisperX local ASR | Python + CUDA + WhisperX | A separate conda environment (PyTorch CUDA, `whisperx`, `faster-whisper`); point the app at its `python.exe` in Settings |

> - Speech-to-subtitle (Qwen ASR) and AI subtitle processing (DeepSeek / GLM, selected by `aiProvider`) call cloud APIs and need keys for DeepSeek or GLM / DashScope (Qwen) / Alibaba OSS. All keys are read from `keys.json` next to the executable (see [Configuration](#configuration-keysjson) below); features prompt for input when something is missing.
> - DeepSeek's sampling temperature runs `0~2` while GLM's runs `0~1`. The app converts for you, so there's nothing to adjust. GLM's thinking mode is always on and can't be disabled.
> - WhisperX runs **locally** and needs your own GPU and conda environment. Model directory priority: **Settings page → `modelDir` in `keys.json` → `E:\MuxerModels` → the system cache**. You can mount a directory of pre-downloaded models (see the `模型说明.md` in the cloud-drive bundle).
> - Temporary audio extracted for any engine is written next to the **source video** and deleted when the job ends, so it never fills up the system temp drive.

---

## Configuration: keys.json

All third-party keys and optional branding live in `keys.json` next to the executable, templated by `keys.example.json`. Search order: same folder as `Muxer.exe` → current working directory → `%APPDATA%\Muxer\keys.json`. Saves go back to whichever file already exists, and a `keys.json.bak` is kept from the previous write.

| Field | Purpose |
|-------|---------|
| `aiProvider` | Active AI provider: `deepseek` or `zhipu` (GLM, also accepted as `glm`) |
| `deepseekKey` | DeepSeek API key (translation, AI repair, ASR post-correction, filename parsing) |
| `zhipuKey` | Zhipu GLM API key, used together with `aiProvider` |
| `dashscopeKey` | Alibaba DashScope (Qwen ASR) |
| `ossAccessKeyID` / `ossAccessKeySecret` | Alibaba OSS, used to stage audio for Qwen ASR |
| `ossBucket` / `ossEndpoint` | OSS bucket and regional endpoint; blank means `muxer-asr` on `oss-cn-shanghai.aliyuncs.com` |
| `modelDir` | Default WhisperX model directory (fallback when Settings is empty) |
| `brandName` / `brandUrl` / `brandTag` | Site branding for the **muxing and merge pipelines**: name, domain, output filename tag |
| `watermarkUrl` / `watermarkBrand` / `watermarkTagline` | URL, brand name, and tagline for the **standalone watermark job**; blank means no watermark and no brand metadata |

> **The two sets of branding are independent.** `brand*` drives muxing and merging (metadata, on-screen watermark, bumper, output filename tag, subtitle cleanup, and the AI's sense of identity). `watermark*` belongs only to the standalone job on the Watermark tab. They can point at entirely different sites.
>
> How `brand*` resolves:
> 1. Value provided → it's used.
> 2. Value missing but `keys.json` exists → the default brand `低端影视 / ddys.io / DDYS` applies.
> 3. No `keys.json` at all → nothing is branded: no metadata, no on-screen watermark, no bumper, and the neutral `.merged` suffix for output names.
>
> **Configured means hidden.** Anything already set in `keys.json` disappears from the UI and is used directly; the input only appears when the value is missing. This covers the DeepSeek key, the watermark details on the Watermark tab, and the Qwen ASR credentials. Priority is always **`keys.json` > what's typed in the UI**; DeepL, Baidu, and Tencent keys aren't part of `keys.json` and are always entered in the UI.
>
> **To hand the app to someone else, just delete `keys.json`.** The program ships without any keys, so they enter their own. Model directories and watermark branding go blank along with it.
>
> **Encoding is UTF-8.** If Notepad saves the file as ANSI, the app falls back to GBK-compatible reading and says so in the log. Malformed JSON (a missing comma, curly quotes) triggers a warning at startup and the app runs as if nothing were configured. Writes use a temp file plus atomic replace, and if the main file is corrupt but a `.bak` exists, the app restores from the backup.

### Distribution safety

`keys.json` is stored in **plain text** and holds your real DeepSeek / DashScope / OSS keys.

| How you distribute | Keys leaked? | Notes |
|---|---|---|
| `Muxer-amd64-installer.exe` | **No** | NSIS packages only `Muxer.exe` and the WebView2 bootstrapper |
| `Muxer.exe` on its own | **No** | The program itself contains no keys |
| **The whole `build\bin` folder** | **Yes** | `keys.json` is sitting right there and goes with it |

- To share, run **`打包分享版.bat`**. It rebuilds and produces a clean `build\share\` containing only `Muxer.exe`, the installer, and `keys.example.json`, then verifies no `keys.json` slipped in — aborting if one did.
- Keep a copy of `keys.json` somewhere private outside the project. It's the only one you have, and clearing `build` deletes it.
- This repository holds source and `keys.example.json` only. `build\bin`, `build\share`, and local configuration are not committed.

---

## Build & Run

### Prerequisites: install the Wails CLI

If `wails` isn't on your PATH, install it first:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

Make sure `%USERPROFILE%\go\bin` is on your PATH afterwards. From mainland China you may need a proxy:

```powershell
$env:HTTP_PROXY="http://127.0.0.1:7890"; $env:HTTPS_PROXY=$env:HTTP_PROXY; go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

The port depends on your proxy client — swap `7890` for whatever it actually listens on:

| Proxy client | Default port |
|------|------|
| Clash / Clash for Windows / Mihomo | `7890` |
| Clash Verge Rev | `7897` |
| v2rayN | HTTP `10809` (SOCKS `10808`) |
| sing-box / Hiddify | `2080` |
| Netch | `2801` |
| Shadowsocks-windows | SOCKS `1080` |

The client's main window or settings shows the port. With the Windows system proxy enabled you can also read it directly: `(Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings').ProxyServer`. Custom ports work the same way.

Go only reads the `HTTP_PROXY` / `HTTPS_PROXY` environment variables (and `NO_PROXY`). The Windows system proxy setting means nothing to it, so you have to set them explicitly, and they only last for the current PowerShell session.

### Building

The easiest route is to double-click **`编译.bat`** in the project root: it injects the build date, compiles `Muxer.exe`, and produces the NSIS installer as well. The equivalent manual commands:

```bash
# Development, with hot reload
wails dev

# Release build: GUI subsystem, build date injected, installer produced
wails build -ldflags "-H windowsgui -X main.BuildTime=2026-11-01" -nsis
```

**What the flags do:**
- `-ldflags "-H windowsgui"` builds a pure GUI app that doesn't allocate a console window
- `-X main.BuildTime=...` injects the build date shown on the About page
- `-nsis` additionally produces `build/bin/Muxer-amd64-installer.exe`

The binary lands at `build/bin/Muxer.exe`.

### Smoke test

`scripts\smoke.ps1` builds its own fixtures in the system temp directory (a 30-second test clip plus an SRT) and checks ffmpeg/ffprobe availability, `go vet`, burn-in rendering, timeline start, mixed-input merging, and that the share package contains no keys — then cleans up after itself. Failures print their cause and exit with code `1`, so it works well as a pre-release gate.

```powershell
powershell -ExecutionPolicy Bypass -File scripts\smoke.ps1
```

This needs `ffmpeg`, `ffprobe`, and `go` on your PATH.

---

## Workflow

1. **Muxing** — pick an input folder (videos and subtitles together), pick an output folder, choose a mode and settings, and start
2. **Utilities** — use each standalone tool from the Tools tab
3. **Subtitles** — edit subtitle content and ASS styling
4. **Queue** — line up several jobs and run them on a schedule
5. **Settings** — configure translation API keys, the WhisperX `python.exe` and model directory, and install or check external tools

---

## Project Layout

```
Muxer/
├── main.go              # Entry point: Wails window config
├── app.go               # Core: muxing/tools/subtitles/FFmpeg/queue/cloud ASR/translation/bilingual
├── appconfig.go         # keys.json loading (keys, model dir, watermark branding, read at runtime)
├── app_whisperx.go      # WhisperX local ASR: environment, model cache dir (configurable), audio extraction
├── app_dedup.go         # Video dedup + ASR-generated YouTube title/description
├── app_watermark.go     # Standalone watermark burn-in + cover/preview (brandable, can be left blank)
├── ai_chat.go           # Built-in AI chat and prompts
├── asr_*.go             # ASR segmentation/cleanup/normalization/post-processing and cloud engines
├── capabilities.go      # FFmpeg capability report (filter and option level)
├── subfix*.go           # Subtitle cleanup and repair
├── version.go           # Version number and About page data
├── 编译.bat             # One-click build: build date + exe + NSIS installer
├── 打包分享版.bat       # Produces a share package without keys.json (build\share)
├── keys.example.json    # keys.json template (keys, model dir, watermark, all blank)
├── embed/               # Bundled assets (studio bumper)
├── subfonts/            # Bundled subtitle fonts (with OFL license texts)
├── scripts/
│   ├── download_align_models.py  # Downloads wav2vec2 alignment models (dir configurable, default E:\MuxerModels)
│   └── smoke.ps1                 # Smoke test: environment/encoding/timeline/merge/share package
├── wails.json           # Wails project config (author, exe properties, installer company/product/version/copyright)
├── frontend/
│   ├── index.html       # Six tabs: mux, tools, subtitles, queue, settings, history
│   ├── src/
│   │   ├── main.js      # Frontend logic
│   │   └── style.css
│   ├── wailsjs/         # Go→JS bindings (generated)
│   └── dist/            # Vite build output
├── README.md            # 简体中文说明
├── README.zh-TW.md      # 繁體中文說明
├── README.en.md         # English documentation
└── LICENSE
```

---

## Architecture

| Layer | Technology |
|-------|------------|
| Desktop shell | **Wails v2** (Go with an embedded WebView) |
| Backend | **Go 1.23**, driving **FFmpeg / ffprobe** for all media work |
| Frontend | Plain **HTML + CSS + JavaScript**, built with **Vite 3** |
| Window | Fixed 800×720, light and dark themes, drag a folder to set the input directory |

---

## Sound Cues

The app can play a sound at key moments. **This is optional**: no audio ships with it. To enable it, create a `sounds/` folder next to `Muxer.exe` and drop in the MP3s you want — missing files are skipped silently and nothing else is affected.

| File | Plays when |
|------|------------|
| `extract_done.mp3` | Subtitle extraction finishes |
| `start_process.mp3` | Muxing starts |
| `mux_done.mp3` | Muxing finishes with at least one success |

---

## Caveats

- Soft-muxing ASS into MP4 converts it to mov_text and drops the styling. Use MKV
- Burn-in skips files whose names contain hardcoded subtitle markers (HC, HARDSUB)
- Multiple subtitle tracks call for MKV; MP4 support is limited
- Treat the FFmpeg environment check in Settings as the source of truth for encoder availability
- **The output filename tag is generated from the subtitles that actually made it in** (when the template is blank; anything typed in the template always wins):
  - One track with Chinese over English → `中英字幕`; separate Simplified and English tracks → `简英双字幕`; Simplified, Traditional, and English → `简繁英`; Simplified or Traditional alone → `简中` / `繁中`; English alone → `英字`; nothing identifiable → no tag
  - `中英字幕` (one track, both languages stacked) and `中英双字幕` (two switchable tracks) mean different things, and mixing them up produces an error message
  - Simplified vs Traditional is detected automatically; when it can't decide, it falls back to plain `中` rather than guessing
- The muxing pipeline **keeps only one Chinese text subtitle**. Non-Chinese tracks and surplus Chinese tracks are dropped, with a warning-level list in the log. If you supply both a Chinese and an English subtitle file, the English one won't reach the output
- **Failed or cancelled jobs delete their half-written output automatically**; the `.error.log` is kept separately for troubleshooting
- **Container index repair** — if the encoder dies during finalization it may write every frame but never the index (`Cues`/`Duration`), which makes players and cloud drives stall when seeking. The app automatically remuxes with `-c copy` to restore the index: **container only, no re-encode**, so burned-in subtitles stay pixel-identical, and MP4 output keeps `+faststart`. If the repair fails, the source is kept for inspection
- **The web copy (`.在线版.mp4`) must also report a readable duration** — master and web copy come from the same command, so an abnormal exit can leave the web copy without a usable `moov`. Such a file looks perfectly normal by size, which is why the web copy is additionally required to report a duration; if it can't, the source is kept
- Subtitle filenames may contain bracketed tags, commas, spaces, and the like — no manual renaming needed
- Embedded subtitle extraction **prefers text tracks** and skips PGS/VobSub bitmap tracks unless the file has nothing else
- WhisperX runs locally, so Settings needs a conda environment's `python.exe` and a **model directory**. The default is `E:\MuxerModels`, and you can mount a directory of pre-downloaded models instead (see the `模型说明.md` in the cloud-drive bundle)

---

## License

[MIT](LICENSE) © 2026 MuxerGUI

The bundled studio bumper, fonts, and third-party components remain under their own licenses. The bumper footage and brand marks (`brand*` / `watermark*`) belong to their respective owners; if you redistribute, replace the assets in `embed/` and `subfonts/`.

Muxer is developed and maintained by [DDYS](https://ddys.io).
