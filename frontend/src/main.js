import './style.css'
import {
  SelectDirectory, SelectFile, SelectVideoFile, SelectSubtitleFile, SelectSaveFile,
  ProcessFiles, StopProcess, ExtractSubtitles,
  ScanDirectory, ConvertSubtitle, ExtractAudio,
  EditMetadata,
  DetectEncoders, DiagnoseEncoders, DetectGPU, GetFFmpegInfo, CheckCapabilities,
  ReadSubtitlePreview, ShiftSubtitleFile, TakeScreenshot,
  LoadSubtitle, SaveSubtitle,
  TrimVideo, GenerateThumbnailGrid, CheckSubtitleQuality, FixSubtitleWithAI, ConvertToCleanSRT, BatchConvertSubtitles,
  MergeSubtitles,
  CheckFFSubSync, AutoSyncSubtitle,
  ConcatVideos,
  ScanConcatDir,
  ReadChapters, WriteChapters, ReadTextFile,
  TranslateSubtitle,
  QwenASRTranscribe,
  WhisperXASRTranscribe,
  CheckWhisperX,
  PreviewCompress,
  GenerateSubtitlePreview,
  CheckVMAF, ScoreVMAF,
  AddToQueue, GetQueue, RemoveQueueTask, ClearQueue,
  StartQueue, StopQueue, ResetCancelledTasks,
  ProcessWatermark,
  PackWatermarkOutput,
  ProcessDedup,
  ReadASSStyles, WriteASSStyles,
  AnalyzeFilenames,
  ApplyFilenameRenames,
  ShutdownSystem,
  GetConfigStatus,
  GetAIConfig, SaveAIConfig,
  GetAppInfo, GetBrandConfig, SaveBrandConfig, GetDiskUsage,
  CheckUpdate,
} from '../wailsjs/go/main/App.js'
import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime.js'

function installInlineHandlerPolyfill() {
  const run = (el, attr, e) => {
    const code = el.getAttribute(attr)
    if (!code) return
    try {
      return new Function('event', code).call(el, e)
    } catch (err) {
      console.error(`[${attr}]`, code, err)
    }
  }
  document.addEventListener('click', (e) => {
    const el = e.target.closest('[onclick]')
    if (!el || el.disabled) return
    const ret = run(el, 'onclick', e)
    if (ret === false) e.preventDefault()
  }, true)
  document.addEventListener('change', (e) => {
    const el = e.target
    if (!el?.getAttribute) return
    if (el.getAttribute('onchange')) run(el, 'onchange', e)
  }, true)
}

function inlineHandlersWork() {
  try {
    if (!document.body) return false
    const b = document.createElement('button')
    b.setAttribute('onclick', 'window.__ihTest=(window.__ihTest||0)+1')
    b.style.display = 'none'
    document.body.appendChild(b)
    const before = window.__ihTest || 0
    b.click()
    const ok = (window.__ihTest || 0) > before
    b.remove()
    return ok
  } catch (_) {
    return false
  }
}
if (!inlineHandlersWork()) installInlineHandlerPolyfill()

function whenWailsReady(fn, tries = 0) {
  if (window.go?.main?.App && window.runtime) {
    fn()
    return
  }
  if (tries > 300) {
    console.warn('Wails bridge not ready, starting anyway')
    fn()
    return
  }
  setTimeout(() => whenWailsReady(fn, tries + 1), 20)
}

function saveDirs() {
  localStorage.setItem('muxer-dirs', JSON.stringify({
    inputDir:  document.getElementById('inputDir').value,
    outputDir: document.getElementById('outputDir').value,
  }))
}

const ENCODE_SELECTS  = ['encoderSelect', 'codecSelect', 'crfSelect', 'presetSelect', 'scaleSelect', 'audioBitrate', 'audioTrack', 'webCodec']
const ENCODE_CHECKS   = ['keepOriginalSubs', 'chkEmbedFonts', 'chkNormalize', 'chkTenBit', 'chkHDR', 'chkHDR2SDR', 'chkAutoCrop', 'chkStripDV', 'chkMultiSub', 'chkKeepSubStyle', 'chkWebVersion']
const ENCODE_RADIOS   = ['mode', 'format']

function saveEncodeOpts() {
  const data = { selects: {}, checks: {}, radios: {} }
  for (const id of ENCODE_SELECTS) {
    const el = document.getElementById(id)
    if (el) data.selects[id] = el.value
  }
  for (const id of ENCODE_CHECKS) {
    const el = document.getElementById(id)
    if (el) data.checks[id] = el.checked
  }
  for (const name of ENCODE_RADIOS) {
    const el = document.querySelector(`input[name="${name}"]:checked`)
    if (el) data.radios[name] = el.value
  }
  const preset = document.querySelector('.preset-btn.active')
  if (preset) data.preset = preset.getAttribute('onclick') || ''
  const webBitrate = document.getElementById('webBitrate')
  if (webBitrate) data.webBitrate = webBitrate.value
  try { localStorage.setItem('muxer-encodeOpts', JSON.stringify(data)) } catch (_) {}
}

function restoreEncodeOpts() {
  let data
  try { data = JSON.parse(localStorage.getItem('muxer-encodeOpts') || 'null') } catch (_) { return }
  if (!data) return
  for (const [id, v] of Object.entries(data.selects || {})) {
    const el = document.getElementById(id)
    if (el && [...el.options].some(o => o.value === v)) el.value = v
  }
  for (const [id, v] of Object.entries(data.checks || {})) {
    const el = document.getElementById(id)
    if (el) el.checked = !!v
  }
  for (const [name, v] of Object.entries(data.radios || {})) {
    const el = document.querySelector(`input[name="${name}"][value="${v}"]`)
    if (el) el.checked = true
  }
  if (data.preset) {
    document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'))
    const btn = [...document.querySelectorAll('.preset-btn')]
      .find(b => (b.getAttribute('onclick') || '') === data.preset)
    if (btn) btn.classList.add('active')
  }
  loadWebBitrateValue(document.getElementById('webBitrate'), data.webBitrate)
  try {
    window.onModeChange?.()
    window.onFormatChange?.()
    window.onCodecChange?.()
    window.onWebVersionChange?.()
  } catch (_) {}
}

function bindEncodeOptsPersist() {
  const ids = [...ENCODE_SELECTS, ...ENCODE_CHECKS]
  for (const id of ids) {
    document.getElementById(id)?.addEventListener('change', saveEncodeOpts)
  }
  for (const name of ENCODE_RADIOS) {
    document.querySelectorAll(`input[name="${name}"]`).forEach(el => el.addEventListener('change', saveEncodeOpts))
  }
  document.getElementById('webBitrate')?.addEventListener('change', () => {
    window.onWebVersionChange?.()
  })
  document.querySelectorAll('.preset-btn').forEach(b => b.addEventListener('click', () => setTimeout(saveEncodeOpts, 0)))
}

function bindWailsEvents() {
  EventsOn('log', e => { if (!_wmProcessing && !_dedupProcessing) appendLog(e.time, e.level, e.msg) })
  EventsOn('history', entry => { saveHistoryEntry(entry); renderHistory() })
  EventsOn('progress', data => {
    if (_wmProcessing || _dedupProcessing) return
    const bar  = document.getElementById('progressBar')
    const fill = document.getElementById('progressFill')
    if (!bar || !fill) return
    const pct   = typeof data === 'object' ? (data.pct || 0) : data
    const speed = typeof data === 'object' ? (data.speed || '') : ''
    fill.style.width = pct + '%'
    const txt = document.getElementById('progressText')
    if (txt) txt.textContent = speed ? `${pct}%  ·  ${speed}` : `${pct}%`
  })
  EventsOn('folder-dropped', path => {
    document.getElementById('inputDir').value = path
    saveDirs()
    appendLog(now(), 'info', '已设置输入目录: ' + path)
    autoScanDir(path)
  })
  EventsOn('queue:update', (tasks) => { renderQueue(tasks || []) })
  EventsOn('queue:running', (v) => { _queueRunning = !!v; syncQueueButtons() })
  EventsOn('log', e => {
    if (_wmProcessing) appendLogTo(document.getElementById('wmLogArea'), e.time, e.level, e.msg)
  })
  EventsOn('progress', data => {
    const fill = document.getElementById('wmProgressFill')
    if (fill && _wmProcessing) {
      const pct = typeof data === 'object' ? (data.pct || 0) : data
      fill.style.width = pct + '%'
    }
  })
  EventsOn('log', e => {
    if (_dedupProcessing) appendLogTo(document.getElementById('dedupLogArea'), e.time, e.level, e.msg)
  })
  EventsOn('progress', data => {
    const fill = document.getElementById('dedupProgressFill')
    if (fill && _dedupProcessing) {
      const pct = typeof data === 'object' ? (data.pct || 0) : data
      fill.style.width = pct + '%'
    }
  })
  ;(async () => {
    try {
      const q = await GetQueue()
      renderQueue(q || [])
    } catch (_) {}
  })()
}

function bindDragDrop() {
  const dropZone = document.getElementById('dropZone')
  if (!dropZone) return
  document.addEventListener('dragover',  e => { e.preventDefault(); dropZone.classList.add('drag-over') })
  document.addEventListener('dragleave', e => { if (!e.relatedTarget) dropZone.classList.remove('drag-over') })
  document.addEventListener('drop',      e => { e.preventDefault(); dropZone.classList.remove('drag-over') })
}

function startApp() {
  const theme = localStorage.getItem('muxer-theme') || ''
  document.documentElement.dataset.theme = theme
  const themeBtn = document.getElementById('themeToggle')
  if (themeBtn) themeBtn.textContent = theme === 'dark' ? '🌙' : '☀'

  try {
    const s = JSON.parse(localStorage.getItem('muxer-dirs') || '{}')
    if (s.inputDir)  document.getElementById('inputDir').value  = s.inputDir
    if (s.outputDir) document.getElementById('outputDir').value = s.outputDir
  } catch (_) {}
  try {
    const w = JSON.parse(localStorage.getItem('muxer-wm-dirs') || '{}')
    if (w.inputDir)  document.getElementById('wmInputDir').value  = w.inputDir
    if (w.outputDir) document.getElementById('wmOutputDir').value = w.outputDir
  } catch (_) {}
  restoreWmSettings()
  try {
    const q = JSON.parse(localStorage.getItem('muxer-qwen-keys') || '{}')
    if (q.dashKey)    document.getElementById('qwenDashKey').value     = q.dashKey
    if (q.ossId)      document.getElementById('qwenOssId').value       = q.ossId
    if (q.ossSecret)  document.getElementById('qwenOssSecret').value   = q.ossSecret
    if (q.ossBucket)  document.getElementById('qwenOssBucket').value   = q.ossBucket
    if (q.ossEndpoint)document.getElementById('qwenOssEndpoint').value = q.ossEndpoint
  } catch (_) {}
  try {
    const d = JSON.parse(localStorage.getItem('muxer-dedup-dirs') || '{}')
    if (d.inputDir)  document.getElementById('dedupInputDir').value  = d.inputDir
    if (d.outputDir) document.getElementById('dedupOutputDir').value = d.outputDir
  } catch (_) {}

  const metaYear = document.getElementById('metaYear')
  if (metaYear) metaYear.placeholder = '如 ' + new Date().getFullYear()

  try {
    const cDynOn = localStorage.getItem('muxer-concatDynAdOn')
    if (cDynOn !== null) {
      const cDyn = document.getElementById('concatDynAd')
      if (cDyn) { cDyn.checked = cDynOn === '1'; window.onDynAdChange(cDyn) }
    }
    localStorage.removeItem('muxer-trans-credit')
    const kss = localStorage.getItem('muxer-keepSubStyle')
    if (kss !== null) {
      const el = document.getElementById('chkKeepSubStyle')
      if (el) el.checked = kss === '1'
    }
    const aac = localStorage.getItem('muxer-asrAiCorrect')
    if (aac !== null) {
      const el = document.getElementById('chkAsrAiCorrect')
      if (el) el.checked = aac === '1'
    }
  } catch (_) {}

  renderHistory()
  bindWailsEvents()
  bindDragDrop()

  const savedInputDir = document.getElementById('inputDir')?.value.trim() || ''
  const scanPromise = savedInputDir ? autoScanDir(savedInputDir) : Promise.resolve()

  Promise.all([scanPromise, initDetectEncoders()]).finally(() => {
    try {
      if (localStorage.getItem('muxer-encodeOpts')) {
        restoreEncodeOpts()
      } else {
        let preset = '1080p'
        if (_encoderAvail['h264_nvenc'])  preset = 'nvenc_h265'
        else if (_encoderAvail['h264_amf'])  preset = 'fast'
        else if (_encoderAvail['h264_qsv'])  preset = 'fast_qsv_original'
        window.applyPreset?.(preset, true)
      }
    } catch (_) {}
    bindEncodeOptsPersist()
  })
  loadTransApiKeys()
  loadWhisperXSettings()
  applyConfigStatus()
  renderCustomPresets()
  loadBrandConfig()
  loadAppInfo()
  refreshDiskUsage()
}

let _cfgHasDeepSeekKey = false
let _cfgHasZhipuKey = false
let _cfgAIProvider = 'deepseek'
let _cfgHasQwenASR = false
async function applyConfigStatus() {
  let status = { hasDeepSeekKey: false, hasZhipuKey: false, hasWatermark: false, hasQwenASR: false, aiProvider: 'deepseek' }
  try {
    status = await GetConfigStatus()
  } catch (_) {}
  _cfgHasDeepSeekKey = !!status.hasDeepSeekKey
  _cfgHasZhipuKey = !!status.hasZhipuKey
  _cfgAIProvider = (status.aiProvider === 'zhipu') ? 'zhipu' : 'deepseek'
  _cfgHasQwenASR = !!status.hasQwenASR
  if (status.keysWarning) {
    appendLog(now(), 'warn', 'keys.json: ' + status.keysWarning)
    toast('keys.json 配置有问题', 'warn')
  }
  if (typeof window.onTransProviderChange === 'function') window.onTransProviderChange()
  if (typeof window.loadAIConfigUI === 'function') window.loadAIConfigUI()
  if (typeof window.toggleASREngine === 'function') window.toggleASREngine()
}


window.onDynAdChange = function (el) {
  const input = document.getElementById('concatAdText')
  if (input) input.style.display = el.checked ? 'none' : ''
  localStorage.setItem('muxer-concatDynAdOn', el.checked ? '1' : '0')
}


window.toggleTheme = function () {
  const isDark = document.documentElement.dataset.theme === 'dark'
  const next = isDark ? '' : 'dark'
  document.documentElement.dataset.theme = next
  document.getElementById('themeToggle').textContent = next === 'dark' ? '🌙' : '☀'
  localStorage.setItem('muxer-theme', next)
}


let _encoderAvail = {}
let _encoderDiag  = {}
let _lastScanResult = null

function bestGpuEncoder() {
  if (_encoderAvail['h264_nvenc']) return 'nvenc'
  if (_encoderAvail['h264_amf'])   return 'amf'
  if (_encoderAvail['h264_qsv'])   return 'qsv'
  return 'cpu'
}

function gpuEncUnavailMsg(encName) {
  const val = _encoderDiag[encName]
  if (!val) return '编码器不可用'
  const code = parseInt(val)
  const stderr = val.indexOf('|') >= 0 ? val.slice(val.indexOf('|') + 1) : ''
  if (code === 1) return 'FFmpeg 不含此编码器（需换用 full_build 完整版）'
  if (code === 2) return '显卡驱动不支持或硬件不满足要求'
  return '编码器不可用'
}

async function warnOnFatalCapabilityGaps() {
  try {
    const fatal = (await CheckCapabilities()).filter(c => !c.ok && c.fatal)
    if (!fatal.length) return
    for (const c of fatal) {
      appendLog(now(), 'error', `能力自检未通过：${c.name} — ${c.detail}。${c.impact}`)
    }
    toast(`FFmpeg 环境缺少 ${fatal.length} 项核心能力`, 'error')
  } catch (_) {}
}

