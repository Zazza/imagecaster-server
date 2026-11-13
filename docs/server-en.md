# Imagecaster Server — documentation

[Русский](server-ru.md) · [README](../README.md)

Imagecaster Server runs stable-diffusion.cpp and llama.cpp for the Imagecaster app on a computer with a graphics card: installs them into one data folder, starts and restarts them, checks their health, puts every GPU request through one VRAM-budget queue and announces itself on the LAN over mDNS.

## Contents

- [Install and first start](#install-and-first-start)
- [Running modes](#running-modes)
- [Data folder](#data-folder)
- [What gets installed](#what-gets-installed)
- [Configuration](#configuration)
- [GPU queue and port guard](#gpu-queue-and-port-guard)
- [API](#api)
- [Components](#components)
- [Limitations and not verified](#limitations-and-not-verified)

## Install and first start

Release archives (GitHub Releases, tag `v*`):

| System | Archive | Inside |
|--------|---------|--------|
| Windows amd64/arm64 | `.zip` | `imagecaster-server.exe` (setup window + tray), `imagecaster-server-console.exe` (terminal UI) |
| Linux amd64/arm64 | `.tar.gz` | `imagecaster-server` |
| macOS amd64/arm64 | `.tar.gz` | `imagecaster-server`, `Imagecaster Server.command` |

Every archive has a `.sha256` file next to it.

**Windows.** `imagecaster-server.exe` opens the setup window: welcome (graphics card from `nvidia-smi`), data folder (with free space), install progress (stable-diffusion.cpp, llama.cpp, first start), ready (address for the app). Then the server runs with a tray icon. The window may be closed at any time after **Install** — installing continues, the tray shows the state. Next starts go straight to the tray if everything is installed, or to the install step if it isn't finished. A second start with the same folder shows "already running"; a busy port 8080 shows a clear error. Child processes run without console windows and are stopped together with the server (a Windows job object).

Tray menu: Stable Diffusion and LLM state, address (click copies it), open Stable Diffusion in the browser (`http://127.0.0.1:7860`), **Dashboard** (starts `imagecaster-server-console.exe attach` in a console), log (`<data>/logs/server.log` in Notepad), **Exit** (asks first if the GPU queue has jobs).

**Linux.** `./imagecaster-server` — the first start asks for the data folder (Enter keeps the default), installs everything and shows the dashboard.

**macOS.** `Imagecaster Server.command` removes the quarantine attribute from the bundled binary and starts it in Terminal (the first time: right click → Open).

Nothing starts with the system; there are no services, registry entries or launch agents.

## Running modes

| Mode | Command | What it is |
|------|---------|------------|
| Window + tray | `imagecaster-server.exe` (Windows build) | setup window, then tray |
| Terminal UI | `imagecaster-server`, `imagecaster-server-console.exe` | dashboard: services, health, GPU, queue, logs |
| Attach | `imagecaster-server attach [host[:port]]` | dashboard of an already running server (default `127.0.0.1:8080`) |
| Headless | `imagecaster-server --headless` | no UI, log to stdout (systemd); also used when stdin is not a terminal. The API is up at once, install runs in the background (`/api/server/install/status`) |

Flags: `--data DIR`, `--config FILE` (default `<data>/server-config.yaml`), `--port N`, `--headless`, `--version`. The Windows window build ignores the UI and starts the window unless `--headless` is given.

### Terminal UI keys

| Screen | Keys |
|--------|------|
| Main | `j`/`k` select, `s` start/stop, `r` restart, `l` service log, `g` GPU queue, `t` server log, `q` quit (attach: detach) |
| Logs / server log | `j`/`k`, `pgup`/`pgdn`, `g`/`G` scroll, `q`/`esc` back |
| GPU queue | `j`/`k` select, `u`/`d` priority, `x` cancel/release, `q`/`esc` back |

### Attach

Connects the dashboard to a running server over its REST API, e.g. from a desktop to the GPU computer. Polls every 3 s; when the connection is lost the last state stays on screen with a reconnect banner (backoff up to 30 s). `q` closes only the client.

### Language

English or Russian. Order: `IMAGECASTER_LANG=ru|en` → `lang:` in the config → on Windows the UI language of the user (`GetUserDefaultUILanguage`) → `LC_ALL`/`LC_MESSAGES`/`LANG` → English.

## Data folder

Everything lives in one folder:

```
<data>/
  server-config.yaml
  engines/sd-cpp-<OS>/        stable-diffusion.cpp (sd-server) and CUDA libraries (platform suffix: sd-cpp-windows, sd-cpp-linux)
  engines/onnxruntime/       ONNX Runtime for pose detection
  models/checkpoints/        single-file models
  models/text_encoders/      text encoders and tokenizers
  models/vae/, models/loras/, models/upscalers/
  models/openpose/           yolox_l.onnx, dw-ll_ucoco_384.onnx
  engines/llama-cpp-<OS>/   llama.cpp (llama-server) for text models (platform suffix, like sd-cpp)
  llamacpp-models.ini        model list for llama-server, written by the server from models/ollama
  models/ollama/             text models in the Ollama layout (manifests + blobs), used by llama.cpp (`llm.models_dir`)
  logs/server.log            server log (Windows window build)
  imagecaster-server.lock    one server per folder (Windows window build)
```

Which folder: `--data` → the folder chosen at the first start (remembered in `<user config dir>/imagecaster-server/data-dir`) → `~/imagecaster-server` → an existing `~/sd-studio-server` (old name). `SD_DATA_DIR` overrides the config value.

## What gets installed

**stable-diffusion.cpp** (`sdcpp`, default). The server downloads a ready `sd-server` build of version `master-1086-465adf3` for the OS and graphics card and checks the sha256 of every file:

| OS | NVIDIA card (`nvidia-smi -L`) | Other card with Vulkan | No graphics card |
|---|---|---|---|
| Windows x64 | upstream CUDA 12 build + CUDA libraries (~0.9 GB) | upstream Vulkan build (`System32\vulkan-1.dll` present) | upstream CPU build |
| Linux x64 | own CUDA 12.8 build (`sdcpp-linux-x86_64-cuda.tar.gz` from release `sdcpp-465adf3` of this repository) | own Vulkan build (`/dev/dri/renderD*` and `libvulkan.so.1` present) | own CPU build |
| macOS arm64 | upstream build (Metal) | | |

Own Linux builds are needed because upstream builds require glibc 2.43 (Ubuntu 26.04) and do not start on Debian 13 or Ubuntu 24.04. They are built by `.github/workflows/sdcpp.yml` (`tools/sdcpp-build.sh`, Ubuntu 22.04, glibc 2.35). Linux arm64, Intel macOS and Windows arm64 have no sd.cpp build and therefore no image generation: the engine install fails with "no stable-diffusion.cpp build for this platform".

ONNX Runtime 1.31.1 (into `engines/onnxruntime`) and the DWPose models from Hugging Face (`yzd-v/DWPose`, into `pose_models_dir`) are installed with the engine. A cut download resumes where it stopped; a file already in place with the right checksum is not downloaded. A finished install is marked by `engines/sd-cpp-<OS>/.installed` with the version. The engine version is set by the server, not the config: a newer server with a newer sd.cpp installs the engine again over the old one (`install.version` in the config is replaced on load). The installer does not download image models — Imagecaster does. Engine folders for sd.cpp and llama.cpp get a platform suffix (`sd-cpp-windows`, `llama-cpp-linux`, …): the data folder may be a shared volume between Windows and Linux, and engine builds are not compatible across OSes — each keeps its own. On the first start the old unsuffixed folder is moved to the new name if it holds a binary for this OS (a foreign build is left in place); paths in old configs are updated automatically.

**llama.cpp** (`active_llm: llamacpp` — there is no other text engine). The server downloads a prebuilt `llama-server` `b12283` into `engines/llama-cpp-<OS>` and checks sha256:

| System | NVIDIA | Vulkan | No GPU |
|---|---|---|---|
| Windows x64 | `win-cuda-12.4` (CUDA libraries are taken from `engines/sd-cpp-<OS>`; if they are not there, `cudart-llama-bin-win-cuda-12.4`, 391 MB, is downloaded) | `win-vulkan` | `win-cpu` |
| Linux x64 | `ubuntu-cuda-12.8` (CUDA libraries from `engines/sd-cpp-<OS>`, otherwise `cudart-…-ubuntu-cuda-12.8`, 594 MB) | `ubuntu-vulkan` | `ubuntu-x64` |
| macOS | `macos-arm64` / `macos-x64` (Metal) | | |

Linux builds need `libssl.so.3` in the system. `llama-server` runs in multi-model (router) mode on `127.0.0.1:11435`; the server itself answers on port `11434` and on `/api/llm/` as Ollama (see "API"). Models are used from `models/ollama` in Ollama format as is, without copying: the model blob and, for image models, the separate projector blob. Models that Ollama packed into its own format (for example `qwen2.5vl:7b`, one blob without a projector) cannot be opened by llama.cpp — requests to them and `/api/pull` answer with a clear error; use GGUF models from hf.co (`hf.co/<user>/<repo>:<quant>`).

**The Ollama program** is neither installed nor started: text models run on llama.cpp only. Linux ARM and Windows ARM have no llama.cpp build, so they have no text models: the `llm` process does not start, `GET /` has no `llamacpp` feature, the LLM routes answer 503. The `<data>/ollama` folder where older versions installed Ollama can be deleted; downloaded models live separately in `models/ollama`, keep them.

**Models of an installed Ollama** are used without downloading them again, read-only: the server also reads the `OLLAMA_MODELS` folder from its own environment, otherwise `~/.ollama/models` (Windows: `%USERPROFILE%\.ollama\models`), and on Linux `/usr/share/ollama/.ollama/models`. Connected folders go to the log. If a model is in both places, the server's own copy wins. Such models cannot be deleted through the server (409 with a hint to run `ollama rm`); `pull` always downloads into the server's own folder. Models llama.cpp cannot open are not listed (own ones are listed in `/api/server/models/llm` with `unsupported`, so they can be deleted). A server running as a service under another user sees that user's home folder. Switch off: `llm.read_ollama_models: false`.

**Upgrading from an old version.** A config without `active_llm` (written by a version before llama.cpp) is moved to the new engines on the first start of the new version: `active_sd: sdcpp` and `active_llm: llamacpp`. The previous config is saved next to it as `server-config.yaml.bak-before-sdcpp` (an existing copy is not overwritten); comments in the config stay. llama.cpp uses the Ollama model store (`models/ollama`, or the `OLLAMA_MODELS` folder of the old `ollama` process). If the config cannot be written, the switch applies to this run only and the reason goes to the log.

**Forge and Forge Neo are no longer supported.** On load, backends that are not sd.cpp (`forge`, `forge-neo` and other custom ones without `kind: sdcpp`) are dropped from the config and `active_sd` pointing to a dropped or unknown backend becomes `sdcpp`; one line goes to the log. If the file has no `sdcpp` section, sd.cpp takes the model folders of the dropped backend (the active one first; then `sd-webui-forge-neo/models`) as they are — `Stable-diffusion`, `Lora`, `VAE`, `text_encoder`, `ControlNetPreprocessor/openpose`, `ESRGAN` — and writes them into a new `sdcpp` section, copying nothing. An existing `sdcpp` section with its own paths is left untouched. The `python` process entry is removed, and an `sd` process still holding Forge launch data (python binary, `launch.py`, Forge env, `/sdapi/v1/options` health, `zip` install) is rewritten from the active sd.cpp backend (binary, workdir, args, health `/sdcpp/v1/capabilities`); other env keys, ports and `ollama`/`llm` entries are kept. If only these leftovers are found (backends already removed), the previous config is kept as `server-config.yaml.bak-before-forge-cleanup`. The config is rewritten once, the previous one is kept as `server-config.yaml.bak-before-forge-removal` (an existing copy is not overwritten); if the file cannot be written, the change applies to this run only. The Forge folders (`sd-webui-forge-neo`, `python`, `bin/uv`, `uv-cache`) are not deleted: once the models are no longer needed from there, delete them by hand.

**An old `ollama` process** in the config is taken over on start, the file is not rewritten: its `OLLAMA_MODELS` becomes `llm.models_dir` and `OLLAMA_KEEP_ALIVE` becomes `llm.keep_alive` (values set in `llm:` win), the process itself is dropped, `active_llm: ollama` is read as `llamacpp`.

Old configs: the old `python` process entry is dropped.

## Configuration

`<data>/server-config.yaml`. Missing fields keep their defaults; a map entry (`processes.sd`, `backends.sdcpp`) given in the file replaces the default entry as a whole.

```yaml
port: 8080
mdns: true
lang: ""                      # "", "ru" or "en"
data_dir: /home/user/imagecaster-server
active_sd: sdcpp
active_llm: llamacpp          # always llamacpp
llm:
  context_size: 8192          # context of every model (-c), one value for all requests; options.num_ctx in a request is ignored
  models_max: 1               # how many models stay loaded at the same time
  models_dir: models/ollama   # text models folder (Ollama layout); default <data_dir>/models/ollama
  keep_alive: 24h             # how long a model stays loaded without keep_alive in the request; cards under 24 GB — 5m
  read_ollama_models: true    # also use models of an installed Ollama, read-only

gpu:
  total_budget_mb: 0          # 0 = detected VRAM - reserve_mb; <= 0 disables the queue
  reserve_mb: 500
  max_wait_seconds: 120       # wait for budget, then 503 + Retry-After
  lease_ttl_seconds: 90
  sd_default_weight_mb: 11000 # SD job weight when the checkpoint is unknown
  sd_overhead_mb: 4500        # SD weight = checkpoint file size + this
  llm_weight_mb: 11000
  guard_ports: true           # port guard, see below

processes:
  sd:
    name: Stable Diffusion
    health_url: http://localhost:7860/sdcpp/v1/capabilities
    target_url: http://localhost:7860
    proxy_path: /api/sd/
    internal_port: 7861
    autostart: true
    restart: true
    max_restart: 5
  llm:                        # llama-server
    name: llama.cpp
    binary: engines/llama-cpp-linux/llama-server
    health_url: http://localhost:11434/health
    target_url: http://localhost:11434
    proxy_path: /api/llm/
    internal_port: 11435
    autostart: true
    restart: true
    max_restart: 5
    install: {method: release, target: engines/llama-cpp-linux, version: b12283}

backends:
  sdcpp:
    name: stable-diffusion.cpp
    kind: sdcpp
    process_key: sd
    binary: engines/sd-cpp-linux/sd-server        # Windows: sd-cpp-windows/sd-server.exe
    workdir: engines/sd-cpp-linux
    models_dir: models/checkpoints
    lora_dir: models/loras
    vae_dir: models/vae
    text_encoder_dir: models/text_encoders
    tokenizer_dir: models/text_encoders
    upscaler_dir: models/upscalers
    pose_models_dir: models/openpose
    onnxruntime_lib: engines/onnxruntime/lib/libonnxruntime.so.1.31.1
    install: {method: release, target: engines/sd-cpp-linux}
```

Relative paths are resolved against the data folder.

**Process fields**: `name`, `binary`, `args`, `env`, `workdir`, `health_url`, `target_url`, `proxy_path`, `internal_port` (loopback port under the port guard; default public + 1), `autostart`, `restart`, `max_restart`, `install`, `category` (`utility` — shown in a separate dashboard block).

**Backend fields**: `name`, `process_key` (which process runs it), `binary`, `args`, `workdir`, `models_dir`, `lora_dir`, `vae_dir`, `install` (`method: release`, `target`; the version is set by the server).

**stable-diffusion.cpp backend** (`kind: sdcpp`). The server runs `sd-server` and answers `/api/sd/` with its own job API (see «SD jobs API» below). The health check of `sd-server` is `GET /sdcpp/v1/capabilities`; an old Forge-style `health_url` (`/sdapi/v1/options`) is switched to it on start. Extra fields:
- `text_encoder_dir`, `upscaler_dir`, `tokenizer_dir` — model folders;
- a model made of several files also loads without an alias: when `model.modules` of a job contains a text encoder, the server runs `--diffusion-model <model> --llm <encoder> --vae <VAE>`, a `.json` module goes to `--tokenizer`; a VAE alone gives `--model <model> --vae <VAE>`. Bare names are looked up in `vae_dir`, `text_encoder_dir`, then `tokenizer_dir`;
- `aliases` — a model name and the files it is assembled from;
- `pose_models_dir` — folder with `yolox_l.onnx` and `dw-ll_ucoco_384.onnx` for `/controlnet/detect` (`dw_openpose_full`). Default: `<models_dir>/../ControlNetPreprocessor/openpose`;
- `onnxruntime_lib` — path to the ONNX Runtime library. If unset, the library is looked up next to the server executable (and in an `onnxruntime/` folder next to it), and on Linux also through the system loader. Without the library or the models, `/controlnet/detect` returns 503 and the rest of the server keeps working.

Environment overrides: `SD_PORT`, `SD_DATA_DIR`, `SD_ACTIVE_BACKEND`.

## GPU queue and port guard

### Queue

`gpuqueue` runs GPU-heavy requests only while the VRAM budget has room for them; the rest wait in a FIFO queue with manual priority. This keeps the image engine and the text model engine from loading models at the same time and running out of video memory.

| Path | Method | Weight |
|------|--------|--------|
| SD job (`POST /api/sd/jobs`) | — | checkpoint file size of the job's own `model.checkpoint` (empty — of the checkpoint chosen at submit, see below) + `sd_overhead_mb` (unknown or nothing loaded → `sd_default_weight_mb`), capped by the budget. The lease is taken by the job runner once per job, not per HTTP request; a job waits in the queue as long as needed (no `max_wait_seconds`). Only one SD job holds a lease at a time, the next ones wait in the queue (visible in `/api/gpu/status`) and do not take budget from text models |
| `/api/llm/api/generate`, `api/chat`, `api/embed`, `api/embeddings`, `v1/chat/completions`, `v1/completions`, `v1/embeddings` | POST | `llm_weight_mb` |
| everything else, including all of `/api/sd/*` | * | 0 — passes freely |

- Leases have a TTL and are renewed while the request runs; a lease of a dead client frees itself.
- An internal queue error lets the request through and adds a warning to the status (fail-open).
- Waiting longer than `max_wait_seconds` → `503` with `Retry-After` (HTTP requests only; SD jobs wait without a limit).
- An SD job is visible in `/api/gpu/status` and the dashboard while it waits and runs (kind `sd`, client from the job), moves with `PATCH /api/gpu/queue/{id}`; cancelling its lease there ends the job as `cancelled`.
- State is in memory; a server restart starts from an empty queue.
- Managed in the dashboard (`g`) — locally and in attach mode.
- With llama.cpp, before an SD job the server unloads idle text models when SD and LLM do not fit into the budget together (`sd + llm_weight_mb > budget`, or the budget is unknown). A model that is answering a request right now is not unloaded. It loads again by itself on the next request (1.5–3 s).

### Port guard

With `gpu.guard_ports: true` (default) the server listens on the public SD port `7860` and the Ollama-compatible LLM port `11434` itself and moves the services it starts to `127.0.0.1:7861` / `127.0.0.1:11435`. Any program in the LAN talking to these ports directly goes through the same queue as `/api/sd/` and `/api/llm/`.

- Only services with `autostart` and a loopback `target_url` are guarded.
- A busy public port (for example, an Ollama already running in the system) is left alone; the queue status shows a warning, and that service is used as is.
- For sd-server the server replaces `--listen-ip/--listen-port` with `--listen-ip 127.0.0.1 --listen-port <internal>`; for llama-server — `--host 127.0.0.1 --port <internal>`.

### Lease API (external GPU consumers)

```
POST   /api/gpu/lease                 {kind: sd|llm, client, weight_mb, priority?, wait_seconds?}
       -> 200 {id, acquired: true, ttl_seconds}
       -> 202 {id, acquired: false, position}
       -> 503 {"error": "gpu queue timeout"} + Retry-After
       -> 503 {"error": "gpu queue full"}          (100 waiting)
GET    /api/gpu/lease/{id}            -> {status: active|queued|unknown, position, lease_deadline}
POST   /api/gpu/lease/{id}/heartbeat  -> 200 | 404
DELETE /api/gpu/lease/{id}            -> 200 | 404
PATCH  /api/gpu/queue/{id}            {"move": "up"|"down"} -> 200 {position} | 400 | 404
GET    /api/gpu/status                -> {enabled, budget, running[], queue[], warnings[]}
```

Body limit 1 MB; `client` is cut to 32 characters; `wait_seconds` is capped by `max_wait_seconds`.

## API

All under `http://<host>:8080`. No authorization — the server is meant for a trusted LAN only.

| Endpoint | Method | What it does |
|----------|--------|--------------|
| `/` | GET | `{name, version, status, features}`; `features` lists the active engines and capabilities: `sdcpp` (sd.cpp is the active backend), `sd_jobs` (the SD jobs API below), `llamacpp` (when text models are available), `download_stream`. Old server versions don't send it |
| `/api/server/status` | GET | `processes`, `health`, `gpu`, `installs`, `models`, host CPU/RAM `sys` |
| `/api/server/start/{name}` | POST | start a process |
| `/api/server/stop/{name}` | POST | stop a process |
| `/api/server/restart/{name}` | POST | restart a process |
| `/api/server/logs/{name}` | GET | process log, `?lines=100` |
| `/api/server/install/status` | GET | install state of all components |
| `/api/server/install/status/{key}` | GET | install state of one component |
| `/api/server/install/{key}` | POST | install a component |
| `/api/server/install/logs/{key}` | GET | install log |
| `/api/server/backends/active` | GET | active backend `{key, name, process}` |
| `/api/server/models/sd` | GET / POST | list checkpoints / download `{url, filename}` |
| `/api/server/models/lora` | GET / POST | the same for LoRA |
| `/api/server/models/vae` | GET / POST | the same for VAE |
| `/api/server/models/text_encoder` | GET / POST | the same for text encoders and tokenizers (`.json`); without `text_encoder_dir` — the `text_encoder` folder next to `models_dir` |
| `/api/server/models/llm` | GET / POST | list text models / pull one (the same code as `/api/pull`; 503 when there is no llama.cpp) |
| `/api/server/models/llm/pull` | GET | pull a text model with streamed progress, `?name=` |
| `/api/server/models/download/stream` | GET | download a model with streamed progress, `?type=sd\|lora\|vae\|text_encoder&url=&filename=` |
| `/api/server/models/delete/{sd,lora,vae,text_encoder,llm}/{name}` | DELETE | delete a model |
| `/api/gpu/*` | | queue and leases, see above |
| `/api/sd/*` | * | the SD jobs API below |
| `/api/llm/*` | * | Ollama-compatible API (through the queue), answered by the server over llama.cpp; 503 when there is no llama.cpp |

**Ollama API over llama.cpp** (`/api/llm/…` and port `11434`): `GET /api/tags`, `GET /api/ps`, `POST /api/show`, `GET /api/version`, `POST /api/chat` and `POST /api/generate` (NDJSON stream by default, `stream:false` — one response; `images` go to llama-server as images; an empty `prompt`/`messages` loads the model, with `keep_alive: 0` it unloads it), `POST /api/pull` (from `registry.ollama.ai` and `hf.co`, resume, sha256 check), `DELETE /api/delete`, plus `GET /v1/models`, `POST /v1/chat/completions` and `/v1/completions` with Ollama model names. `keep_alive` is read as Ollama does (seconds, `"10m"`, `-1` — keep, `0` — unload right after the answer; if the model is answering another request, right after that one). Without `keep_alive` in the request, `llm.keep_alive` applies (24h by default), and on cards under 24 GB — 5 minutes. The context size is `llm.context_size` (8192) for every request: `options.num_ctx` is ignored, and llama-server rejects a request longer than the context with an error (Ollama accepts such a request with a larger `num_ctx`). llama-server always listens on `127.0.0.1:11435`; if port `11434` is busy (for example Ollama already runs in the system as a service), the server leaves it alone and the Ollama API is available at `http://<host>:8080/api/llm/`. The system prompt and parameters stored with the Ollama model (`system`, `params`) are added when the request has none; the Ollama template is not used — llama.cpp takes the template from the GGUF itself.

### SD jobs API (sd.cpp)

A generation is a job with an id: the server answers at once and runs the job in the background. A client that disconnects does not cancel it — only an explicit cancel does. Every job carries its own model, so two jobs with different models in a row each run on their own model (the engine is reloaded only when the model files differ). Names are the sd.cpp ones: samplers `euler_a`, schedulers `karras` (`""` = default), upscalers — the model file name without extension, built-in `lanczos` and `nearest`; for hires `""` means latent. Forge names (`Euler a`, `Karras`, `R-ESRGAN 4x+`) are rejected with `invalid_params`.

Errors: `4xx/5xx {"error":{"code","message"}}`, codes `invalid_params`, `model_not_found`, `queue_full`, `not_found`, `not_ready`, `sd_failed`, `cancelled`, `server_restarted`.

```
POST   /api/sd/jobs            {kind: txt2img|img2img|upscale, client, client_ref?,
                                model: {checkpoint, vae, modules[], klein_reference, moss_reference},
                                params: {prompt, negative_prompt, sampler, scheduler, steps, cfg_scale,
                                         distilled_cfg_scale, width, height, seed (-1 random), clip_skip, batch_size,
                                         hires: {enable, scale, upscaler, denoising, steps, resize_x, resize_y},
                                         init_image, mask, denoising_strength,      (img2img)
                                         image, upscaler, scale}}                   (upscale; model not needed)
       -> 202 JobStatus | 200 JobStatus (same client + client_ref already queued, running or completed)
       -> 400 invalid_params | model_not_found;  409 model_not_found (empty checkpoint, nothing loaded or queued);  429 queue_full (100 unfinished jobs; SD jobs do not count toward the GPU queue length limit, so a queue full of text requests does not reject them)
GET    /api/sd/jobs[?client=X]  -> {jobs: [JobStatus]}  (unfinished and recent)
GET    /api/sd/jobs/{id}        -> JobStatus | 404
GET    /api/sd/jobs/{id}/result -> {images: [base64 png], info} | 409 not_ready | 404
POST   /api/sd/jobs/{id}/cancel -> JobStatus (a queued job comes back already cancelled; a running one stops shortly; repeat is harmless; a finished job is unchanged)
DELETE /api/sd/jobs/{id}        -> 204 | 404  (result picked up; an unfinished job is cancelled)
GET    /api/sd/checkpoints      -> {checkpoints: [{name, file}]}   (name as used in model.checkpoint)
GET    /api/sd/vaes             -> {vaes: [{name, file}]}
GET    /api/sd/loras            -> {loras: [{name, path}]}         (in a prompt: <lora:name:weight>)
GET    /api/sd/samplers         -> {samplers: [...]}
GET    /api/sd/schedulers       -> {schedulers: ["", ...]}
GET    /api/sd/upscalers        -> {upscalers: [{name, scale}]}
GET    /api/sd/memory           -> {ram, cuda}
GET    /api/sd/state            -> {loaded_checkpoint, running_job}
POST   /api/sd/unload           -> {} | 409 while a job runs
POST   /api/sd/controlnet/detect   pose detection (dw_openpose_full)

JobStatus = {id, kind, client, client_ref, status: queued|running|completed|failed|cancelled|lost,
             position (0 unless queued), progress: {step, steps}, error: null|{code, message},
             created_at, started_at, finished_at, expires_at}   (RFC3339, null when not set)
```

- `model.checkpoint: ""` — fixed at submit: the model of the last unfinished job in the queue, or the loaded one if the queue is empty, with its VAE and modules unless the job sets its own; neither → `409 model_not_found`. The job status and GPU lease then name that checkpoint.
- `info` of a txt2img/img2img result: `checkpoint` (resolved), `vae`, `modules`, `sampler`, `scheduler`, `steps`, `cfg_scale` (actual, defaults 20 and 7 filled in), `seed` (actual), `all_seeds`, `width`, `height`, `prompt`, `negative_prompt`, `batch_size`, `denoising_strength` (img2img only), `sd_model_name`, `backend`.
- `running` starts when the engine reports the first step (model loading counts as `queued`).
- Results are kept on disk in `<data>/sdjobs` for `sd.job_result_ttl_hours` (24 by default) and survive a restart; jobs that were queued or running at a restart become `lost` with `server_restarted`. Expired results are removed. The folder and files are readable by the server user only (0700/0600); job input images are not kept in memory after the job ends.
- `/api/sd/*` sends no CORS headers: a web page in a LAN browser cannot read job results (other routes keep `Access-Control-Allow-Origin: *`).

mDNS: service `_imagecaster._tcp`, instance "Imagecaster Server", TXT `version=<version>`; `mdns: false` turns it off.

## Components

```
main.go            modes: window+tray / TUI / headless / attach; wiring
desktop/           Windows setup window (Wails) and tray (systray); state rules shared with tests
desktop_windows.go engine of the window build (reuses the headless start path)
portguard.go       port guard listeners
gate.go, proxy.go  GPU gate and reverse proxy for /api/sd/ and /api/llm/
handlers.go        /api/server/* handlers
mdns.go            mDNS announcement
attach/            remote dashboard client over REST
config/            config types, defaults, loading, migration of old configs, data folder
gpu/               nvidia-smi monitor, launch flags by VRAM
gpuqueue/          VRAM budget queue and Lease API
health/            HTTP health checks
hide/              start child processes without console windows on Windows
i18n/              English/Russian strings and language detection
installer/         stable-diffusion.cpp, ONNX Runtime, DWPose (release.go), llama.cpp (llamacpp.go)
llamacpp/          Ollama API over llama-server router: model store, pull, chat/generate conversion, keep_alive
llm_wire.go        llama.cpp wiring: facade on /api/llm/, unload before SD jobs, /api/server/models/llm*
models/            checkpoints, LoRA, VAE, Ollama models
process/           process lifecycle, logs, restarts
tui/               terminal dashboard (Bubble Tea)
```

## Limitations and not verified

- Not tested on real hardware yet: the Windows window and tray, the macOS launcher and sd.cpp on macOS.
- About 8 GB without models is an estimate: sd.cpp with CUDA libraries, ONNX Runtime and DWPose — up to 3 GB, llama.cpp — 0.2–1 GB (with CUDA; up to 1.4 GB without the shared sd.cpp libraries).
- With llama.cpp: embeddings (`/api/embed`) are not supported (501); `tools` in `/api/chat` are not passed on; the context size is one value for all models (`llm.context_size`), `options.num_ctx` in a request does not change it. Sharing the sd.cpp CUDA libraries with llama.cpp on Windows is not checked live (the versions match: cudart 12.8 in sd.cpp, llama.cpp built for 12.4).
- The minimum NVIDIA driver for the CUDA 12 builds is not verified; with an old driver sd-server will not start — update the driver.
- No authorization; don't expose the API outside a trusted LAN. Lease IDs are not tied to their owner — any LAN client can renew or cancel any lease.
