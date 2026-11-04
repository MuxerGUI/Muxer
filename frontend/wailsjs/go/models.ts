export namespace main {
	
	export class AIConfig {
	    aiProvider: string;
	    hasDeepSeekKey: boolean;
	    hasZhipuKey: boolean;
	    keysFilePath: string;
	    hasKeysFile: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AIConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.aiProvider = source["aiProvider"];
	        this.hasDeepSeekKey = source["hasDeepSeekKey"];
	        this.hasZhipuKey = source["hasZhipuKey"];
	        this.keysFilePath = source["keysFilePath"];
	        this.hasKeysFile = source["hasKeysFile"];
	    }
	}
	export class AIConfigSave {
	    aiProvider: string;
	    deepseekKey: string;
	    zhipuKey: string;
	
	    static createFrom(source: any = {}) {
	        return new AIConfigSave(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.aiProvider = source["aiProvider"];
	        this.deepseekKey = source["deepseekKey"];
	        this.zhipuKey = source["zhipuKey"];
	    }
	}
	export class ASSStyle {
	    name: string;
	    fontname: string;
	    fontsize: number;
	    primaryColour: string;
	    secondaryColour: string;
	    outlineColour: string;
	    backColour: string;
	    bold: number;
	    italic: number;
	    underline: number;
	    strikeOut: number;
	    scaleX: number;
	    scaleY: number;
	    spacing: number;
	    angle: number;
	    borderStyle: number;
	    outline: number;
	    shadow: number;
	    alignment: number;
	    marginL: number;
	    marginR: number;
	    marginV: number;
	    encoding: number;
	
	    static createFrom(source: any = {}) {
	        return new ASSStyle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.fontname = source["fontname"];
	        this.fontsize = source["fontsize"];
	        this.primaryColour = source["primaryColour"];
	        this.secondaryColour = source["secondaryColour"];
	        this.outlineColour = source["outlineColour"];
	        this.backColour = source["backColour"];
	        this.bold = source["bold"];
	        this.italic = source["italic"];
	        this.underline = source["underline"];
	        this.strikeOut = source["strikeOut"];
	        this.scaleX = source["scaleX"];
	        this.scaleY = source["scaleY"];
	        this.spacing = source["spacing"];
	        this.angle = source["angle"];
	        this.borderStyle = source["borderStyle"];
	        this.outline = source["outline"];
	        this.shadow = source["shadow"];
	        this.alignment = source["alignment"];
	        this.marginL = source["marginL"];
	        this.marginR = source["marginR"];
	        this.marginV = source["marginV"];
	        this.encoding = source["encoding"];
	    }
	}
	export class AppInfo {
	    version: string;
	    buildTime: string;
	    goVersion: string;
	    os: string;
	    arch: string;
	    exePath: string;
	    hasKeysFile: boolean;
	    brandName: string;
	    brandUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.buildTime = source["buildTime"];
	        this.goVersion = source["goVersion"];
	        this.os = source["os"];
	        this.arch = source["arch"];
	        this.exePath = source["exePath"];
	        this.hasKeysFile = source["hasKeysFile"];
	        this.brandName = source["brandName"];
	        this.brandUrl = source["brandUrl"];
	    }
	}
	export class EffectiveBrand {
	    brandName: string;
	    brandUrl: string;
	    brandTag: string;
	    watermarkUrl: string;
	    watermarkBrand: string;
	
	    static createFrom(source: any = {}) {
	        return new EffectiveBrand(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.brandName = source["brandName"];
	        this.brandUrl = source["brandUrl"];
	        this.brandTag = source["brandTag"];
	        this.watermarkUrl = source["watermarkUrl"];
	        this.watermarkBrand = source["watermarkBrand"];
	    }
	}
	export class BrandConfig {
	    brandName: string;
	    brandUrl: string;
	    brandTag: string;
	    watermarkUrl: string;
	    watermarkBrand: string;
	    watermarkTagline: string;
	    effective: EffectiveBrand;
	    keysFilePath: string;
	    hasKeysFile: boolean;
	
	    static createFrom(source: any = {}) {
	        return new BrandConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.brandName = source["brandName"];
	        this.brandUrl = source["brandUrl"];
	        this.brandTag = source["brandTag"];
	        this.watermarkUrl = source["watermarkUrl"];
	        this.watermarkBrand = source["watermarkBrand"];
	        this.watermarkTagline = source["watermarkTagline"];
	        this.effective = this.convertValues(source["effective"], EffectiveBrand);
	        this.keysFilePath = source["keysFilePath"];
	        this.hasKeysFile = source["hasKeysFile"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CapabilityCheck {
	    name: string;
	    ok: boolean;
	    fatal: boolean;
	    detail: string;
	    impact: string;
	
	    static createFrom(source: any = {}) {
	        return new CapabilityCheck(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.ok = source["ok"];
	        this.fatal = source["fatal"];
	        this.detail = source["detail"];
	        this.impact = source["impact"];
	    }
	}
	export class ChapterEntry {
	    title: string;
	    start: string;
	
	    static createFrom(source: any = {}) {
	        return new ChapterEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.title = source["title"];
	        this.start = source["start"];
	    }
	}
	export class ConfigStatus {
	    aiProvider: string;
	    hasDeepSeekKey: boolean;
	    hasZhipuKey: boolean;
	    hasWatermark: boolean;
	    hasQwenASR: boolean;
	    keysWarning: string;
	
	    static createFrom(source: any = {}) {
	        return new ConfigStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.aiProvider = source["aiProvider"];
	        this.hasDeepSeekKey = source["hasDeepSeekKey"];
	        this.hasZhipuKey = source["hasZhipuKey"];
	        this.hasWatermark = source["hasWatermark"];
	        this.hasQwenASR = source["hasQwenASR"];
	        this.keysWarning = source["keysWarning"];
	    }
	}
	export class DedupOptions {
	    inputDir: string;
	    outputDir: string;
	    notify: boolean;
	    autoOpenOutput: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DedupOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.inputDir = source["inputDir"];
	        this.outputDir = source["outputDir"];
	        this.notify = source["notify"];
	        this.autoOpenOutput = source["autoOpenOutput"];
	    }
	}
	export class DiskUsage {
	    freeBytes: number;
	    freeText: string;
	    available: boolean;
	    lowWarning: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DiskUsage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.freeBytes = source["freeBytes"];
	        this.freeText = source["freeText"];
	        this.available = source["available"];
	        this.lowWarning = source["lowWarning"];
	    }
	}
	
	export class FilenameAnalysisItem {
	    videoFile: string;
	    subFile: string;
	    episode: string;
	    suggestName: string;
	    confidence: string;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new FilenameAnalysisItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.videoFile = source["videoFile"];
	        this.subFile = source["subFile"];
	        this.episode = source["episode"];
	        this.suggestName = source["suggestName"];
	        this.confidence = source["confidence"];
	        this.note = source["note"];
	    }
	}
	export class FilenameAnalysisResult {
	    items: FilenameAnalysisItem[];
	    summary: string;
	    hasIssues: boolean;
	    issueDetail: string;
	
	    static createFrom(source: any = {}) {
	        return new FilenameAnalysisResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], FilenameAnalysisItem);
	        this.summary = source["summary"];
	        this.hasIssues = source["hasIssues"];
	        this.issueDetail = source["issueDetail"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PreviewResult {
	    originalSizeMB: number;
	    estimatedSizeMB: number;
	    ratio: number;
	    originalShot: string;
	    compressedShot: string;
	    clipDurationSec: number;
	    codec: string;
	    crf: number;
	
	    static createFrom(source: any = {}) {
	        return new PreviewResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.originalSizeMB = source["originalSizeMB"];
	        this.estimatedSizeMB = source["estimatedSizeMB"];
	        this.ratio = source["ratio"];
	        this.originalShot = source["originalShot"];
	        this.compressedShot = source["compressedShot"];
	        this.clipDurationSec = source["clipDurationSec"];
	        this.codec = source["codec"];
	        this.crf = source["crf"];
	    }
	}
	export class ProcessOptions {
	    inputDir: string;
	    outputDir: string;
	    hardsub: boolean;
	    crf: number;
	    preset: string;
	    encoder: string;
	    codec: string;
	    scaleHeight: number;
	    nvencAutoScale: boolean;
	    nvencAutoCQ: boolean;
	    nvencAutoCrop: boolean;
	    format: string;
	    keepOriginalSubs: boolean;
	    embedFonts: boolean;
	    normalizeAudio: boolean;
	    subtitleOffset: number;
	    audioTrackIndex: number;
	    audioDelay: number;
	    tenBit: boolean;
	    hdrPassthrough: boolean;
	    hdr2sdr: boolean;
	    autoCrop: boolean;
	    stripDv: boolean;
	    audioBitrate: string;
	    multiSub: boolean;
	    extraArgs: string;
	    filenameTmpl: string;
	    notify: boolean;
	    autoOpenOutput: boolean;
	    keepSubStyle: boolean;
	    shutdownAfter: boolean;
	    webVersion: boolean;
	    webBitrateKbps: number;
	    webCodec: string;
	
	    static createFrom(source: any = {}) {
	        return new ProcessOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.inputDir = source["inputDir"];
	        this.outputDir = source["outputDir"];
	        this.hardsub = source["hardsub"];
	        this.crf = source["crf"];
	        this.preset = source["preset"];
	        this.encoder = source["encoder"];
	        this.codec = source["codec"];
	        this.scaleHeight = source["scaleHeight"];
	        this.nvencAutoScale = source["nvencAutoScale"];
	        this.nvencAutoCQ = source["nvencAutoCQ"];
	        this.nvencAutoCrop = source["nvencAutoCrop"];
	        this.format = source["format"];
	        this.keepOriginalSubs = source["keepOriginalSubs"];
	        this.embedFonts = source["embedFonts"];
	        this.normalizeAudio = source["normalizeAudio"];
	        this.subtitleOffset = source["subtitleOffset"];
	        this.audioTrackIndex = source["audioTrackIndex"];
	        this.audioDelay = source["audioDelay"];
	        this.tenBit = source["tenBit"];
	        this.hdrPassthrough = source["hdrPassthrough"];
	        this.hdr2sdr = source["hdr2sdr"];
	        this.autoCrop = source["autoCrop"];
	        this.stripDv = source["stripDv"];
	        this.audioBitrate = source["audioBitrate"];
	        this.multiSub = source["multiSub"];
	        this.extraArgs = source["extraArgs"];
	        this.filenameTmpl = source["filenameTmpl"];
	        this.notify = source["notify"];
	        this.autoOpenOutput = source["autoOpenOutput"];
	        this.keepSubStyle = source["keepSubStyle"];
	        this.shutdownAfter = source["shutdownAfter"];
	        this.webVersion = source["webVersion"];
	        this.webBitrateKbps = source["webBitrateKbps"];
	        this.webCodec = source["webCodec"];
	    }
	}
	export class QueueTask {
	    id: string;
	    name: string;
	    opts: ProcessOptions;
	    status: string;
	    addedAt: string;
	    startedAt: string;
	    finishedAt: string;
	    err: string;
	    attempts: number;
	
	    static createFrom(source: any = {}) {
	        return new QueueTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.opts = this.convertValues(source["opts"], ProcessOptions);
	        this.status = source["status"];
	        this.addedAt = source["addedAt"];
	        this.startedAt = source["startedAt"];
	        this.finishedAt = source["finishedAt"];
	        this.err = source["err"];
	        this.attempts = source["attempts"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class RenameEntry {
	    oldName: string;
	    newName: string;
	
	    static createFrom(source: any = {}) {
	        return new RenameEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.oldName = source["oldName"];
	        this.newName = source["newName"];
	    }
	}
	export class RenameResult {
	    oldName: string;
	    newName: string;
	    ok: boolean;
	    skipped: boolean;
	    err: string;
	
	    static createFrom(source: any = {}) {
	        return new RenameResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.oldName = source["oldName"];
	        this.newName = source["newName"];
	        this.ok = source["ok"];
	        this.skipped = source["skipped"];
	        this.err = source["err"];
	    }
	}
	export class TrackInfo {
	    index: number;
	    codec: string;
	    language: string;
	    title: string;
	
	    static createFrom(source: any = {}) {
	        return new TrackInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.codec = source["codec"];
	        this.language = source["language"];
	        this.title = source["title"];
	    }
	}
	export class ScanResult {
	    videoFile: string;
	    width: number;
	    height: number;
	    duration: number;
	    durationStr: string;
	    videoCodec: string;
	    fileSize: string;
	    fileSizeBytes: number;
	    videoBitrate: number;
	    isHdr: boolean;
	    isDv: boolean;
	    dvProfile: number;
	    audioTracks: TrackInfo[];
	    subTracks: TrackInfo[];
	    subFile: string;
	
	    static createFrom(source: any = {}) {
	        return new ScanResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.videoFile = source["videoFile"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.duration = source["duration"];
	        this.durationStr = source["durationStr"];
	        this.videoCodec = source["videoCodec"];
	        this.fileSize = source["fileSize"];
	        this.fileSizeBytes = source["fileSizeBytes"];
	        this.videoBitrate = source["videoBitrate"];
	        this.isHdr = source["isHdr"];
	        this.isDv = source["isDv"];
	        this.dvProfile = source["dvProfile"];
	        this.audioTracks = this.convertValues(source["audioTracks"], TrackInfo);
	        this.subTracks = this.convertValues(source["subTracks"], TrackInfo);
	        this.subFile = source["subFile"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleEntry {
	    index: number;
	    start: string;
	    end: string;
	    text: string;
	    format: string;
	    assParts: string[];
	    fps?: number;
	
	    static createFrom(source: any = {}) {
	        return new SubtitleEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.start = source["start"];
	        this.end = source["end"];
	        this.text = source["text"];
	        this.format = source["format"];
	        this.assParts = source["assParts"];
	        this.fps = source["fps"];
	    }
	}
	
	export class TranslateSubtitleConfig {
	    provider: string;
	    key1: string;
	    key2: string;
	    targetLang: string;
	    mode: string;
	    forceSRT: boolean;
	
	    static createFrom(source: any = {}) {
	        return new TranslateSubtitleConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.provider = source["provider"];
	        this.key1 = source["key1"];
	        this.key2 = source["key2"];
	        this.targetLang = source["targetLang"];
	        this.mode = source["mode"];
	        this.forceSRT = source["forceSRT"];
	    }
	}
	export class UpdateInfo {
	    current: string;
	    latest: string;
	    hasUpdate: boolean;
	    publishedAt: string;
	    url: string;
	    repo: string;
	    error: string;
	    checkedAt: string;
	    cached: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current = source["current"];
	        this.latest = source["latest"];
	        this.hasUpdate = source["hasUpdate"];
	        this.publishedAt = source["publishedAt"];
	        this.url = source["url"];
	        this.repo = source["repo"];
	        this.error = source["error"];
	        this.checkedAt = source["checkedAt"];
	        this.cached = source["cached"];
	    }
	}
	export class VMAFResult {
	    vmaf: number;
	    psnr: number;
	    ssim: number;
	    hasVmaf: boolean;
	
	    static createFrom(source: any = {}) {
	        return new VMAFResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.vmaf = source["vmaf"];
	        this.psnr = source["psnr"];
	        this.ssim = source["ssim"];
	        this.hasVmaf = source["hasVmaf"];
	    }
	}
	export class WatermarkOptions {
	    inputDir: string;
	    outputDir: string;
	    notify: boolean;
	    autoOpenOutput: boolean;
	    webVersion: boolean;
	    webBitrateKbps: number;
	    webCodec: string;
	
	    static createFrom(source: any = {}) {
	        return new WatermarkOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.inputDir = source["inputDir"];
	        this.outputDir = source["outputDir"];
	        this.notify = source["notify"];
	        this.autoOpenOutput = source["autoOpenOutput"];
	        this.webVersion = source["webVersion"];
	        this.webBitrateKbps = source["webBitrateKbps"];
	        this.webCodec = source["webCodec"];
	    }
	}

}