async function initDetectEncoders() {
  warnOnFatalCapabilityGaps()
  try {
    const [avail, diag] = await Promise.all([DetectEncoders(), DiagnoseEncoders().catch(() => ({}))])
    _encoderAvail = avail
    _encoderDiag  = diag
  } catch (_) { return }

  const encMap = { nvenc: ['h264_nvenc', 'hevc_nvenc'], amf: ['h264_amf', 'hevc_amf'], qsv: ['h264_qsv', 'hevc_qsv'] }
  const encSel = document.getElementById('encoderSelect')
  const keep = new Set(['cpu'])
  for (const opt of [...encSel.options]) {
    if (opt.value === 'cpu') continue
    const encs = encMap[opt.value] || []
    if (!encs.some(e => _encoderAvail[e])) {
      opt.remove()
    } else {
      keep.add(opt.value)
    }
  }
  if (!keep.has(encSel.value)) {
    encSel.value = bestGpuEncoder()
  }
  const h265opt = document.querySelector('#codecSelect option[value="h265"]')
  if (h265opt && !_encoderAvail['libx265'] && !_encoderAvail['hevc_nvenc'] && !_encoderAvail['hevc_amf'] && !_encoderAvail['hevc_qsv']) {
    h265opt.disabled = true
  }
}


window.applyPreset = function (name, silent = false) {
  document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'))
  document.querySelector(`.preset-btn[onclick="applyPreset('${name}')"]`)?.classList.add('active')

  const setMode = v => { document.querySelector(`input[name="mode"][value="${v}"]`).checked = true; onModeChange() }
  const setFmt  = v => { document.querySelector(`input[name="format"][value="${v}"]`).checked = true; onFormatChange() }
  const setVal  = (id, v) => { document.getElementById(id).value = v }

  const labels = {
    soft:    '软封推荐',
    '4k':    '4K 高质量',
    '1080p': '1080p 标准',
    fast:    '快速出片',
    fast_qsv_original: '核显专用',
    nvenc_h265: '独显高效',
  }

  switch (name) {
    case 'soft':
      setMode('soft')
      setFmt('mkv')
      break
    case '4k':
      setMode('hard')
      setFmt('mp4')
      setVal('codecSelect', 'h265')
      setVal('encoderSelect', 'cpu')
      setVal('crfSelect', '18')
      setVal('presetSelect', 'slow')
      setVal('scaleSelect', '0')
      break
    case '1080p':
      setMode('hard')
      setFmt('mp4')
      setVal('codecSelect', 'h265')
      setVal('encoderSelect', 'cpu')
      setVal('crfSelect', '22')
      setVal('presetSelect', 'medium')
      setVal('scaleSelect', '1080')
      break
    case 'fast':
      setMode('hard')
      setFmt('mp4')
      if (_encoderAvail['h264_nvenc']) {
        setVal('codecSelect', 'h265')
        setVal('encoderSelect', 'nvenc')
        setVal('crfSelect', '30')
      } else if (_encoderAvail['h264_amf']) {
        setVal('codecSelect', 'h265')
        setVal('encoderSelect', 'amf')
        setVal('crfSelect', '30')
      } else {
        setVal('codecSelect', 'h265')
        setVal('encoderSelect', 'cpu')
        setVal('crfSelect', '22')
      }
      setVal('presetSelect', 'fast')
      setVal('scaleSelect', '1080')
      break
    case 'fast_qsv_original':
      setMode('hard')
      setFmt('mp4')
      setVal('codecSelect', 'h265')
      setVal('encoderSelect', 'qsv')
      setVal('crfSelect', '28')
      setVal('presetSelect', 'fast')
      setVal('scaleSelect', '0')
      break
    case 'nvenc_h265':
      if (!_encoderAvail['h264_nvenc']) {
        const reason = gpuEncUnavailMsg('h264_nvenc')
        toast(`NVENC 编码器不可用：${reason}`, 'error')
        return
      }
      if (_lastScanResult?.isHdr) {
        toast('检测到 HDR 片源，NVENC 硬压可能偏暗', 'warn')
      }
      setMode('hard')
      setFmt('mkv')
      setVal('codecSelect', 'h265')
      setVal('encoderSelect', 'nvenc')
      setVal('presetSelect', 'slow')
      setVal('scaleSelect', '0')
      applyNvencCQ(_lastScanResult, !silent)
      break
  }
  autoFixFmtIfNeeded()
  if (!silent) toast(labels[name] || '已应用方案', 'ok')
}

function autoFixFmtIfNeeded() {
  const curFmt = document.querySelector('input[name="format"]:checked')
  if (!curFmt || curFmt.value !== 'mp4' || !_lastScanResult) return
  const mp4IncompatCodecs = ['truehd', 'dts', 'dts-hd', 'pcm_bluray']
  const hasIncompat = _lastScanResult.audioTracks?.some(t => t.codec && mp4IncompatCodecs.includes(t.codec.toLowerCase()))
  const isHard = document.querySelector('input[name="mode"]:checked')?.value === 'hard'
  const isLargeFile = (_lastScanResult.fileSizeBytes || 0) >= 20 * 1024 * 1024 * 1024 || (_lastScanResult.videoBitrate || 0) >= 30000
  const switchToMkv = v => {
    const radio = document.querySelector(`input[name="format"][value="${v}"]`)
    if (radio) { radio.checked = true; onFormatChange() }
  }
  if (hasIncompat) {
    switchToMkv('mkv')
    toast('检测到 MP4 不兼容的音频编码，已自动切换输出格式为 MKV', 'warn')
  } else if (isHard && isLargeFile) {
    switchToMkv('mkv')
    toast('检测到高码率大文件，已自动切换输出格式为 MKV', 'warn')
  }
}


window.switchTab = function (name) {
  const tabs = ['mux', 'tools', 'subtitle', 'watermark', 'dedup', 'queue', 'settings', 'history']
  document.querySelectorAll('.tab-btn').forEach((b, i) => {
    b.classList.toggle('active', tabs[i] === name)
  })
  document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'))
  document.getElementById('tab-' + name).classList.add('active')
}


const LOG_MAX_LINES = 1000

function appendLogTo(area, time, level, msg) {
  if (!area) return
  const line = document.createElement('div')
  line.className = 'log-line'
  line.innerHTML = `<span class="log-time">${escapeHtml(time)}</span><span class="log-msg-${level}">${escapeHtml(msg)}</span>`
  area.appendChild(line)
  while (area.childElementCount > LOG_MAX_LINES) area.removeChild(area.firstElementChild)
  area.scrollTop = area.scrollHeight
}

function appendLog(time, level, msg) {
  appendLogTo(document.getElementById('logArea'), time, level, msg)
}

function escapeHtml(s) {
  return String(s ?? '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
           .replace(/"/g,'&quot;').replace(/'/g,'&#39;')
}


function clampNum(raw, min, max, fallback = 0) {
  const v = parseFloat(raw)
  if (!Number.isFinite(v)) return fallback
  return Math.min(max, Math.max(min, v))
}

function parseWebBitrateKbps(id) {
  const el = document.getElementById(id)
  if (!el) return 0
  const raw = String(el.value ?? '').trim()
  if (raw === '' || raw === '0') return 0
  return Math.round(clampNum(raw, 500, 30000, 0))
}

function loadWebBitrateValue(el, raw) {
  if (!el || raw === undefined || raw === null || raw === '') {
    if (el) el.value = ''
    return
  }
  const n = Number(raw)
  el.value = (!Number.isFinite(n) || n === 0 || n === 3000) ? '' : String(raw)
}

function isBusy() {
  return _isProcessing || _wmProcessing || _dedupProcessing || _queueRunning
}

function busyReason() {
  if (_isProcessing)    return '封装/字幕任务'
  if (_wmProcessing)    return '水印任务'
  if (_dedupProcessing) return '去重任务'
  if (_queueRunning)    return '任务队列'
  return '任务'
}

async function runJob(label, fn, opts = {}) {
  const { checkBusy = true, before, after } = opts
  if (checkBusy && isBusy()) {
    toast(`${busyReason()}正在执行，请等待完成或停止后再试`, 'warn')
    return false
  }
  try {
    before?.()
    const r = await fn()
    if (r === false) {
      toast(`${label}失败，请查看日志`, 'error')
      return false
    }
    return r
  } catch (e) {
    appendLog(now(), 'error', `${label}调用失败: ${e}`)
    toast(`${label}失败: ${e}`, 'error')
    return false
  } finally {
    after?.()
  }
}

window.clearLog = function () {
  const area = document.getElementById('logArea')
  if (!area.hasChildNodes()) { appendLog(now(), 'warn', '日志已为空'); return }
  area.innerHTML = ''
}


let _wmProcessing    = false
let _dedupProcessing = false


window.selectInput = async function () {
  const dir = await SelectDirectory('选择输入目录')
  if (!dir) return
  document.getElementById('inputDir').value = dir
  saveDirs()
  autoScanDir(dir)
}

window.selectOutput = async function () {
  const dir = await SelectDirectory('选择输出目录')
  if (dir) {
    document.getElementById('outputDir').value = dir
    saveDirs()
    refreshDiskUsage()
  }
}


async function autoScanDir(dir) {
  const result = await ScanDirectory(dir)
  if (!result) return
  _lastScanResult = result

  const sel = document.getElementById('audioTrack')
  sel.innerHTML = '<option value="-1">全部保留</option>'
  if (result.audioTracks && result.audioTracks.length > 0) {
    result.audioTracks.forEach(t => {
      const label = [t.codec, t.language, t.title].filter(Boolean).join(' · ') || `轨道 ${t.index}`
      const opt = document.createElement('option')
      opt.value = t.index
      opt.textContent = `轨 ${t.index}: ${label}`
      sel.appendChild(opt)
    })
  }

  const activePreset = document.querySelector('.preset-btn.active')
  if (activePreset && activePreset.getAttribute('onclick')?.includes('nvenc_h265')) {
    applyNvencCQ(result, false)
  }

  autoFixFmtIfNeeded()
}

function calcNvencCQ(scanResult) {
  if (!scanResult) return 30
  const kbps = scanResult.videoBitrate || 0
  const h = scanResult.height || 0
  let cq = 30
  if (kbps > 15000)      cq = 28
  else if (kbps > 8000)  cq = 30
  else if (kbps > 3000)  cq = 31
  else if (kbps > 0)     cq = 32
  if (h > 1440) cq = Math.max(26, cq - 2)
  return cq
}

function applyNvencCQ(scanResult, showToast = true) {
  const cq = calcNvencCQ(scanResult)
  const sel = document.getElementById('crfSelect')
  let applied = cq
  if (![...sel.options].some(o => o.value === String(cq))) {
    let bestDiff = Infinity
    for (const o of sel.options) {
      const d = Math.abs(parseInt(o.value, 10) - cq)
      if (d < bestDiff) { bestDiff = d; applied = parseInt(o.value, 10) }
    }
  }
  sel.value = String(applied)
  if (showToast && scanResult) {
    const kbps = scanResult.videoBitrate || 0
    const kbpsStr = kbps > 0 ? `片源码率 ${kbps} kbps` : '未读到片源码率'
    toast(`自动匹配 CQ=${applied}（${kbpsStr}）`, 'ok')
  }
}


window.onModeChange = function () {
  const isHard = document.querySelector('input[name="mode"]:checked').value === 'hard'
  document.getElementById('hardsubOptions').classList.toggle('hidden', !isHard)
  document.getElementById('webVersionRow')?.classList.toggle('hidden', !isHard)
  document.getElementById('softOptions').classList.toggle('hidden', isHard)
  document.getElementById('offsetRow').classList.toggle('hidden', isHard)
  const tenBitRow = document.getElementById('advTenBitRow')
  if (tenBitRow) tenBitRow.classList.toggle('hidden', !isHard)
  const hdrRow = document.getElementById('advHDRRow')
  if (hdrRow) hdrRow.classList.toggle('hidden', !isHard)
  const cropRow = document.getElementById('advCropRow')
  if (cropRow) cropRow.classList.toggle('hidden', !isHard)
  onFormatChange()
}

window.toggleAdvanced = function () {
  const panel  = document.getElementById('advPanel')
  const toggle = panel.previousElementSibling
  const isOpen = !panel.classList.contains('hidden')
  panel.classList.toggle('hidden', isOpen)
  toggle.classList.toggle('open', !isOpen)
}

window.onFormatChange = function () {
  const isMKV  = document.querySelector('input[name="format"]:checked').value === 'mkv'
  const isSoft = document.querySelector('input[name="mode"]:checked').value === 'soft'
  document.getElementById('embedFontsRow').classList.toggle('hidden', !(isMKV && isSoft))
}

window.onCodecChange = function () {
  const codec  = document.getElementById('codecSelect').value
  const hint   = document.getElementById('av1Hint')
  if (hint) hint.classList.toggle('hidden', codec !== 'av1')
}


window.startProcess = async function () {
  const inputDir = document.getElementById('inputDir').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!inputDir)  { toast('请先选择输入目录', 'warn'); return }
  if (!outputDir) { toast('请先选择输出目录', 'warn'); return }
  if (isBusy()) { toast(`${busyReason()}正在执行，请等待完成或停止后再试`, 'warn'); return }
  setProcessing(true)

  const activePreset = document.querySelector('.preset-btn.active')
  if (activePreset && activePreset.getAttribute('onclick')?.includes('nvenc_h265')) {
    await autoScanDir(inputDir)
    applyNvencCQ(_lastScanResult, true)
  }

  const opts = collectOpts()
  await runJob('压制', async () => {
    const ok = await ProcessFiles({ ...opts, shutdownAfter: false })
    if (ok && document.getElementById('chkShutdown').checked) ShutdownSystem()
    return ok
  }, { checkBusy: false, after: () => setProcessing(false) })
}

window.stopProcess = async function () {
  appendLog(now(), 'warn', '正在停止...')
  await StopProcess()
}

window.generateSubtitlePreview = async function () {
  const inputDir = document.getElementById('inputDir').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!inputDir)  { toast('请先选择输入目录', 'warn'); return }
  if (!outputDir) { toast('请先选择输出目录', 'warn'); return }
  const opts = collectOpts()
  await runJob('字幕预览', () => GenerateSubtitlePreview(opts), {
    before: () => setProcessing(true),
    after:  () => setProcessing(false),
  })
}


function collectOpts() {
  const activePreset = document.querySelector('.preset-btn.active')
  const isNvenc = document.getElementById('encoderSelect').value === 'nvenc'
  const scaleHeight = parseInt(document.getElementById('scaleSelect').value, 10)
  const autoCropChecked = document.getElementById('chkAutoCrop').checked
  const nvencAutoScale = !!(
    activePreset &&
    activePreset.getAttribute('onclick')?.includes('nvenc_h265') &&
    isNvenc &&
    scaleHeight === 0
  )
  const nvencAutoCQ = !!(
    activePreset &&
    activePreset.getAttribute('onclick')?.includes('nvenc_h265') &&
    isNvenc
  )
  const nvencAutoCrop = !!(
    activePreset &&
    activePreset.getAttribute('onclick')?.includes('nvenc_h265') &&
    isNvenc &&
    !autoCropChecked
  )
  return {
    inputDir:         document.getElementById('inputDir').value.trim(),
    outputDir:        document.getElementById('outputDir').value.trim(),
    hardsub:          document.querySelector('input[name="mode"]:checked').value === 'hard',
    format:           document.querySelector('input[name="format"]:checked').value,
    encoder:          document.getElementById('encoderSelect').value,
    codec:            document.getElementById('codecSelect').value,
    crf:              parseInt(document.getElementById('crfSelect').value, 10) || 22,
    preset:           document.getElementById('presetSelect').value,
    scaleHeight,
    nvencAutoScale,
    nvencAutoCQ,
    nvencAutoCrop,
    keepOriginalSubs: document.getElementById('keepOriginalSubs').checked,
    embedFonts:       document.getElementById('chkEmbedFonts').checked,
    normalizeAudio:   document.getElementById('chkNormalize').checked,
    subtitleOffset:   clampNum(document.getElementById('subtitleOffset').value, -600, 600, 0),
    audioTrackIndex:  parseInt(document.getElementById('audioTrack').value, 10),
    audioDelay:       Math.round(clampNum(document.getElementById('audioDelay').value, -60000, 60000, 0)),
    tenBit:           document.getElementById('chkTenBit').checked,
    hdrPassthrough:   document.getElementById('chkHDR').checked,
    hdr2sdr:          document.getElementById('chkHDR2SDR').checked,
    autoCrop:         autoCropChecked,
    keepSubStyle:     document.getElementById('chkKeepSubStyle')?.checked || false,
    stripDv:          document.getElementById('chkStripDV').checked,
    audioBitrate:     document.getElementById('audioBitrate').value,
    multiSub:         document.getElementById('chkMultiSub').checked,
    extraArgs:        document.getElementById('extraArgs').value.trim(),
    filenameTmpl:     document.getElementById('filenameTmpl').value.trim(),
    notify:           document.getElementById('chkNotify').checked,
    autoOpenOutput:   document.getElementById('chkOpenOutput').checked,
    shutdownAfter:    document.getElementById('chkShutdown').checked,
    webVersion:       document.querySelector('input[name="mode"]:checked').value === 'hard' && !!document.getElementById('chkWebVersion')?.checked,
    webBitrateKbps:   parseWebBitrateKbps('webBitrate'),
    webCodec:         document.getElementById('webCodec')?.value || 'h265',
  }
}

