import os, sys, time


def resolve_cache():
    if len(sys.argv) > 1 and sys.argv[1].strip():
        return sys.argv[1].strip()
    env = os.environ.get("MUXER_MODEL_DIR", "").strip()
    if env:
        return env
    if os.path.isdir("E:\\"):
        return r"E:\MuxerModels"
    return ""

_cache = resolve_cache()
if _cache:
    os.makedirs(_cache, exist_ok=True)
    os.environ.setdefault("HF_HOME", os.path.join(_cache, "huggingface"))
    os.environ.setdefault("TORCH_HOME", os.path.join(_cache, "torch"))
    print(f"模型缓存目录: {_cache}", flush=True)
else:
    print("未指定模型缓存目录，使用系统默认缓存", flush=True)

os.environ.setdefault("HTTP_PROXY", "http://127.0.0.1:7890")
os.environ.setdefault("HTTPS_PROXY", "http://127.0.0.1:7890")
os.environ.pop("HF_ENDPOINT", None)
os.environ.pop("HUGGINGFACE_HUB_ENDPOINT", None)

import whisperx
import whisperx.alignment as _align

ALL_LANGS = sorted(set(
    list(_align.DEFAULT_ALIGN_MODELS_TORCH.keys()) +
    list(_align.DEFAULT_ALIGN_MODELS_HF.keys())
))
extra = [a for a in sys.argv[2:] if a.strip()]
LANGS = extra if extra else ALL_LANGS
print(f"将下载 {len(LANGS)} 个语种对齐模型: {' '.join(LANGS)}", flush=True)

ok, fail = [], []
for i, lang in enumerate(LANGS, 1):
    print(f"\n===== [{i}/{len(LANGS)}] 下载对齐模型: {lang} =====", flush=True)
    t0 = time.time()
    try:
        whisperx.load_align_model(language_code=lang, device="cpu")
        print(f"OK {lang}  用时 {time.time()-t0:.0f}s", flush=True)
        ok.append(lang)
    except Exception as e:
        print(f"FAIL {lang}: {e}", flush=True)
        fail.append(lang)

print("\n========== 完成 ==========", flush=True)
print("成功:", ok, flush=True)
print("失败:", fail, flush=True)
print("ALL_DONE", flush=True)