window.onWebVersionChange = function () {
  const on = !!document.getElementById('chkWebVersion')?.checked
  const box = document.getElementById('webVersionOptions')
  if (box) box.style.display = on ? 'block' : 'none'
  saveEncodeOpts()
}

window.addCurrentToQueue = async function () {
  const opts = collectOpts()
  if (!opts.inputDir)  { toast('请先选择输入目录', 'warn'); return }
  if (!opts.outputDir) { toast('请先选择输出目录', 'warn'); return }
  const name = opts.inputDir.split(/[\\/]/).pop() || '任务'
  await AddToQueue(opts, name)
  toast('已加入队列: ' + name)
  switchTab('queue')
}

let _queueRunning = false

function syncQueueButtons() {
  const startBtn   = document.getElementById('queueStartBtn')
  const stopBtn    = document.getElementById('queueStopBtn')
  const stopAllBtn = document.getElementById('queueStopAllBtn')
  if (!startBtn || !stopBtn) return
  startBtn.classList.toggle('hidden', _queueRunning)
  stopBtn.classList.toggle('hidden', !_queueRunning)
  if (stopAllBtn) stopAllBtn.classList.toggle('hidden', !_queueRunning)
}

window.startQueue = async function () {
  const btn = document.getElementById('queueStartBtn')
  if (btn.disabled) return
  btn.disabled = true
  try {
    const schedule   = document.getElementById('queueSchedule').value || ''
    const postAction = document.getElementById('queuePostAction').value
    const ok = await StartQueue(schedule, postAction)
    if (ok) {
      _queueRunning = true
      syncQueueButtons()
      if (schedule) toast('队列已定时，将于 ' + schedule + ' 开始执行', 'ok')
    } else {
      toast('队列启动失败，请查看日志', 'warn')
    }
  } catch (e) {
    toast('队列启动异常: ' + e, 'error')
  } finally {
    btn.disabled = false
  }
}

window.stopQueue = async function () {
  await StopQueue()
  _queueRunning = false
  syncQueueButtons()
}

window.stopQueueAll = async function () {
  await StopProcess()
  _queueRunning = false
  syncQueueButtons()
}

window.clearQueue = async function () {
  await ClearQueue()
}

window.resetCancelledTasks = async function () {
  await ResetCancelledTasks()
}

window.removeQueueTask = async function (id) {
  await RemoveQueueTask(id)
}

const _statusLabel = { pending: '等待中', running: '进行中', done: '已完成', failed: '失败', cancelled: '已取消' }
const _statusClass = { pending: 'qs-pending', running: 'qs-running', done: 'qs-done', failed: 'qs-failed', cancelled: 'qs-cancelled' }

function renderQueue(tasks) {
  const list  = document.getElementById('queueList')
  const empty = document.getElementById('queueEmpty')
  const badge = document.getElementById('queueBadge')

  const pending = tasks.filter(t => t.status === 'pending' || t.status === 'running').length
  if (pending > 0) {
    badge.textContent = pending
    badge.classList.remove('hidden')
  } else {
    badge.classList.add('hidden')
  }

  if (!tasks || tasks.length === 0) {
    list.classList.add('hidden')
    empty.classList.remove('hidden')
    syncQueueButtons()
    return
  }
  list.classList.remove('hidden')
  empty.classList.add('hidden')

  list.innerHTML = tasks.map((t, i) => {
    const label = _statusLabel[t.status] || t.status
    const cls   = _statusClass[t.status] || ''
    const mode  = t.opts.hardsub ? '硬字幕' : '软字幕'
    const codec = (t.opts.codec || 'copy').toUpperCase()
    const time  = t.startedAt ? `${t.addedAt} → ${t.startedAt}` : t.addedAt
    const canRemove = t.status !== 'running'
    const eName = escapeHtml(t.name)
    const eDir  = escapeHtml(t.opts.inputDir || '')
    const eErr  = t.err ? escapeHtml(t.err) : ''
    const eId   = escapeHtml(t.id)
    return `<div class="queue-row">
      <span class="queue-idx">${i + 1}</span>
      <div class="queue-info">
        <span class="queue-name" title="${eDir}">${eName}</span>
        <span class="queue-meta">${mode} · ${codec} · ${eDir}</span>
        ${eErr ? `<span class="queue-err">${eErr}</span>` : ''}
      </div>
      <span class="queue-status ${cls}">${label}</span>
      <span class="queue-time">${escapeHtml(time)}</span>
      ${canRemove ? `<button class="btn btn-ghost btn-sm queue-del" onclick="removeQueueTask('${eId}')" title="移除">✕</button>` : '<span class="queue-del-ph"></span>'}
    </div>`
  }).join('')

  syncQueueButtons()
}


window.previewCompress = async function () {
  const inputDir = document.getElementById('inputDir').value.trim()
  if (!inputDir) { appendLog(now(), 'warn', '请先选择输入目录'); return }

  const isHard = document.querySelector('input[name="mode"]:checked').value === 'hard'
  if (!isHard) { toast('试算预览仅适用于硬压模式（软封不重编码）', 'warn'); return }

  const btn = document.getElementById('previewBtn')
  btn.disabled = true
  btn.textContent = '试算中…'

  const opts = {
    inputDir,
    outputDir:    document.getElementById('outputDir').value.trim() || inputDir,
    hardsub:      true,
    encoder:      document.getElementById('encoderSelect').value,
    codec:        document.getElementById('codecSelect').value,
    crf:          parseInt(document.getElementById('crfSelect').value, 10),
    preset:       document.getElementById('presetSelect').value,
    scaleHeight:  parseInt(document.getElementById('scaleSelect').value, 10),
    tenBit:       document.getElementById('chkTenBit').checked,
    format: 'mp4', subtitleOffset: 0, audioTrackIndex: -1, audioDelay: 0,
    audioBitrate: '192k', multiSub: false, hdrPassthrough: false,
    keepOriginalSubs: false, embedFonts: false, normalizeAudio: false,
    extraArgs: '', filenameTmpl: '', notify: false, autoOpenOutput: false,
  }

  try {
    const r = await PreviewCompress(opts)
    if (r) renderPreview(r)
  } catch (e) {
    appendLog(now(), 'error', '试算失败: ' + e)
  } finally {
    btn.disabled = false
    btn.textContent = '试算预览'
  }
}

function renderPreview(r) {
  const panel = document.getElementById('previewPanel')
  const stats = document.getElementById('previewStats')
  const shots = document.getElementById('previewShots')

  const saving   = r.ratio > 0 ? ((1 - r.ratio) * 100) : 0
  const bigger   = saving < 0
  const savingTxt = bigger
    ? `<span style="color:#c0392b;font-weight:700">⚠ 变大 ${Math.abs(saving).toFixed(1)}%</span>`
    : `<span style="color:#27ae60;font-weight:700">节省 ${saving.toFixed(1)}%</span>`

  const fmtSize = (mb) => mb >= 1024 ? (mb / 1024).toFixed(2) + ' GB' : mb.toFixed(1) + ' MB'

  stats.innerHTML = `
    <div class="preview-stat">
      <span class="preview-stat-label">原始文件大小</span>
      <span class="preview-stat-val">${fmtSize(r.originalSizeMB)}</span>
    </div>
    <div class="preview-stat">
      <span class="preview-stat-label">预估输出大小</span>
      <span class="preview-stat-val" style="${bigger ? 'color:#c0392b' : ''}">${fmtSize(r.estimatedSizeMB)}</span>
    </div>
    <div class="preview-stat">
      <span class="preview-stat-label">结果</span>
      <span class="preview-stat-val">${savingTxt}</span>
    </div>
    <div class="preview-stat">
      <span class="preview-stat-label">编码</span>
      <span class="preview-stat-val">${r.codec} · CRF ${r.crf || '基准'}</span>
    </div>
    <div class="preview-stat" style="grid-column:1/-1;font-size:11px;color:var(--text-hint);font-weight:400">预估仅供参考</div>`

  let shotHtml = ''
  if (r.originalShot) {
    shotHtml += `<div class="preview-shot-wrap">
      <div class="preview-shot-label">原始帧</div>
      <img class="preview-shot-img" src="${r.originalShot}" alt="原始" />
    </div>`
  }
  if (r.compressedShot) {
    shotHtml += `<div class="preview-shot-wrap">
      <div class="preview-shot-label">压缩后（${r.clipDurationSec.toFixed(0)}s 片段）</div>
      <img class="preview-shot-img" src="${r.compressedShot}" alt="压缩后" />
    </div>`
  }
  shots.innerHTML = shotHtml || '<span style="color:var(--text-hint);font-size:13px">截图获取失败</span>'

  panel.classList.remove('hidden')
  panel.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
}

window.closePreview = function () {
  document.getElementById('previewPanel').classList.add('hidden')
}

window.extractSubs = async function () {
  const inputDir  = document.getElementById('inputDir').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!inputDir)  { appendLog(now(), 'warn', '请先选择输入目录'); return }
  await runJob('提取字幕', () => ExtractSubtitles(inputDir, outputDir || inputDir))
}

let _isProcessing = false
function setProcessing(active) {
  _isProcessing = active
  document.getElementById('startBtn').disabled = active
  document.getElementById('startBtn').textContent = active ? '处理中...' : '开始处理'
  document.getElementById('stopBtn').classList.toggle('hidden', !active)
  const bar  = document.getElementById('progressBar')
  const fill = document.getElementById('progressFill')
  const txt  = document.getElementById('progressText')
  if (active) {
    if (bar) bar.classList.remove('hidden')
    if (fill) fill.style.width = '0%'
    if (txt) txt.textContent = '0%'
    return
  }
  setTimeout(() => {
    if (_isProcessing) return
    if (bar) bar.classList.add('hidden')
    if (fill) fill.style.width = '0%'
    if (txt) txt.textContent = ''
  }, 1500)
}


window.selectInfoDir = async function () {
  const dir = await SelectDirectory('选择视频目录')
  if (dir) document.getElementById('infoInputDir').value = dir
}

window.showVideoInfo = async function () {
  const dir = document.getElementById('infoInputDir').value.trim()
  if (!dir) { toast('请先选择目录'); return }
  const r = await ScanDirectory(dir)
  if (!r) { toast('未找到视频文件', 'error'); return }

  const panel = document.getElementById('videoInfoPanel')
  const grid  = document.getElementById('infoGrid')
  panel.classList.remove('hidden')

  const rows = [
    ['文件名', r.videoFile],
    ['分辨率', r.width && r.height ? `${r.width} × ${r.height}` : '未知'],
    ['视频编码', r.videoCodec || '未知'],
    ['时长', r.durationStr || '未知'],
    ['文件大小', r.fileSize || '未知'],
    ['音轨数', (r.audioTracks || []).length + ' 条'],
    ['内嵌字幕轨', (r.subTracks || []).length + ' 条'],
    ['外挂字幕', r.subFile || '无'],
  ]
  grid.innerHTML = rows.map(([k, v]) =>
    `<span class="info-key">${k}</span><span class="info-val">${escapeHtml(String(v))}</span>`
  ).join('')

  ;(r.audioTracks || []).forEach(t => {
    const label = [t.codec, t.language, t.title].filter(Boolean).join(' · ')
    grid.innerHTML += `<span class="info-key">音轨 ${t.index}</span><span class="info-val">${escapeHtml(label)}</span>`
  })
  ;(r.subTracks || []).forEach(t => {
    const label = [t.codec, t.language, t.title].filter(Boolean).join(' · ')
    grid.innerHTML += `<span class="info-key">字幕轨 ${t.index}</span><span class="info-val">${escapeHtml(label)}</span>`
  })
}

window.selectSubFile = async function () {
  const f = await SelectFile('选择字幕文件')
  if (f) document.getElementById('subConvertFile').value = f
}

window.convertSub = async function () {
  const file      = document.getElementById('subConvertFile').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!file)      { toast('请先选择字幕文件'); return }
  if (!outputDir) { toast('请先在封装标签设置输出目录'); return }
  await runJob('字幕转换', () => ConvertSubtitle(file, outputDir))
}


window.selectShiftFile = async function () {
  const f = await SelectSubtitleFile('选择字幕文件')
  if (!f) return
  document.getElementById('shiftSubFile').value = f
  const lines = await ReadSubtitlePreview(f)
  const panel = document.getElementById('subPreviewPanel')
  const linesEl = document.getElementById('subPreviewLines')
  if (lines && lines.length > 0) {
    panel.classList.remove('hidden')
    linesEl.innerHTML = lines.map(l => `<div class="sub-preview-line">${escapeHtml(l)}</div>`).join('')
  } else {
    panel.classList.add('hidden')
  }
}

window.shiftSub = async function () {
  const file      = document.getElementById('shiftSubFile').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!file)      { toast('请先选择字幕文件'); return }
  if (!outputDir) { toast('请先在封装标签设置输出目录'); return }
  const offset = clampNum(document.getElementById('shiftOffset').value, -600, 600, 0)
  if (offset === 0) { toast('偏移量不能为 0，且需在 ±600 秒内'); return }
  await runJob('字幕偏移', () => ShiftSubtitleFile(file, outputDir, offset))
}


window.selectSyncVideo = async function () {
  const f = await SelectVideoFile('选择参考视频文件')
  if (f) document.getElementById('syncVideoFile').value = f
}

window.selectSyncSub = async function () {
  const f = await SelectSubtitleFile('选择需要同步的字幕文件')
  if (f) document.getElementById('syncSubFile').value = f
}

window.checkSyncTool = async function () {
  const btn = event?.target
  const ok = await CheckFFSubSync()
  if (ok) {
    toast('ffsubsync 已安装', 'ok')
    if (btn) { btn.textContent = '✓ 已安装'; btn.disabled = true }
  } else {
    toast('未检测到 ffsubsync，请在「设置」标签页查看安装方法', 'warn')
  }
}

window.autoSyncSub = async function () {
  const video = document.getElementById('syncVideoFile').value.trim()
  const sub   = document.getElementById('syncSubFile').value.trim()
  const out   = document.getElementById('outputDir').value.trim()
  if (!video) { toast('请先选择视频文件', 'warn'); return }
  if (!sub)   { toast('请先选择字幕文件', 'warn'); return }
  if (!out)   { toast('请先在封装页设置输出目录', 'warn'); return }
  await runJob('字幕自动同步', () => AutoSyncSubtitle(video, sub, out))
}


let _whisperStartGuardUntil = 0
function armWhisperStartGuard() {
  _whisperStartGuardUntil = Date.now() + 600
  if (document.activeElement && document.activeElement.blur) document.activeElement.blur()
}

window.selectWhisperFile = async function () {
  const f = await SelectFile('选择视频或音频文件')
  if (f) {
    document.getElementById('whisperFile').value = f
    armWhisperStartGuard()
  }
}

window.selectWhisperDir = async function () {
  const d = await SelectDirectory('选择视频/音频目录（批量转字幕）')
  if (d) {
    document.getElementById('whisperFile').value = d
    armWhisperStartGuard()
  }
}

window.whisperTranscribe = async function () {
  if (Date.now() < _whisperStartGuardUntil) {
    return
  }
  const file   = document.getElementById('whisperFile').value.trim()
  const out    = document.getElementById('outputDir').value.trim()
  if (!file) { toast('请先选择视频或音频文件', 'warn'); return }
  if (!out)  { toast('请先在封装页设置输出目录', 'warn'); return }
  const lang   = document.getElementById('whisperLang').value
  const engine = document.querySelector('input[name="asrEngine"]:checked')?.value || 'whisperx'
  const aiCorrect = document.getElementById('chkAsrAiCorrect')?.checked ?? true
  await runJob('转字幕', () => {
    if (engine === 'qwen') {
      saveQwenKeys()
      return QwenASRTranscribe(
        file, out, lang,
        document.getElementById('qwenDashKey')?.value.trim()     || '',
        document.getElementById('qwenOssId')?.value.trim()       || '',
        document.getElementById('qwenOssSecret')?.value.trim()   || '',
        document.getElementById('qwenOssBucket')?.value.trim()   || '',
        document.getElementById('qwenOssEndpoint')?.value.trim() || '',
        aiCorrect,
      )
    }
    saveWhisperXSettings()
    const { pythonPath, httpProxy, modelDir } = getWhisperXSettings()
    return WhisperXASRTranscribe(file, out, lang, pythonPath, httpProxy, modelDir, aiCorrect)
  }, {
    before: () => setProcessing(true),
    after:  () => setProcessing(false),
  })
}

window.toggleASREngine = function () {
  const engine = document.querySelector('input[name="asrEngine"]:checked')?.value || 'whisperx'
  const qwenFields = document.getElementById('qwenConfigFields')
  if (qwenFields) qwenFields.style.display = (engine === 'qwen' && !_cfgHasQwenASR) ? 'flex' : 'none'
}

function saveQwenKeys() {
  localStorage.setItem('muxer-qwen-keys', JSON.stringify({
    dashKey:     document.getElementById('qwenDashKey')?.value     || '',
    ossId:       document.getElementById('qwenOssId')?.value       || '',
    ossSecret:   document.getElementById('qwenOssSecret')?.value   || '',
    ossBucket:   document.getElementById('qwenOssBucket')?.value   || '',
    ossEndpoint: document.getElementById('qwenOssEndpoint')?.value || '',
  }))
}

const WHISPERX_STORAGE = 'muxer-whisperx'

function getWhisperXSettings() {
  try {
    const s = JSON.parse(localStorage.getItem(WHISPERX_STORAGE) || '{}')
    const httpProxy = ('httpProxy' in s ? s.httpProxy : 'http://127.0.0.1:7890').trim()
    return {
      pythonPath: (s.pythonPath || '').trim(),
      httpProxy,
      modelDir: (s.modelDir || '').trim(),
    }
  } catch (_) {
    return { pythonPath: '', httpProxy: 'http://127.0.0.1:7890', modelDir: '' }
  }
}

function loadWhisperXSettings() {
  const { pythonPath, httpProxy, modelDir } = getWhisperXSettings()
  const pyEl = document.getElementById('whisperxPython')
  const pxEl = document.getElementById('whisperxProxy')
  const mdEl = document.getElementById('whisperxModelDir')
  if (pyEl) pyEl.value = pythonPath
  if (pxEl) pxEl.value = httpProxy
  if (mdEl) mdEl.value = modelDir
  toggleASREngine()
}

window.saveWhisperXSettings = function () {
  const pythonPath = document.getElementById('whisperxPython')?.value.trim() || ''
  const httpProxy = document.getElementById('whisperxProxy')?.value.trim() || ''
  const modelDir = document.getElementById('whisperxModelDir')?.value.trim() || ''
  localStorage.setItem(WHISPERX_STORAGE, JSON.stringify({ pythonPath, httpProxy, modelDir }))
}

window.selectWhisperxModelDir = async function () {
  const dir = await SelectDirectory('选择 WhisperX 模型目录（HuggingFace/torch 缓存的父目录）')
  if (dir) {
    const el = document.getElementById('whisperxModelDir')
    if (el) el.value = dir
    saveWhisperXSettings()
  }
}

window.checkWhisperXTool = async function () {
  saveWhisperXSettings()
  const { pythonPath, httpProxy, modelDir } = getWhisperXSettings()
  const ok = await CheckWhisperX(pythonPath, httpProxy, modelDir)
  toast(ok ? 'WhisperX 已就绪' : 'WhisperX 未就绪', ok ? 'ok' : 'warn')
}

window.toggleLangDropdown = function () {
  const panel = document.getElementById('whisperLangPanel')
  if (!panel) return
  const isOpen = !panel.classList.contains('hidden')
  if (isOpen) {
    panel.classList.add('hidden')
  } else {
    panel.classList.remove('hidden')
    const search = document.getElementById('whisperLangSearch')
    if (search) { search.value = ''; filterLangDropdown(''); search.focus() }
  }
}

window.selectLang = function (el) {
  const val = el.dataset.value
  const label = el.textContent
  document.getElementById('whisperLang').value = val
  document.getElementById('whisperLangLabel').textContent = label
  document.querySelectorAll('#whisperLangList .lang-opt').forEach(o => o.classList.remove('selected'))
  el.classList.add('selected')
  document.getElementById('whisperLangPanel').classList.add('hidden')
}

window.filterLangDropdown = function (q) {
  q = q.toLowerCase().trim()
  const list = document.getElementById('whisperLangList')
  if (!list) return
  const opts = list.querySelectorAll('.lang-opt')
  const groups = list.querySelectorAll('.lang-group-label')
  const groupVisible = new Map()
  let lastGroup = null
  opts.forEach(o => {
    const text = o.textContent.toLowerCase()
    const code = (o.dataset.value || '').toLowerCase()
    const show = !q || text.includes(q) || code.includes(q)
    o.classList.toggle('hidden-opt', !show)
    if (o.previousElementSibling?.classList?.contains('lang-group-label')) {
      lastGroup = o.previousElementSibling
    }
    if (show && lastGroup) {
      groupVisible.set(lastGroup, true)
    }
  })
  groups.forEach(g => {
    g.classList.toggle('hidden-opt', q && !groupVisible.get(g))
  })
}

document.addEventListener('click', e => {
  const dd = document.getElementById('whisperLangDropdown')
  if (dd && !dd.contains(e.target)) {
    const panel = document.getElementById('whisperLangPanel')
    if (panel) panel.classList.add('hidden')
  }
})



window.extractAudio = async function () {
  const inputDir  = document.getElementById('inputDir').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!inputDir)  { toast('请先在封装标签设置输入目录'); return }
  if (!outputDir) { toast('请先在封装标签设置输出目录'); return }
  const fmt = document.querySelector('input[name="audioFmt"]:checked').value
  await runJob('提取音频', () => ExtractAudio(inputDir, outputDir, fmt))
}


window.selectScreenshotFile = async function () {
  const f = await SelectVideoFile('选择视频文件')
  if (f) document.getElementById('screenshotFile').value = f
}

window.takeScreenshot = async function () {
  const file      = document.getElementById('screenshotFile').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!file)      { toast('请先选择视频文件'); return }
  if (!outputDir) { toast('请先在封装标签设置输出目录'); return }
  const ts = document.getElementById('screenshotTime').value.trim() || '00:00:30'
  await runJob('截图', () => TakeScreenshot(file, outputDir, ts))
}


window.selectMetaFile = async function () {
  const f = await SelectVideoFile('选择视频文件')
  if (f) document.getElementById('metaFile').value = f
}

window.saveMeta = async function () {
  const file      = document.getElementById('metaFile').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!file)      { toast('请先选择视频文件'); return }
  if (!outputDir) { toast('请先在封装标签设置输出目录'); return }
  const title   = document.getElementById('metaTitle').value.trim()
  const year    = document.getElementById('metaYear').value.trim()
  const comment = document.getElementById('metaComment').value.trim()
  await runJob('修改元数据', () => EditMetadata(file, outputDir, title, year, comment))
}


window.selectChapterVideo = async function () {
  const f = await SelectVideoFile('选择视频文件')
  if (f) document.getElementById('chapterVideoFile').value = f
}

window.loadChapters = async function () {
  const file = document.getElementById('chapterVideoFile').value.trim()
  if (!file) { toast('请先选择视频文件', 'warn'); return }
  const chapters = await ReadChapters(file)
  renderChapterList(chapters || [])
}

function renderChapterList(chapters) {
  const el = document.getElementById('chapterList')
  if (!chapters || chapters.length === 0) {
    el.innerHTML = '<div class="chapter-empty">暂无章节，点击"添加章节"新增</div>'
    return
  }
  el.innerHTML = chapters.map((c, i) => `
    <div class="chapter-row" data-i="${i}">
      <input class="chapter-time input" type="text" value="${escapeHtml(c.start)}"
             placeholder="HH:MM:SS" spellcheck="false" />
      <input class="chapter-title input" type="text" value="${escapeHtml(c.title)}"
             placeholder="章节名称（可留空）" />
      <button class="sub-del-btn" onclick="chapterRemoveRow(this)" title="删除">✕</button>
    </div>`).join('')
}

function collectChapters() {
  return Array.from(document.getElementById('chapterList')
    .querySelectorAll('.chapter-row'))
    .map(row => ({
      start: row.querySelector('.chapter-time').value.trim(),
      title: row.querySelector('.chapter-title').value.trim(),
    }))
    .filter(c => c.start !== '')
}

window.chapterAddRow = function () {
  const el = document.getElementById('chapterList')
  const empty = el.querySelector('.chapter-empty')
  if (empty) empty.remove()
  const row = document.createElement('div')
  row.className = 'chapter-row'
  row.innerHTML = `
    <input class="chapter-time input" type="text" value=""
           placeholder="HH:MM:SS" spellcheck="false" />
    <input class="chapter-title input" type="text" value=""
           placeholder="章节名称（可留空）" />
    <button class="sub-del-btn" onclick="chapterRemoveRow(this)" title="删除">✕</button>`
  el.appendChild(row)
  row.querySelector('.chapter-time').focus()
}

window.chapterRemoveRow = function (btn) {
  const row = btn.closest('.chapter-row')
  row.remove()
  if (!document.getElementById('chapterList').querySelector('.chapter-row')) {
    document.getElementById('chapterList').innerHTML =
      '<div class="chapter-empty">暂无章节，点击"添加章节"新增</div>'
  }
}

window.chapterImportText = async function () {
  const f = await SelectFile('选择章节文本文件（每行 HH:MM:SS 章节名）')
  if (!f) return
  try {
    const lines = await ReadTextFile(f)
    if (!lines) return
    const chapters = lines
      .filter(l => l.trim())
      .map(l => {
        const m = l.trim().match(/^(\d{1,2}:\d{2}(?::\d{2})?)\s+(.*)$/)
        if (!m) return null
        let time = m[1]
        if (time.split(':').length === 2) time = '00:' + time
        return { start: time, title: m[2].trim() }
      })
      .filter(Boolean)
    if (chapters.length === 0) { toast('未能解析章节，请检查文件格式', 'warn'); return }
    renderChapterList(chapters)
    toast(`导入 ${chapters.length} 个章节`, 'ok')
  } catch (e) {
    toast('读取文件失败', 'error')
  }
}

window.writeChapters = async function () {
  const file = document.getElementById('chapterVideoFile').value.trim()
  const out  = document.getElementById('outputDir').value.trim()
  if (!file) { toast('请先选择视频文件', 'warn'); return }
  if (!out)  { toast('请先在封装页设置输出目录', 'warn'); return }
  const chapters = collectChapters()
  if (chapters.length === 0) { toast('请至少添加一个章节', 'warn'); return }
  await runJob('写入章节', () => WriteChapters(file, out, chapters))
}


window.selectVmafRef = async function () {
  const f = await SelectVideoFile('选择参考（原始）视频')
  if (f) document.getElementById('vmafRefFile').value = f
}
window.selectVmafDist = async function () {
  const f = await SelectVideoFile('选择压缩后的视频')
  if (f) document.getElementById('vmafDistFile').value = f
}
window.checkVmafSupport = async function () {
  const btn = event?.target
  const ok = await CheckVMAF()
  if (ok) {
    toast('FFmpeg 已包含 libvmaf，支持 VMAF 评分', 'ok')
    if (btn) { btn.textContent = '✓ libvmaf 可用'; btn.disabled = true }
  } else {
    toast('当前 FFmpeg 未包含 libvmaf，仅可使用 PSNR / SSIM', 'warn')
  }
}

window.scoreVmaf = async function () {
  const ref  = document.getElementById('vmafRefFile').value.trim()
  const dist = document.getElementById('vmafDistFile').value.trim()
  if (!ref)  { toast('请先选择参考视频', 'warn'); return }
  if (!dist) { toast('请先选择压缩视频', 'warn'); return }

  const panel = document.getElementById('vmafResult')
  panel.innerHTML = '<span class="vmaf-loading">评分中，请稍候…</span>'
  panel.classList.remove('hidden')

  const r = await ScoreVMAF(ref, dist)
  if (!r) { panel.classList.add('hidden'); return }

  const vmafGrade = v => v >= 93 ? 'grade-ok' : v >= 80 ? 'grade-warn' : 'grade-bad'
  const psnrGrade = v => v >= 40 ? 'grade-ok' : v >= 35 ? 'grade-warn' : 'grade-bad'
  const ssimGrade = v => v >= 0.98 ? 'grade-ok' : v >= 0.95 ? 'grade-warn' : 'grade-bad'

  const vmafRow = r.hasVmaf
    ? `<div class="vmaf-metric">
         <span class="vmaf-label">VMAF</span>
         <span class="vmaf-val ${vmafGrade(r.vmaf)}">${r.vmaf.toFixed(2)}</span>
         <span class="vmaf-hint">满分 100，≥ 93 高质量</span>
       </div>`
    : `<div class="vmaf-metric">
         <span class="vmaf-label">VMAF</span>
         <span class="vmaf-val grade-na">—</span>
         <span class="vmaf-hint">FFmpeg 未包含 libvmaf</span>
       </div>`

  panel.innerHTML = `
    <div class="vmaf-metrics">
      ${vmafRow}
      <div class="vmaf-metric">
        <span class="vmaf-label">PSNR</span>
        <span class="vmaf-val ${psnrGrade(r.psnr)}">${r.psnr > 0 ? r.psnr.toFixed(2) + ' dB' : '—'}</span>
        <span class="vmaf-hint">≥ 40 dB 优秀</span>
      </div>
      <div class="vmaf-metric">
        <span class="vmaf-label">SSIM</span>
        <span class="vmaf-val ${ssimGrade(r.ssim)}">${r.ssim > 0 ? r.ssim.toFixed(4) : '—'}</span>
        <span class="vmaf-hint">≥ 0.98 优秀</span>
      </div>
    </div>`
}


window.checkFFmpegEnv = async function () {
  const panel   = document.getElementById('ffmpegInfoPanel')
  const verEl   = document.getElementById('ffmpegVersion')
  const gpuEl   = document.getElementById('gpuInfo')
  const encEl   = document.getElementById('encoderList')

  verEl.textContent = '检测中...'
  gpuEl.textContent = ''
  panel.classList.remove('hidden')
  encEl.innerHTML = ''

  const [version, gpu, available] = await Promise.all([
    GetFFmpegInfo(), DetectGPU(), DetectEncoders()
  ])
  verEl.textContent = version
  gpuEl.textContent = gpu ? ('显卡: ' + gpu.replace(/\n/g, '  |  ')) : '显卡: 未检测到'

  const labels = {
    'libx264':    'CPU · H.264',    'libx265':    'CPU · H.265',
    'h264_nvenc': 'NVIDIA · H.264', 'hevc_nvenc': 'NVIDIA · H.265',
    'h264_amf':   'AMD · H.264',    'hevc_amf':   'AMD · H.265',
    'h264_qsv':   'Intel · H.264',  'hevc_qsv':   'Intel · H.265',
    'libsvtav1':  'CPU · AV1',
    'av1_nvenc':  'NVIDIA · AV1',   'av1_amf':    'AMD · AV1',    'av1_qsv': 'Intel · AV1',
  }

  encEl.innerHTML = Object.entries(labels)
    .filter(([k]) => available[k])
    .map(([, v]) => `<span class="enc-tag enc-ok">${v}</span>`)
    .join('')

  renderCapabilities(await CheckCapabilities().catch(() => []))
}

function renderCapabilities(list) {
  const el = document.getElementById('capabilityList')
  if (!el) return
  if (!list.length) { el.innerHTML = ''; return }

  const bad = list.filter(c => !c.ok)
  const head = bad.length === 0
    ? `<div class="field-hint" style="color:var(--enc-ok-text)">能力自检：${list.length} 项全部通过</div>`
    : `<div class="field-hint" style="color:var(--enc-na-text)">能力自检：${bad.length}/${list.length} 项存在问题</div>`

  const okTags = list.filter(c => c.ok)
    .map(c => `<span class="enc-tag enc-ok">${escapeHtml(c.name)}</span>`).join('')
  const badRows = bad.map(c => `
    <div style="margin-top:6px">
      <span class="enc-tag enc-na">${c.fatal ? '致命' : '降级'} · ${escapeHtml(c.name)}</span>
      <div class="field-hint" style="margin-top:2px">${escapeHtml(c.detail)}</div>
      <div class="field-hint" style="margin-top:2px">影响：${escapeHtml(c.impact)}</div>
    </div>`).join('')

  el.innerHTML = head + badRows + `<div class="encoder-list" style="margin-top:6px">${okTags}</div>`
}


function loadHistory() {
  try { return JSON.parse(localStorage.getItem('muxer-history') || '[]') } catch (_) { return [] }
}

function saveHistoryEntry(entry) {
  const list = loadHistory()
  list.unshift(entry)
  if (list.length > 100) list.pop()
  localStorage.setItem('muxer-history', JSON.stringify(list))
}

window.renderHistory = function renderHistory() {
  const all  = loadHistory()
  const el   = document.getElementById('historyList')
  const cnt  = document.getElementById('historyCount')
  if (!el || !cnt) return

  const mode = document.querySelector('input[name="historyFilter"]:checked')?.value || 'all'
  const list = mode === 'ok'   ? all.filter(e => e.ok)
             : mode === 'fail' ? all.filter(e => !e.ok)
             : all
  const okCount = all.filter(e => e.ok).length
  cnt.textContent = `共 ${all.length} 条（成功 ${okCount} · 失败 ${all.length - okCount}）`

  if (list.length === 0) {
    el.innerHTML = '<div class="history-empty">暂无记录</div>'
    return
  }
  el.innerHTML = list.map(e => `
    <div class="history-item ${e.ok ? 'ok' : 'fail'}">
      <span class="h-status">${e.ok ? '✓' : '✗'}</span>
      <span class="h-file">${escapeHtml(e.file)}</span>
      <span class="h-mode">${escapeHtml(e.mode)}</span>
      <span class="h-elapsed">${escapeHtml(e.elapsed)}</span>
      <span class="h-time">${escapeHtml(e.time)}</span>
    </div>`).join('')
}

window.exportHistoryCSV = function () {
  const list = loadHistory()
  if (list.length === 0) { toast('暂无记录可导出', 'warn'); return }
  const esc = v => `"${String(v ?? '').replace(/"/g, '""')}"`
  const rows = [['时间', '文件', '模式', '耗时', '结果'].map(esc).join(',')]
  for (const e of list) {
    rows.push([e.time, e.file, e.mode, e.elapsed, e.ok ? '成功' : '失败'].map(esc).join(','))
  }
  const blob = new Blob(['\uFEFF' + rows.join('\r\n')], { type: 'text/csv;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `Muxer处理记录_${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  toast(`已导出 ${list.length} 条记录`, 'ok')
}

window.clearHistory = function () {
  const n = loadHistory().length
  if (n === 0) { toast('暂无记录', 'warn'); return }
  localStorage.removeItem('muxer-history')
  renderHistory()
  toast(`已清空 ${n} 条记录`, 'ok')
}


const MUXER_REPO_URL = 'https://github.com/MuxerGUI/Muxer'

window.loadAppInfo = async function () {
  const box = document.getElementById('aboutInfo')
  if (!box) return
  let info
  try { info = await GetAppInfo() } catch (e) { box.textContent = '读取失败: ' + e; return }
  const rows = [
    ['当前版本', info.version],
    ['构建时间', info.buildTime || '未知'],
    ['运行环境', `${info.os}/${info.arch} · Go ${info.goVersion}`],
    ['当前品牌', info.brandName || info.brandUrl ? `${info.brandName || '—'} · ${info.brandUrl || '—'}` : '未配置（白标模式）'],
    ['配置文件', info.hasKeysFile ? '已加载 keys.json' : '未使用 keys.json（分享版）'],
    ['程序路径', info.exePath],
  ]
  const cells = rows.map(([k, v]) =>
    `<span class="info-key">${escapeHtml(k)}</span><span class="info-val">${escapeHtml(v)}</span>`
  )
  cells.push(
    '<span class="info-key">官方仓库</span>' +
    `<span class="info-val"><a class="info-link" href="#" title="${MUXER_REPO_URL}" ` +
    `onclick="openLink('${MUXER_REPO_URL}');return false">GitHub</a></span>`
  )
  box.innerHTML = cells.join('')
}

window.checkUpdate = async function () {
  const btn = document.getElementById('updateCheckBtn')
  const status = document.getElementById('updateStatus')
  if (!btn || btn.disabled) return
  btn.disabled = true
  if (status) status.textContent = '检测中...'
  let info
  try {
    info = await CheckUpdate(true)
  } catch (e) {
    if (status) status.textContent = '检测失败: ' + e
    btn.disabled = false
    return
  }
  btn.disabled = false
  if (!status) return

  let text
  if (info.error) {
    text = info.error
  } else if (info.hasUpdate) {
    text = `发现新版本 ${info.latest}${info.publishedAt ? ' · ' + info.publishedAt : ''}`
  } else {
    status.textContent = `已是最新 ${info.current}`
    return
  }

  const link = document.createElement('a')
  link.href = '#'
  link.textContent = '打开下载页'
  link.onclick = (e) => {
    e.preventDefault()
    window.openLink(info.url || '')
  }
  status.replaceChildren(document.createTextNode(text + ' · '), link)
}

async function loadBrandConfig() {
  let c
  try { c = await GetBrandConfig() } catch (_) { return }
  const e = c.effective || {}
  const set = (id, v) => { const el = document.getElementById(id); if (el) el.value = v || '' }
  set('cfgBrandName', c.brandName || e.brandName)
  set('cfgBrandUrl', c.brandUrl || e.brandUrl)
  set('cfgBrandTag', c.brandTag || e.brandTag)
  set('cfgWmUrl', c.watermarkUrl || e.watermarkUrl)
  set('cfgWmBrand', c.watermarkBrand || e.watermarkBrand)
  set('cfgWmTagline', c.watermarkTagline)
  const adEl = document.getElementById('concatAdText')
  if (adEl && !adEl.value.trim()) {
    adEl.value = [c.brandName || e.brandName, c.brandUrl || e.brandUrl].filter(Boolean).join(' ')
  }
}

window.saveBrandConfig = async function () {
  const get = id => document.getElementById(id)?.value.trim() || ''
  const ok = await runJob('保存品牌配置', () => SaveBrandConfig({
    brandName:        get('cfgBrandName'),
    brandUrl:         get('cfgBrandUrl'),
    brandTag:         get('cfgBrandTag'),
    watermarkUrl:     get('cfgWmUrl'),
    watermarkBrand:   get('cfgWmBrand'),
    watermarkTagline: document.getElementById('cfgWmTagline')?.value || '',
  }), { checkBusy: false })
  if (ok) {
    toast('品牌配置已保存，立即生效', 'ok')
    loadBrandConfig()
    applyConfigStatus()
  }
}

async function refreshDiskUsage() {
  const hint = document.getElementById('diskUsageHint')
  const dir  = document.getElementById('outputDir')?.value.trim() || ''
  if (!hint) return
  if (!dir) { hint.textContent = ''; return }
  let u
  try { u = await GetDiskUsage(dir) } catch (_) { hint.textContent = ''; return }
  if (!u?.available) { hint.textContent = ''; return }
  hint.textContent = `可用空间：${u.freeText}` + (u.lowWarning ? '　⚠ 空间偏低' : '')
  hint.style.color = u.lowWarning ? 'var(--warn, #c77)' : ''
}


function loadCustomPresets() {
  try { return JSON.parse(localStorage.getItem('muxer-customPresets') || '[]') } catch (_) { return [] }
}

function storeCustomPresets(list) {
  localStorage.setItem('muxer-customPresets', JSON.stringify(list))
  renderCustomPresets()
}

function snapshotEncodeOpts() {
  const data = { selects: {}, checks: {}, radios: {} }
  for (const id of ENCODE_SELECTS) {
    const el = document.getElementById(id)
    if (el) data.selects[id] = el.value
  }
  for (const id of ENCODE_CHECKS) {
    const el = document.getElementById(id)
    if (el) data.checks[id] = el.checked
  }
  for (const name of ENCODE_RADIOS) {
    const el = document.querySelector(`input[name="${name}"]:checked`)
    if (el) data.radios[name] = el.value
  }
  return data
}

function applyEncodeSnapshot(data) {
  if (!data) return
  for (const [id, v] of Object.entries(data.selects || {})) {
    const el = document.getElementById(id)
    if (el && [...el.options].some(o => o.value === v)) el.value = v
  }
  for (const [id, v] of Object.entries(data.checks || {})) {
    const el = document.getElementById(id)
    if (el) el.checked = !!v
  }
  for (const [name, v] of Object.entries(data.radios || {})) {
    const el = document.querySelector(`input[name="${name}"][value="${v}"]`)
    if (el) el.checked = true
  }
  try { window.onModeChange?.(); window.onFormatChange?.(); window.onCodecChange?.() } catch (_) {}
  saveEncodeOpts()
}

function renderCustomPresets() {
  const box = document.getElementById('customPresetList')
  if (!box) return
  const list = loadCustomPresets()
  if (list.length === 0) {
    box.innerHTML = ''
    return
  }
  box.innerHTML = list.map((p, i) =>
    `<button class="preset-btn" onclick="applyCustomPreset(${i})" oncontextmenu="deleteCustomPreset(${i});return false" title="左键应用 · 右键删除">${escapeHtml(p.name)}</button>`
  ).join('')
}

window.saveCustomPreset = function () {
  const name = (prompt('方案名称：') || '').trim()
  if (!name) return
  const list = loadCustomPresets()
  const idx = list.findIndex(p => p.name === name)
  const entry = { name, opts: snapshotEncodeOpts() }
  if (idx >= 0) {
    if (!confirm(`已存在同名方案「${name}」，覆盖吗？`)) return
    list[idx] = entry
  } else {
    list.push(entry)
  }
  storeCustomPresets(list)
  toast(`已保存方案「${name}」`, 'ok')
}

window.applyCustomPreset = function (i) {
  const p = loadCustomPresets()[i]
  if (!p) return
  document.querySelectorAll('.preset-btn').forEach(b => b.classList.remove('active'))
  applyEncodeSnapshot(p.opts)
  toast(`已应用方案「${p.name}」`, 'ok')
}

window.deleteCustomPreset = function (i) {
  const list = loadCustomPresets()
  const p = list[i]
  if (!p) return
  if (!confirm(`删除方案「${p.name}」？`)) return
  list.splice(i, 1)
  storeCustomPresets(list)
  toast('已删除', 'ok')
}

window.exportCustomPresets = function () {
  const list = loadCustomPresets()
  if (list.length === 0) { toast('暂无自定义方案', 'warn'); return }
  const blob = new Blob([JSON.stringify(list, null, 2)], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = 'Muxer方案.json'
  a.click()
  setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  toast(`已导出 ${list.length} 个方案`, 'ok')
}

window.importCustomPresets = function () {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = '.json,application/json'
  input.onchange = () => {
    const f = input.files?.[0]
    if (!f) return
    const reader = new FileReader()
    reader.onload = () => {
      let incoming
      try { incoming = JSON.parse(String(reader.result)) } catch (_) { toast('文件不是合法 JSON', 'error'); return }
      if (!Array.isArray(incoming)) { toast('文件格式不正确', 'error'); return }
      const valid = incoming.filter(p => p && typeof p.name === 'string' && p.opts)
      if (valid.length === 0) { toast('文件中没有可用方案', 'warn'); return }
      const list = loadCustomPresets()
      for (const p of valid) {
        const idx = list.findIndex(x => x.name === p.name)
        if (idx >= 0) list[idx] = p; else list.push(p)
      }
      storeCustomPresets(list)
      toast(`已导入 ${valid.length} 个方案`, 'ok')
    }
    reader.readAsText(f)
  }
  input.click()
}


function now() { return new Date().toTimeString().slice(0, 8) }

window.openLink = function (url) { BrowserOpenURL(url) }

window.copyCmd = function (text) {
  navigator.clipboard.writeText(text).then(() => {
    toast('已复制: ' + text)
  }).catch(() => {
    toast('复制失败，请手动复制', 'warn')
  })
}


let _toastTimer = null
function toast(msg, type = 'warn') {
  const el = document.getElementById('toast')
  el.textContent = msg
  el.className = `show toast-${type}`
  clearTimeout(_toastTimer)
  _toastTimer = setTimeout(() => { el.className = '' }, 2800)
}


let _subOriginalPath = ''
let _subMeta = []

function renderSubTable(entries) {
  const body = document.getElementById('subTableBody')
  const frag = document.createDocumentFragment()
  entries.forEach((e, i) => {
    const row = document.createElement('div')
    row.className = 'sub-row'
    row.dataset.i = i
    row.innerHTML = `
      <span class="sub-col-idx">${e.index}</span>
      <input  class="sub-input"    type="text" value="${escapeHtml(e.start)}" placeholder="start" />
      <input  class="sub-input"    type="text" value="${escapeHtml(e.end)}"   placeholder="end"   />
      <textarea class="sub-textarea" rows="1">${escapeHtml(e.text)}</textarea>
      <button class="sub-del-btn" onclick="deleteSubRow(this)" title="删除">✕</button>`
    const ta = row.querySelector('.sub-textarea')
    ta.addEventListener('input', () => autoResizeTa(ta))
    frag.appendChild(row)
  })
  body.innerHTML = ''
  body.appendChild(frag)
  body.querySelectorAll('.sub-textarea').forEach(autoResizeTa)
}

function autoResizeTa(ta) {
  ta.style.height = 'auto'
  ta.style.height = ta.scrollHeight + 'px'
}

function collectEntries() {
  const rows = document.getElementById('subTableBody').querySelectorAll('.sub-row')
  const entries = []
  rows.forEach((row, i) => {
    const inputs = row.querySelectorAll('.sub-input')
    const ta     = row.querySelector('.sub-textarea')
    const meta   = _subMeta[i] || { format: 'srt', assParts: [] }
    entries.push({
      index:    i + 1,
      start:    inputs[0].value.trim(),
      end:      inputs[1].value.trim(),
      text:     ta.value,
      format:   meta.format,
      assParts: meta.assParts,
    })
  })
  return entries
}

window.saveSubtitle = async function () {
  if (!_subOriginalPath) return
  const entries = collectEntries()
  const ok = await SaveSubtitle(_subOriginalPath, _subOriginalPath, entries)
  toast(ok ? '字幕已保存' : '保存失败', ok ? 'ok' : 'warn')
}

window.saveSubtitleAs = async function () {
  if (!_subOriginalPath) return
  const defaultName = _subOriginalPath.split('\\').pop()
  const outPath = await SelectSaveFile('另存为', defaultName)
  if (!outPath) return
  const entries = collectEntries()
  const ok = await SaveSubtitle(_subOriginalPath, outPath, entries)
  toast(ok ? '字幕已另存为: ' + outPath.split('\\').pop() : '保存失败', ok ? 'ok' : 'warn')
}

window.deleteSubRow = function (btn) {
  const row = btn.closest('.sub-row')
  const i   = parseInt(row.dataset.i, 10)
  _subMeta.splice(i, 1)
  row.remove()
  document.getElementById('subTableBody')
    .querySelectorAll('.sub-row')
    .forEach((r, idx) => {
      r.dataset.i = idx
      r.querySelector('.sub-col-idx').textContent = idx + 1
    })
  const cnt = document.getElementById('subTableBody').querySelectorAll('.sub-row').length
  document.getElementById('subStats').textContent =
    document.getElementById('subStats').textContent.replace(/共 \d+ 条字幕/, `共 ${cnt} 条字幕`)
}


window.openFindBar = function () {
  document.getElementById('subFindBar').classList.remove('hidden')
  document.getElementById('subFindInput').focus()
}
window.closeFindBar = function () {
  document.getElementById('subFindBar').classList.add('hidden')
  document.querySelectorAll('.sub-row.find-highlight').forEach(r => r.classList.remove('find-highlight'))
}

let _findIdx = -1

window.findNextSub = function () {
  const keyword = document.getElementById('subFindInput').value
  if (!keyword) return
  const cs = document.getElementById('subFindCase').checked
  const rows = [...document.getElementById('subTableBody').querySelectorAll('.sub-row')]
  if (!rows.length) return
  rows.forEach(r => r.classList.remove('find-highlight'))
  _findIdx = (_findIdx + 1) % rows.length
  for (let i = 0; i < rows.length; i++) {
    const idx = (_findIdx + i) % rows.length
    const ta  = rows[idx].querySelector('.sub-textarea')
    const haystack = cs ? ta.value : ta.value.toLowerCase()
    const needle   = cs ? keyword  : keyword.toLowerCase()
    if (haystack.includes(needle)) {
      _findIdx = idx
      rows[idx].classList.add('find-highlight')
      rows[idx].scrollIntoView({ block: 'nearest' })
      ta.focus()
      return
    }
  }
  toast('未找到匹配内容', 'warn')
}

window.replaceAllSub = function () {
  const keyword = document.getElementById('subFindInput').value
  const replacement = document.getElementById('subReplaceInput').value
  if (!keyword) { toast('请输入搜索文本', 'warn'); return }
  const cs = document.getElementById('subFindCase').checked
  let count = 0
  document.getElementById('subTableBody').querySelectorAll('.sub-textarea').forEach(ta => {
    if (cs) {
      if (ta.value.includes(keyword)) {
        ta.value = ta.value.replaceAll(keyword, replacement)
        count++
        autoResizeTa(ta)
      }
    } else {
      const re = new RegExp(keyword.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi')
      if (re.test(ta.value)) {
        ta.value = ta.value.replace(re, replacement)
        count++
        autoResizeTa(ta)
      }
    }
  })
  toast(count > 0 ? `已替换 ${count} 处` : '未找到匹配内容', count > 0 ? 'ok' : 'warn')
}


window.selectBatchSubDir = async function () {
  const d = await SelectDirectory('选择字幕文件所在目录')
  if (d) document.getElementById('batchSubDir').value = d
}
window.batchConvertSubs = async function () {
  const dir = document.getElementById('batchSubDir').value.trim()
  if (!dir) { toast('请先选择目录', 'warn'); return }
  const fmt = document.querySelector('input[name="batchSubFmt"]:checked').value
  await runJob('批量字幕转换', () => BatchConvertSubtitles(dir, fmt))
}


let _concatFiles = []

function renderConcatList() {
  const el = document.getElementById('concatList')
  const summary = document.getElementById('concatSummary')
  if (summary) {
    summary.textContent = _concatFiles.length === 0
      ? '暂无文件'
      : `已添加 ${_concatFiles.length} 个文件（点击展开 / 折叠）`
  }
  if (_concatFiles.length === 0) {
    el.innerHTML = '<div class="concat-empty">暂无文件，点击"添加文件"或将视频文件拖入此处</div>'
    return
  }
  el.innerHTML = _concatFiles.map((f, i) => `
    <div class="concat-row" draggable="true" data-i="${i}"
         ondragstart="concatDragStart(event)"
         ondragover="concatDragOver(event)"
         ondrop="concatDrop(event)">
      <span class="concat-handle" title="拖拽排序">⠿</span>
      <span class="concat-idx">${i + 1}</span>
      <span class="concat-name" title="${escapeHtml(f.path)}">${escapeHtml(f.name)}</span>
      <button class="sub-del-btn" onclick="concatRemove(${i})" title="移除">✕</button>
    </div>`).join('')
}

window.concatToggleList = function () {
  const list = document.getElementById('concatList')
  const arrow = document.getElementById('concatArrow')
  if (!list) return
  const collapsed = list.classList.toggle('collapsed')
  if (arrow) arrow.classList.toggle('expanded', !collapsed)
}

let _dragSrcIdx = null

window.concatDragStart = function (e) {
  _dragSrcIdx = parseInt(e.currentTarget.dataset.i, 10)
  e.dataTransfer.effectAllowed = 'move'
}
window.concatDragOver = function (e) {
  e.preventDefault()
  e.dataTransfer.dropEffect = 'move'
}
window.concatDrop = function (e) {
  e.preventDefault()
  const dst = parseInt(e.currentTarget.dataset.i, 10)
  if (_dragSrcIdx === null || _dragSrcIdx === dst) return
  const moved = _concatFiles.splice(_dragSrcIdx, 1)[0]
  _concatFiles.splice(dst, 0, moved)
  _dragSrcIdx = null
  renderConcatList()
}

window.concatAddFiles = async function () {
  const f = await SelectVideoFile('添加视频文件')
  if (!f) return
  _concatFiles.push({ path: f, name: f.split('\\').pop() })
  renderConcatList()
}

window.concatAddDir = async function () {
  const dir = await SelectDirectory('选择视频目录（自动按剧集编号排序）')
  if (!dir) return
  const files = await ScanConcatDir(dir)
  if (!files || files.length === 0) { toast('该目录下未找到视频文件', 'warn'); return }
  for (const f of files) {
    _concatFiles.push({ path: f, name: f.split('\\').pop() })
  }
  toast(`已添加 ${files.length} 个文件（按剧集编号排序）`, 'ok')
  renderConcatList()
}

window.concatRemove = function (i) {
  _concatFiles.splice(i, 1)
  renderConcatList()
}

window.concatClear = function () {
  _concatFiles = []
  renderConcatList()
}

window.concatVideos = async function () {
  if (_concatFiles.length < 2) { toast('请至少添加两个视频文件', 'warn'); return }
  const out = document.getElementById('outputDir').value.trim()
  if (!out) { toast('请先在封装页设置输出目录', 'warn'); return }
  const copyMode  = document.querySelector('input[name="concatMode"]:checked').value === 'copy'
  const outName   = document.getElementById('concatOutName').value.trim()
  const headAd    = document.getElementById('concatHeadAd').checked
  const tailAd    = document.getElementById('concatTailAd').checked
  const adText    = document.getElementById('concatAdText').value.trim()
  const dynAd     = document.getElementById('concatDynAd').checked
  const paths     = _concatFiles.map(f => f.path)
  await runJob('视频合并', () => ConcatVideos(paths, out, outName, copyMode, headAd, tailAd, adText, dynAd))
}


window.selectMergeSub = async function (n) {
  const f = await SelectSubtitleFile('选择字幕文件 ' + n)
  if (f) document.getElementById('mergeSub' + n).value = f
}

window.mergeSubtitles = async function () {
  const f1  = document.getElementById('mergeSub1').value.trim()
  const f2  = document.getElementById('mergeSub2').value.trim()
  const out = document.getElementById('outputDir').value.trim()
  if (!f1 || !f2) { toast('请先选择两个字幕文件', 'warn'); return }
  if (!out)       { toast('请先在封装页设置输出目录', 'warn'); return }
  const topFile = parseInt(document.querySelector('input[name="mergeTop"]:checked').value, 10)
  const fmt     = document.querySelector('input[name="mergeFmt"]:checked').value
  await runJob('双语字幕合并', () => MergeSubtitles(f1, f2, out, topFile, fmt))
}


const TRANS_KEY_STORAGE = 'muxer-trans-keys'

function loadTransApiKeys() {
  try {
    const s = JSON.parse(localStorage.getItem(TRANS_KEY_STORAGE) || '{}')
    if (s.deepseekKey)  document.getElementById('transDeepSeekKey').value = s.deepseekKey
    if (s.deeplKey)     document.getElementById('transDeepLKey').value    = s.deeplKey
    if (s.baiduAppID)   document.getElementById('transBaiduAppID').value  = s.baiduAppID
    if (s.baiduKey)     document.getElementById('transBaiduKey').value     = s.baiduKey
    if (s.tencentID)    document.getElementById('transTencentID').value    = s.tencentID
    if (s.tencentKey)   document.getElementById('transTencentKey').value   = s.tencentKey
  } catch (_) {}
}

function saveTransApiKeys() {
  localStorage.setItem(TRANS_KEY_STORAGE, JSON.stringify({
    deepseekKey: document.getElementById('transDeepSeekKey').value,
    deeplKey:   document.getElementById('transDeepLKey').value,
    baiduAppID: document.getElementById('transBaiduAppID').value,
    baiduKey:   document.getElementById('transBaiduKey').value,
    tencentID:  document.getElementById('transTencentID').value,
    tencentKey: document.getElementById('transTencentKey').value,
  }))
}


window.loadAIConfigUI = async function () {
  let cfg = { aiProvider: _cfgAIProvider || 'deepseek', hasDeepSeekKey: _cfgHasDeepSeekKey, hasZhipuKey: _cfgHasZhipuKey }
  try { cfg = await GetAIConfig() } catch (_) {}
  _cfgAIProvider = (cfg.aiProvider === 'zhipu') ? 'zhipu' : 'deepseek'
  _cfgHasDeepSeekKey = !!cfg.hasDeepSeekKey
  _cfgHasZhipuKey = !!cfg.hasZhipuKey
  const radio = document.querySelector(`input[name="aiProvider"][value="${_cfgAIProvider}"]`)
  if (radio) radio.checked = true
  window.onAIProviderChange()
}

window.onAIProviderChange = function () {
  const p = document.querySelector('input[name="aiProvider"]:checked')?.value || 'deepseek'
  _cfgAIProvider = p === 'zhipu' ? 'zhipu' : 'deepseek'
  const dsRow = document.getElementById('aiKeyDeepSeek')
  const zpRow = document.getElementById('aiKeyZhipu')
  if (dsRow) dsRow.classList.toggle('hidden', _cfgAIProvider !== 'deepseek' || _cfgHasDeepSeekKey)
  if (zpRow) zpRow.classList.toggle('hidden', _cfgAIProvider !== 'zhipu' || _cfgHasZhipuKey)
}

window.saveAIConfig = async function () {
  const p = document.querySelector('input[name="aiProvider"]:checked')?.value || 'deepseek'
  const deepseekKey = document.getElementById('aiDeepSeekKey')?.value.trim() || ''
  const zhipuKey = document.getElementById('aiZhipuKey')?.value.trim() || ''
  if (p === 'zhipu' && !zhipuKey && !_cfgHasZhipuKey) {
    toast('请填写 GLM API Key', 'warn'); return
  }
  if (p === 'deepseek' && !deepseekKey && !_cfgHasDeepSeekKey) {
    toast('请填写 DeepSeek API Key', 'warn'); return
  }
  const ok = await runJob('保存 AI 配置', () => SaveAIConfig({
    aiProvider: p === 'zhipu' ? 'zhipu' : 'deepseek',
    deepseekKey,
    zhipuKey,
  }), { checkBusy: false })
  if (ok) {
    toast('AI 配置已保存', 'ok')
    const ds = document.getElementById('aiDeepSeekKey'); if (ds) ds.value = ''
    const zp = document.getElementById('aiZhipuKey'); if (zp) zp.value = ''
    await applyConfigStatus()
  }
}

window.onTransProviderChange = function () {
  const p = document.querySelector('input[name="transProvider"]:checked').value
  document.getElementById('transKeyDeepSeek').classList.toggle('hidden', p !== 'deepseek' || _cfgHasDeepSeekKey)
  document.getElementById('transKeyDeepL').classList.toggle('hidden',   p !== 'deepl')
  document.getElementById('transKeyBaidu').classList.toggle('hidden',   p !== 'baidu')
  document.getElementById('transKeyTencent').classList.toggle('hidden', p !== 'tencent')
}

window.onKeepSubStyleChange = function (el) {
  localStorage.setItem('muxer-keepSubStyle', el.checked ? '1' : '0')
}

window.onAsrAiCorrectChange = function (el) {
  localStorage.setItem('muxer-asrAiCorrect', el.checked ? '1' : '0')
}

window.selectTransSubFile = async function () {
  const f = await SelectSubtitleFile('选择字幕文件')
  if (f) document.getElementById('transSubFile').value = f
}

window.openDeepLLink = function () {
  BrowserOpenURL('https://www.deepl.com/zh/pro-api')
}

window.translateSubtitle = async function () {
  const file = document.getElementById('transSubFile').value.trim()
  const out  = document.getElementById('outputDir').value.trim()
  if (!file) { toast('请先选择字幕文件', 'warn'); return }
  if (!out)  { toast('请先在封装页设置输出目录', 'warn'); return }

  saveTransApiKeys()

  const provider = document.querySelector('input[name="transProvider"]:checked').value
  const lang     = document.getElementById('transTargetLang').value
  const mode     = document.querySelector('input[name="transMode"]:checked').value
  const forceSRT = document.getElementById('transForceSRT').checked

  let key1 = '', key2 = ''
  if (provider === 'deepseek') {
    key1 = document.getElementById('transDeepSeekKey').value.trim()
  } else if (provider === 'deepl') {
    key1 = document.getElementById('transDeepLKey').value.trim()
  } else if (provider === 'baidu') {
    key1 = document.getElementById('transBaiduAppID').value.trim()
    key2 = document.getElementById('transBaiduKey').value.trim()
  } else {
    key1 = document.getElementById('transTencentID').value.trim()
    key2 = document.getElementById('transTencentKey').value.trim()
  }
  if (!key1 && provider !== 'deepseek') { toast('请填写 API Key', 'warn'); return }

  await runJob('字幕翻译', () => TranslateSubtitle(file, out, { provider, key1, key2, targetLang: lang, mode, forceSRT }))
}


window.selectQualitySubFile = async function () {
  const f = await SelectSubtitleFile('选择字幕文件')
  if (f) document.getElementById('qualitySubFile').value = f
}

window.selectQualitySubDir = async function () {
  const d = await SelectDirectory('选择字幕文件所在目录（批量处理）')
  if (d) document.getElementById('qualitySubFile').value = d
}
window.checkSubQuality = async function () {
  const f = document.getElementById('qualitySubFile').value.trim()
  const out = document.getElementById('outputDir').value.trim()
  if (!f) { toast('请先选择字幕文件', 'warn'); return }
  if (!out) { toast('请先在封装页设置输出目录', 'warn'); return }
  await runJob('字幕质量检查', () => CheckSubtitleQuality(f, out))
}

window.convertToCleanSRT = async function () {
  const f = document.getElementById('qualitySubFile').value.trim()
  const out = document.getElementById('outputDir').value.trim()
  if (!f) { toast('请先选择字幕文件', 'warn'); return }
  if (!out) { toast('请先在封装页设置输出目录', 'warn'); return }
  await runJob('转简洁 SRT', () => ConvertToCleanSRT(f, out))
}

window.fixSubWithAI = async function () {
  const f = document.getElementById('qualitySubFile').value.trim()
  const out = document.getElementById('outputDir').value.trim()
  if (!f) { toast('请先选择字幕文件', 'warn'); return }
  if (!out) { toast('请先在封装页设置输出目录', 'warn'); return }
  await runJob('AI 修复字幕', () => FixSubtitleWithAI(f, out))
}


window.selectTrimFile = async function () {
  const f = await SelectVideoFile('选择视频文件')
  if (f) document.getElementById('trimFile').value = f
}
window.trimVideo = async function () {
  const f = document.getElementById('trimFile').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!f) { toast('请先选择视频文件', 'warn'); return }
  if (!outputDir) { toast('请在封装页设置输出目录', 'warn'); return }
  const start = document.getElementById('trimStart').value.trim() || '00:00:00'
  const end   = document.getElementById('trimEnd').value.trim()   || '00:01:00'
  const copy  = document.querySelector('input[name="trimMode"]:checked').value === 'copy'
  await runJob('视频裁剪', () => TrimVideo(f, outputDir, start, end, copy))
}


window.selectThumbFile = async function () {
  const f = await SelectVideoFile('选择视频文件')
  if (f) document.getElementById('thumbFile').value = f
}
window.generateThumbs = async function () {
  const f = document.getElementById('thumbFile').value.trim()
  const outputDir = document.getElementById('outputDir').value.trim()
  if (!f) { toast('请先选择视频文件', 'warn'); return }
  if (!outputDir) { toast('请在封装页设置输出目录', 'warn'); return }
  const grid = document.querySelector('input[name="thumbGrid"]:checked').value
  const [cols, rows] = grid.split('x').map(Number)
  await runJob('生成缩略图', () => GenerateThumbnailGrid(f, outputDir, cols, rows))
}

window.addSubtitleRow = function () {
  const body = document.getElementById('subTableBody')
  const rows = body.querySelectorAll('.sub-row')
  const idx  = rows.length
  let defStart = '00:00:00,000'
  if (rows.length > 0) {
    const lastEnd = rows[rows.length - 1].querySelectorAll('.sub-input')[1].value
    defStart = lastEnd
  }
  const format = _subMeta.length > 0 ? _subMeta[0].format : 'srt'
  const defEnd = defStart

  const row = document.createElement('div')
  row.className = 'sub-row'
  row.dataset.i = idx
  row.innerHTML = `
    <span class="sub-col-idx">${idx + 1}</span>
    <input  class="sub-input"    type="text" value="${escapeHtml(defStart)}" placeholder="start" />
    <input  class="sub-input"    type="text" value="${escapeHtml(defEnd)}"   placeholder="end"   />
    <textarea class="sub-textarea" rows="1"></textarea>
    <button class="sub-del-btn" onclick="deleteSubRow(this)" title="删除">✕</button>`
  const ta = row.querySelector('.sub-textarea')
  ta.addEventListener('input', () => autoResizeTa(ta))
  body.appendChild(row)
  _subMeta.push({ format, assParts: [] })
  ta.focus()
  body.scrollTop = body.scrollHeight
}


let _assStyles     = []
let _assEditIdx    = -1
let _assFilePath   = ''

window.assColorToHex = function assColorToHex(ass) {
  const s = (ass || '&H00FFFFFF').replace(/&[Hh]/g, '').padStart(8, '0')
  const rr = s.slice(6, 8), gg = s.slice(4, 6), bb = s.slice(2, 4)
  return '#' + rr + gg + bb
}
window.hexToAssColor = function hexToAssColor(hex) {
  const r = hex.slice(1, 3), g = hex.slice(3, 5), b = hex.slice(5, 7)
  return `&H00${b}${g}${r}`.toUpperCase()
}

function assUpdatePreview() {
  if (_assEditIdx < 0) return
  const s    = _assStyles[_assEditIdx]
  const text = document.getElementById('assPreviewText')
  const box  = document.getElementById('assPreviewBox')
  const fg   = assColorToHex(s.primaryColour || '&H00FFFFFF')
  const ol   = assColorToHex(s.outlineColour || '&H00000000')
  const outlinePx = Math.max(0, (s.outline || 2)) * 1.2

  text.style.fontFamily  = s.fontname || 'Arial'
  text.style.fontSize    = Math.min(Math.max(s.fontsize || 20, 10), 72) + 'px'
  text.style.color       = fg
  text.style.fontWeight  = s.bold   ? '700' : '400'
  text.style.fontStyle   = s.italic ? 'italic' : 'normal'
  text.style.textDecoration = [s.underline ? 'underline' : '', s.strikeOut ? 'line-through' : ''].filter(Boolean).join(' ') || 'none'
  text.style.textShadow  = outlinePx > 0
    ? `${ol} 0 0 ${outlinePx}px, ${ol} 0 ${outlinePx}px 0, ${ol} ${outlinePx}px 0 0, ${ol} 0 -${outlinePx}px 0, ${ol} -${outlinePx}px 0 0`
    : 'none'
  text.style.letterSpacing = (s.spacing || 0) + 'px'
  const al = s.alignment || 2
  box.style.justifyContent = [1, 4, 7].includes(al) ? 'flex-start' : [3, 6, 9].includes(al) ? 'flex-end' : 'center'
  box.style.alignItems     = al >= 7 ? 'flex-start' : al >= 4 ? 'center' : 'flex-end'
}

const _assAlignFlex = {
  7: ['flex-start', 'flex-start'], 8: ['center', 'flex-start'], 9: ['flex-end', 'flex-start'],
  4: ['flex-start', 'center'], 5: ['center', 'center'], 6: ['flex-end', 'center'],
  1: ['flex-start', 'flex-end'], 2: ['center', 'flex-end'], 3: ['flex-end', 'flex-end'],
}
const _assAlignTitle = { 7: '左上', 8: '上沿', 9: '右上', 4: '左侧', 5: '正中', 6: '右侧', 1: '左下', 2: '下沿', 3: '右下' }

function assAlignMatrixHTML(s) {
  const order = [7, 8, 9, 4, 5, 6, 1, 2, 3]
  return order.map((n) => {
    const [jc, ai] = _assAlignFlex[n]
    const active = s.alignment === n ? 'active' : ''
    return `<button type="button" class="ass-align-cell ${active}" onclick="assSetAlign(${n})" title="${_assAlignTitle[n]}（ASS 对齐 ${n}）" style="justify-content:${jc};align-items:${ai}"><span class="ass-align-dot"></span></button>`
  }).join('')
}

function assRenderList() {
  const ul = document.getElementById('assStyleList')
  if (!_assStyles.length) {
    ul.innerHTML = '<div class="ass-list-empty">暂无样式定义</div>'
    return
  }
  ul.innerHTML = _assStyles.map((s, i) => `
    <div class="ass-style-item ${i === _assEditIdx ? 'active' : ''}" onclick="assSelectStyle(${i})">
      <div class="ass-style-item-top">
        <span class="ass-style-name">${escapeHtml(s.name)}</span>
        <button class="btn btn-ghost btn-sm ass-del-btn" onclick="event.stopPropagation();assDeleteStyle(${i})" title="删除此样式">✕</button>
      </div>
      <span class="ass-style-font">${escapeHtml(s.fontname)} · ${s.fontsize | 0}px</span>
    </div>`).join('')
}

function assRenderForm(i) {
  if (i < 0 || i >= _assStyles.length) {
    document.getElementById('assStyleForm').innerHTML = '<div class="ass-form-empty">在左侧选择一个样式以编辑其参数</div>'
    document.getElementById('assEditingName').textContent = '—'
    return
  }
  const s = _assStyles[i]
  document.getElementById('assEditingName').textContent = s.name

  document.getElementById('assStyleForm').innerHTML = `
    <div class="ass-form-section">
      <div class="ass-form-section-title">基础</div>
      <div class="ass-form-section-body">
        <label class="ass-label">样式名称</label>
        <input class="input ass-input" id="assFName" type="text" value="${escapeHtml(s.name)}" oninput="assFieldChange('name',this.value)" />
        <label class="ass-label">字体</label>
        <input class="input ass-input" id="assFFont" type="text" value="${escapeHtml(s.fontname)}" oninput="assFieldChange('fontname',this.value)" />
        <label class="ass-label">字号</label>
        <input class="input ass-input" id="assFSize" type="number" min="6" max="200" step="1" value="${s.fontsize | 0}" oninput="assFieldChange('fontsize',+this.value)" />
      </div>
    </div>
    <div class="ass-form-section">
      <div class="ass-form-section-title">颜色</div>
      <div class="ass-form-section-body">
        <label class="ass-label">主颜色</label>
        <div class="ass-color-row">
          <input type="color" class="ass-color-input" value="${assColorToHex(s.primaryColour)}"
                 oninput="assFieldChange('primaryColour',hexToAssColor(this.value))" />
          <input class="input ass-input" type="text" value="${escapeHtml(s.primaryColour)}"
                 oninput="this.previousElementSibling.value=assColorToHex(this.value);assFieldChange('primaryColour',this.value)" />
        </div>
        <label class="ass-label">描边色</label>
        <div class="ass-color-row">
          <input type="color" class="ass-color-input" value="${assColorToHex(s.outlineColour)}"
                 oninput="assFieldChange('outlineColour',hexToAssColor(this.value))" />
          <input class="input ass-input" type="text" value="${escapeHtml(s.outlineColour)}"
                 oninput="this.previousElementSibling.value=assColorToHex(this.value);assFieldChange('outlineColour',this.value)" />
        </div>
        <label class="ass-label">背景色</label>
        <div class="ass-color-row">
          <input type="color" class="ass-color-input" value="${assColorToHex(s.backColour)}"
                 oninput="assFieldChange('backColour',hexToAssColor(this.value))" />
          <input class="input ass-input" type="text" value="${escapeHtml(s.backColour)}"
                 oninput="this.previousElementSibling.value=assColorToHex(this.value);assFieldChange('backColour',this.value)" />
        </div>
      </div>
    </div>
    <div class="ass-form-section">
      <div class="ass-form-section-title">笔画与边距</div>
      <div class="ass-form-section-body">
        <label class="ass-label">描边宽度</label>
        <input class="input ass-input" type="number" min="0" max="10" step="0.5" value="${s.outline}" oninput="assFieldChange('outline',+this.value)" />
        <label class="ass-label">阴影深度</label>
        <input class="input ass-input" type="number" min="0" max="10" step="0.5" value="${s.shadow}" oninput="assFieldChange('shadow',+this.value)" />
        <label class="ass-label">字间距</label>
        <input class="input ass-input" type="number" min="0" max="20" step="0.5" value="${s.spacing}" oninput="assFieldChange('spacing',+this.value)" />
        <label class="ass-label">缩放 %</label>
        <div class="ass-margin-row ass-margin-row--2">
          <input class="input ass-input" type="number" min="10" max="400" step="1" value="${s.scaleX | 0}" oninput="assFieldChange('scaleX',+this.value)" title="横向 ScaleX" placeholder="X" />
          <input class="input ass-input" type="number" min="10" max="400" step="1" value="${s.scaleY | 0}" oninput="assFieldChange('scaleY',+this.value)" title="纵向 ScaleY" placeholder="Y" />
        </div>
        <label class="ass-label">边距 L/R/V</label>
        <div class="ass-margin-row">
          <input class="input ass-input" type="number" min="0" step="1" value="${s.marginL}" oninput="assFieldChange('marginL',+this.value)" title="左边距" />
          <input class="input ass-input" type="number" min="0" step="1" value="${s.marginR}" oninput="assFieldChange('marginR',+this.value)" title="右边距" />
          <input class="input ass-input" type="number" min="0" step="1" value="${s.marginV}" oninput="assFieldChange('marginV',+this.value)" title="垂直边距" />
        </div>
      </div>
    </div>
    <div class="ass-form-section">
      <div class="ass-form-section-title">版式</div>
      <div class="ass-form-section-body">
        <label class="ass-label">修饰</label>
        <div class="ass-toggles">
          <label class="checkbox-label"><input type="checkbox" ${s.bold ? 'checked' : ''} onchange="assFieldChange('bold',this.checked?-1:0)" /><span>粗体</span></label>
          <label class="checkbox-label"><input type="checkbox" ${s.italic ? 'checked' : ''} onchange="assFieldChange('italic',this.checked?-1:0)" /><span>斜体</span></label>
          <label class="checkbox-label"><input type="checkbox" ${s.underline ? 'checked' : ''} onchange="assFieldChange('underline',this.checked?-1:0)" /><span>下划线</span></label>
          <label class="checkbox-label"><input type="checkbox" ${s.strikeOut ? 'checked' : ''} onchange="assFieldChange('strikeOut',this.checked?-1:0)" /><span>删除线</span></label>
        </div>
        <label class="ass-label">对齐</label>
        <div>
          <div class="ass-align-hint">九宫格对应 ASS 对齐编号，悬停可看说明</div>
          <div class="ass-align-matrix">${assAlignMatrixHTML(s)}</div>
        </div>
        <label class="ass-label">边框样式</label>
        <select class="select ass-input" onchange="assFieldChange('borderStyle',+this.value)">
          <option value="1" ${s.borderStyle === 1 ? 'selected' : ''}>描边 + 阴影</option>
          <option value="3" ${s.borderStyle === 3 ? 'selected' : ''}>不透明底框</option>
        </select>
      </div>
    </div>`
  assUpdatePreview()
}

window.assSelectStyle = function (i) {
  _assEditIdx = i
  assRenderList()
  assRenderForm(i)
}

window.assFieldChange = function (key, val) {
  if (_assEditIdx < 0) return
  _assStyles[_assEditIdx][key] = val
  assUpdatePreview()
}

window.assSetAlign = function (n) {
  if (_assEditIdx < 0) return
  _assStyles[_assEditIdx].alignment = n
  assRenderForm(_assEditIdx)
}

window.assDeleteStyle = function (i) {
  _assStyles.splice(i, 1)
  if (_assEditIdx >= _assStyles.length) _assEditIdx = _assStyles.length - 1
  assRenderList()
  assRenderForm(_assEditIdx)
}

window.assAddStyle = function () {
  const base = _assStyles[_assEditIdx] || {
    name: 'Style', fontname: 'Arial', fontsize: 20,
    primaryColour: '&H00FFFFFF', secondaryColour: '&H000000FF',
    outlineColour: '&H00000000', backColour: '&H00000000',
    bold: 0, italic: 0, underline: 0, strikeOut: 0,
    scaleX: 100, scaleY: 100, spacing: 0, angle: 0,
    borderStyle: 1, outline: 2, shadow: 2, alignment: 2,
    marginL: 10, marginR: 10, marginV: 10, encoding: 1,
  }
  const newStyle = { ...base, name: base.name + '_copy' }
  _assStyles.push(newStyle)
  _assEditIdx = _assStyles.length - 1
  assRenderList()
  assRenderForm(_assEditIdx)
}

window.assStyleSave = async function () {
  if (!_assFilePath) { toast('请先加载 ASS 文件', 'warn'); return }
  if (!_assStyles.length) { toast('样式列表为空，请至少保留一个样式', 'warn'); return }
  const ok = await WriteASSStyles(_assFilePath, '', _assStyles)
  if (ok) toast('ASS 样式已保存')
}

window.toggleAssStylePanel = function () {
  const panel = document.getElementById('assStylePanel')
  panel.classList.toggle('hidden')
}

function assOnFileLoaded(filePath, styles) {
  _assFilePath = filePath
  _assStyles   = styles || []
  _assEditIdx  = _assStyles.length > 0 ? 0 : -1
  assRenderList()
  assRenderForm(_assEditIdx)
  document.getElementById('assStyleBtn').classList.remove('hidden')
  document.getElementById('assStylePanel').classList.remove('hidden')
}

window.selectSubEditFile = async function () {
  const f = await SelectSubtitleFile('选择字幕文件')
  if (!f) return
  const ext = f.split('.').pop().toLowerCase()
  if (ext === 'sup') {
    document.getElementById('subEditFile').value = ''
    _subOriginalPath = ''
    toast('SUP/PGS 为图形字幕，无法编辑，仅可用于封装', 'warn')
    return
  }
  document.getElementById('subEditFile').value = f
  _subOriginalPath = f
  const entries = await LoadSubtitle(f)
  if (!entries || entries.length === 0) {
    toast('未能解析字幕文件', 'error')
    return
  }
  _subMeta = entries.map(e => ({ format: e.format, assParts: e.assParts || [] }))
  renderSubTable(entries)
  document.getElementById('subStats').textContent =
    `共 ${entries.length} 条字幕  ·  格式: ${entries[0].format.toUpperCase()}`
  document.getElementById('subStats').classList.remove('hidden')
  document.getElementById('subSaveBtn').disabled = false
  document.getElementById('subSaveAsBtn').disabled = false
  document.getElementById('subFooterBar').classList.remove('hidden')

  if (ext === 'ass' || ext === 'ssa') {
    const styles = await ReadASSStyles(f)
    assOnFileLoaded(f, styles)
  } else {
    document.getElementById('assStyleBtn').classList.add('hidden')
    document.getElementById('assStylePanel').classList.add('hidden')
  }
}


window.analyzeFilenames = async function () {
  const inputDir = document.getElementById('inputDir').value.trim()
  if (!inputDir) { appendLog(now(), 'warn', '请先选择输入目录'); return }

  const btn        = document.getElementById('analyzeNamesBtn')
  const panel      = document.getElementById('filenameAnalysisPanel')
  const tbody      = document.getElementById('analysisTableBody')
  const summary    = document.getElementById('analysisSummary')
  const issueTag   = document.getElementById('analysisIssueTag')
  const issueDetail = document.getElementById('analysisIssueDetail')

  btn.disabled    = true
  btn.textContent = '分析中…'

  panel.classList.remove('hidden')
  tbody.innerHTML = '<tr><td colspan="6" class="analysis-loading">AI 正在分析文件名，请稍候...</td></tr>'
  summary.textContent = ''
  issueTag.classList.add('hidden')
  issueDetail.classList.add('hidden')

  try {
    const result = await AnalyzeFilenames(inputDir)
    if (!result) {
      tbody.innerHTML = '<tr><td colspan="6" class="analysis-loading" style="color:#C42B2B">分析失败，请查看日志</td></tr>'
      return
    }

    summary.textContent = result.summary || ('分析完成：' + (result.items || []).length + ' 个文件')

    if (result.hasIssues) {
      issueTag.textContent = '存在命名问题'
      issueTag.classList.remove('hidden')
      if (result.issueDetail) {
        issueDetail.textContent = result.issueDetail
        issueDetail.classList.remove('hidden')
      }
    }

    const items = result.items || []
    if (items.length === 0) {
      tbody.innerHTML = '<tr><td colspan="6" class="analysis-loading">未找到文件</td></tr>'
      return
    }

    const confMap   = { high: '高', medium: '中', low: '低' }
    const confClass = { high: 'confidence-high', medium: 'confidence-medium', low: 'confidence-low' }

    tbody.innerHTML = items.map(item => {
      const subCell = item.subFile
        ? '<span title="' + escapeHtml(item.subFile) + '">' + escapeHtml(fnShort(item.subFile, 28)) + '</span>'
        : '<span class="analysis-no-sub">未匹配</span>'

      const epCell = item.episode
        ? '<span class="analysis-ep-tag">' + escapeHtml(item.episode) + '</span>'
        : '<span style="color:#999">—</span>'

      const cc     = confClass[item.confidence] || ''
      const cLabel = confMap[item.confidence]   || item.confidence || '—'

      const noteCell = item.note
        ? '<span style="color:#E65100" title="' + escapeHtml(item.note) + '">' + escapeHtml(item.note) + '</span>'
        : '—'

      const nameInput =
        '<input class="analysis-name-input"' +
        ' data-orig-video="' + escapeHtml(item.videoFile)      + '"' +
        ' data-orig-sub="'   + escapeHtml(item.subFile || '')  + '"' +
        ' value="'           + escapeHtml(item.suggestName)    + '"' +
        ' title="'           + escapeHtml(item.suggestName)    + '"' +
        ' />'

      return '<tr>' +
        '<td title="' + escapeHtml(item.videoFile) + '">' + escapeHtml(fnShort(item.videoFile, 30)) + '</td>' +
        '<td>' + subCell    + '</td>' +
        '<td>' + epCell     + '</td>' +
        '<td>' + nameInput  + '</td>' +
        '<td class="' + cc + '">' + cLabel + '</td>' +
        '<td>' + noteCell   + '</td>' +
        '</tr>'
    }).join('')

    const applyBtn = document.getElementById('applyRenameBtn')
    if (applyBtn) applyBtn.style.display = ''

  } catch (e) {
    tbody.innerHTML = '<tr><td colspan="6" class="analysis-loading" style="color:#C42B2B">调用失败: ' + escapeHtml(String(e)) + '</td></tr>'
    appendLog(now(), 'error', 'AI分析失败: ' + e)
  } finally {
    btn.disabled    = false
    btn.textContent = '分析'
  }
}

window.applyFilenameRenames = async function () {
  const inputDir  = document.getElementById('inputDir').value.trim()
  const applyBtn  = document.getElementById('applyRenameBtn')
  const statusEl  = document.getElementById('applyRenameStatus')
  const rows      = document.querySelectorAll('#analysisTableBody tr')

  if (!inputDir) { appendLog(now(), 'warn', '请先选择输入目录'); return }
  if (!rows.length) return

  const entries = []
  rows.forEach(tr => {
    const inp = tr.querySelector('.analysis-name-input')
    if (!inp) return
    const newName   = inp.value.trim()
    const origVideo = inp.dataset.origVideo || ''
    const origSub   = inp.dataset.origSub   || ''
    if (!newName) return
    const videoDot = origVideo.lastIndexOf('.')
    const videoExt = videoDot >= 0 ? origVideo.slice(videoDot) : ''
    if (origVideo) entries.push({ oldName: origVideo, newName: newName + videoExt })
    if (origSub) {
      const subDot = origSub.lastIndexOf('.')
      const subExt = subDot >= 0 ? origSub.slice(subDot) : ''
      entries.push({ oldName: origSub, newName: newName + subExt })
    }
  })

  if (entries.length === 0) {
    statusEl.textContent = '没有需要重命名的条目'
    statusEl.className   = 'analysis-rename-status rename-warn'
    return
  }

  applyBtn.disabled    = true
  applyBtn.textContent = '重命名中…'
  statusEl.textContent = ''
  statusEl.className   = 'analysis-rename-status'

  try {
    const results = await ApplyFilenameRenames(inputDir, entries)
    let ok = 0, skip = 0, fail = 0
    results.forEach(r => {
      if (r.ok && !r.skipped)  ok++
      else if (r.skipped)      skip++
      else                     fail++
    })

    const parts = []
    if (ok)   parts.push(ok   + ' 个成功')
    if (skip) parts.push(skip + ' 个跳过')
    if (fail) parts.push(fail + ' 个失败')
    statusEl.textContent = '重命名结果：' + parts.join('，')
    statusEl.className   = 'analysis-rename-status ' + (fail ? 'rename-error' : ok ? 'rename-ok' : 'rename-warn')

    results.filter(r => !r.ok && !r.skipped).forEach(r => {
      appendLog(now(), 'error', '重命名失败 [' + r.oldName + ']: ' + r.err)
    })
    results.filter(r => r.skipped && r.err).forEach(r => {
      appendLog(now(), 'warn', '重命名跳过 [' + r.oldName + ']: ' + r.err)
    })
    if (ok) appendLog(now(), 'info', '批量重命名完成，成功 ' + ok + ' 个')

  } catch (e) {
    statusEl.textContent = '调用失败: ' + String(e)
    statusEl.className   = 'analysis-rename-status rename-error'
    appendLog(now(), 'error', '应用重命名失败: ' + e)
  } finally {
    applyBtn.disabled    = false
    applyBtn.textContent = '应用重命名'
  }
}

window.closeFilenameAnalysis = function () {
  const applyBtn = document.getElementById('applyRenameBtn')
  if (applyBtn) applyBtn.style.display = 'none'
  const statusEl = document.getElementById('applyRenameStatus')
  if (statusEl) { statusEl.textContent = ''; statusEl.className = 'analysis-rename-status' }
  document.getElementById('filenameAnalysisPanel').classList.add('hidden')
}

function fnShort(name, maxLen) {
  if (!name || name.length <= maxLen) return name || ''
  const dot = name.lastIndexOf('.')
  if (dot > 0 && name.length - dot <= 6) {
    const stem = name.slice(0, dot)
    const ext  = name.slice(dot)
    const keep = maxLen - ext.length - 1
    return keep > 4 ? stem.slice(0, keep) + '…' + ext : name.slice(0, maxLen - 1) + '…'
  }
  return name.slice(0, maxLen - 1) + '…'
}


function saveWmDirs() {
  localStorage.setItem('muxer-wm-dirs', JSON.stringify({
    inputDir:  document.getElementById('wmInputDir').value,
    outputDir: document.getElementById('wmOutputDir').value,
  }))
}

window.saveWmSettings = function () {
  localStorage.setItem('muxer-wm-web', JSON.stringify({
    webVersion: document.getElementById('wmWebVersion').checked,
    bitrate:    document.getElementById('wmWebBitrate').value,
    codec:      document.getElementById('wmWebCodec').value,
  }))
}

window.onWmWebVersionChange = function () {
  const on = document.getElementById('wmWebVersion').checked
  document.getElementById('wmWebOptions').style.display = on ? 'block' : 'none'
  saveWmSettings()
}

function restoreWmSettings() {
  try {
    const s = JSON.parse(localStorage.getItem('muxer-wm-web') || '{}')
    if (s.webVersion) document.getElementById('wmWebVersion').checked = true
    loadWebBitrateValue(document.getElementById('wmWebBitrate'), s.bitrate)
    if (s.codec)      document.getElementById('wmWebCodec').value     = s.codec
  } catch (_) {}
  onWmWebVersionChange()
}


window.wmSelectInput = async function () {
  const dir = await SelectDirectory('选择输入目录（含视频）')
  if (dir) { document.getElementById('wmInputDir').value = dir; saveWmDirs() }
}

window.wmSelectOutput = async function () {
  const dir = await SelectDirectory('选择输出目录')
  if (dir) { document.getElementById('wmOutputDir').value = dir; saveWmDirs() }
}

window.startWatermark = async function () {
  const inputDir  = document.getElementById('wmInputDir').value.trim()
  const outputDir = document.getElementById('wmOutputDir').value.trim()
  if (!inputDir)  { wmLog('warn', '请先选择输入目录'); return }
  if (!outputDir) { wmLog('warn', '请先选择输出目录'); return }
  if (_wmProcessing) { wmLog('warn', '已有任务正在执行'); return }
  document.getElementById('wmLogArea').innerHTML = ''
  _wmProcessing = true
  setWmProcessing(true)
  try {
    await ProcessWatermark({
      inputDir,
      outputDir,
      notify:         document.getElementById('wmNotify').checked,
      autoOpenOutput: document.getElementById('wmAutoOpen').checked,
      webVersion:     document.getElementById('wmWebVersion').checked,
      webBitrateKbps: parseWebBitrateKbps('wmWebBitrate'),
      webCodec:       document.getElementById('wmWebCodec').value,
    })
  } catch (e) {
    wmLog('error', '调用失败: ' + e)
  } finally {
    _wmProcessing = false
    setWmProcessing(false)
  }
}

function setWmProcessing(active) {
  const startBtn = document.getElementById('wmStartBtn')
  const stopBtn  = document.getElementById('wmStopBtn')
  const packBtn  = document.getElementById('wmPackBtn')
  if (startBtn) {
    startBtn.disabled = active
    startBtn.textContent = active ? '处理中...' : '开始处理'
  }
  if (stopBtn) stopBtn.disabled = !active
  if (packBtn) packBtn.disabled = active
  if (!active) {
    if (packBtn) packBtn.textContent = '汇总'
    const fill = document.getElementById('wmProgressFill')
    if (fill) fill.style.width = '0%'
  }
}

window.packWatermarkOutput = async function () {
  const outputDir = document.getElementById('wmOutputDir').value.trim()
  if (!outputDir) { wmLog('warn', '请先选择输出目录'); return }
  if (isBusy()) { toast(`${busyReason()}正在执行，请等待完成或停止后再试`, 'warn'); return }
  document.getElementById('wmLogArea').innerHTML = ''
  _wmProcessing = true
  setWmProcessing(true)
  const packBtn = document.getElementById('wmPackBtn')
  if (packBtn) packBtn.textContent = '汇总中...'
  try {
    await PackWatermarkOutput(outputDir)
  } catch (e) {
    wmLog('error', '调用失败: ' + e)
  } finally {
    _wmProcessing = false
    setWmProcessing(false)
  }
}

window.stopWatermark = async function () {
  wmLog('warn', '正在停止...')
  await StopProcess()
}

function wmLog(level, msg) {
  appendLogTo(document.getElementById('wmLogArea'), now(), level, msg)
}


function saveDedupDirs() {
  localStorage.setItem('muxer-dedup-dirs', JSON.stringify({
    inputDir:  document.getElementById('dedupInputDir').value,
    outputDir: document.getElementById('dedupOutputDir').value,
  }))
}

window.dedupSelectInput = async function () {
  const dir = await SelectDirectory('选择输入目录（含视频）')
  if (dir) { document.getElementById('dedupInputDir').value = dir; saveDedupDirs() }
}

window.dedupSelectOutput = async function () {
  const dir = await SelectDirectory('选择输出目录')
  if (dir) { document.getElementById('dedupOutputDir').value = dir; saveDedupDirs() }
}

window.startDedup = async function () {
  const inputDir  = document.getElementById('dedupInputDir').value.trim()
  const outputDir = document.getElementById('dedupOutputDir').value.trim()
  if (!inputDir)  { dedupLog('warn', '请先选择输入目录'); return }
  if (!outputDir) { dedupLog('warn', '请先选择输出目录'); return }
  if (_dedupProcessing) { dedupLog('warn', '已有任务正在执行'); return }
  document.getElementById('dedupLogArea').innerHTML = ''
  _dedupProcessing = true
  setDedupProcessing(true)
  try {
    await ProcessDedup({
      inputDir,
      outputDir,
      notify:         document.getElementById('dedupNotify').checked,
      autoOpenOutput: document.getElementById('dedupAutoOpen').checked,
    })
  } catch (e) {
    dedupLog('error', '调用失败: ' + e)
  } finally {
    _dedupProcessing = false
    setDedupProcessing(false)
  }
}

function setDedupProcessing(active) {
  const startBtn = document.getElementById('dedupStartBtn')
  const stopBtn  = document.getElementById('dedupStopBtn')
  if (startBtn) {
    startBtn.disabled = active
    startBtn.textContent = active ? '处理中...' : '开始处理'
  }
  if (stopBtn) stopBtn.disabled = !active
  if (!active) {
    const fill = document.getElementById('dedupProgressFill')
    if (fill) fill.style.width = '0%'
  }
}

window.stopDedup = async function () {
  dedupLog('warn', '正在停止...')
  await StopProcess()
}

function dedupLog(level, msg) {
  appendLogTo(document.getElementById('dedupLogArea'), now(), level, msg)
}

whenWailsReady(startApp)
