# Imagecaster Server — documentation

[Русский](server-ru.md) · [README](../README.md)

Imagecaster Server runs Stable Diffusion Forge Neo and Ollama for the Imagecaster app on a computer with a graphics card: installs them into one data folder, starts and restarts them, checks their health, puts every GPU request through one VRAM-budget queue and announces itself on the LAN over mDNS.

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

**Windows.** `imagecaster-server.exe` opens the setup window: welcome (graphics card from `nvidia-smi`), data folder (with free space), install progress (stable-diffusion.cpp, Ollama, first start), ready (address for the app). Then the server runs with a tray icon. The window may be closed at any time after **Install** — installing continues, the tray shows the state. Next starts go straight to the tray if everything is installed, or to the install step if it isn't finished. A second start with the same folder shows "already running"; a busy port 8080 shows a clear error. Child processes run without console windows and are stopped together with the server (a Windows job object).

Tray menu: Stable Diffusion and Ollama state, address (click copies it), open Stable Diffusion in the browser (`http://127.0.0.1:7860`), **Dashboard** (starts `imagecaster-server-console.exe attach` in a console), log (`<data>/logs/server.log` in Notepad), **Exit** (asks first if the GPU queue has jobs).

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
  engines/sd-cpp/            stable-diffusion.cpp (sd-server) and CUDA libraries
  engines/onnxruntime/       ONNX Runtime for pose detection
  models/checkpoints/        single-file models
  models/text_encoders/      text encoders and tokenizers
  models/vae/, models/loras/, models/upscalers/
  models/openpose/           yolox_l.onnx, dw-ll_ucoco_384.onnx
  sd-webui-forge-neo/        Forge Neo, only when rolled back to it
  python/                    Python 3.13 managed by uv
  bin/uv(.exe)               uv 0.12.19
  uv-cache/                  uv package cache (PyTorch wheels)
  ollama/                    Ollama when it isn't installed in the system
  models/ollama/             Ollama models
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

Own Linux builds are needed because upstream builds require glibc 2.43 (Ubuntu 26.04) and do not start on Debian 13 or Ubuntu 24.04. They are built by `.github/workflows/sdcpp.yml` (`tools/sdcpp-build.sh`, Ubuntu 22.04, glibc 2.35). Linux arm64, Intel macOS and Windows arm64 are not supported: Forge Neo is the default there.

ONNX Runtime 1.31.1 (into `engines/onnxruntime`) and the DWPose models from Hugging Face (`yzd-v/DWPose`, into `pose_models_dir`) are installed with the engine. A cut download resumes where it stopped; a file already in place with the right checksum is not downloaded. A finished install is marked by `engines/sd-cpp/.installed` with the version. The engine version is set by the server, not the config: a newer server with a newer sd.cpp installs the engine again over the old one (`install.version` in the config is replaced on load). The installer does not download image models — Imagecaster does.

If an old config has no `sdcpp` section and the Forge models folder exists, `sdcpp` uses its folders as they are (`Stable-diffusion`, `Lora`, `VAE`, `text_encoder`, `ControlNetPreprocessor/openpose`, `ESRGAN`), copying nothing. `active_sd` of an old config is not changed: switch with `active_sd: sdcpp`.

**Forge Neo** 2.31 (`forge-neo`, rollback option: `active_sd: forge-neo`, [Haoming02/sd-webui-forge-classic](https://github.com/Haoming02/sd-webui-forge-classic), zip of the tag). After unpacking: git (if missing), uv (pinned version, checksum checked), `uv venv venv --python 3.13 --managed-python --seed`. Forge Neo starts with `launch.py --listen --api --uv` and installs PyTorch and its requirements itself on the first start (CUDA 13 build by default; on macOS `TORCH_COMMAND` installs the plain torch). The server sets `VIRTUAL_ENV` and the uv cache/python folders for it. If setup fails after the download, the next start only finishes the setup. A backend whose binary already exists is never reinstalled.

**Ollama**: an Ollama already in the system (or already running on 11434) is used. Otherwise it is installed into `<data>/ollama`: Windows — the standalone zip (no system install, no autostart), Linux — the `.tar.zst` archive, macOS — Homebrew, or the universal `ollama-darwin.tgz`. Extraction goes into a temporary folder first, so an interrupted install is redone.

Old configs: `active_sd: forge` (classic Forge) switches to Forge Neo; the old Forge folder stays on disk — move its models to `sd-webui-forge-neo/models`. The old `python` process entry is dropped.

## Configuration

`<data>/server-config.yaml`. Missing fields keep their defaults; a map entry (`processes.sd`, `backends.forge-neo`) given in the file replaces the default entry as a whole.

```yaml
port: 8080
mdns: true
lang: ""                      # "", "ru" or "en"
data_dir: /home/user/imagecaster-server
active_sd: forge-neo

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
    health_url: http://localhost:7860/sdapi/v1/options
    target_url: http://localhost:7860
    proxy_path: /api/sd/
    internal_port: 7861
    autostart: true
    restart: true
    max_restart: 5
  ollama:
    name: Ollama
    binary: ollama
    args: [serve]
    env:
      OLLAMA_HOST: 0.0.0.0:11434
      OLLAMA_MODELS: models/ollama
      OLLAMA_KEEP_ALIVE: 24h
      OLLAMA_NUM_PARALLEL: "2"
      OLLAMA_NUM_GPU: "-1"
    health_url: http://localhost:11434/api/tags
    target_url: http://localhost:11434
    proxy_path: /api/llm/
    internal_port: 11435
    autostart: true
    restart: true
    max_restart: 5

backends:
  forge-neo:
    name: Stable Diffusion Forge Neo
    process_key: sd
    binary: sd-webui-forge-neo/venv/bin/python     # Windows: venv/Scripts/python.exe
    args: [launch.py, --listen, --api, --uv]
    workdir: sd-webui-forge-neo
    models_dir: sd-webui-forge-neo/models/Stable-diffusion
    lora_dir: sd-webui-forge-neo/models/Lora
    vae_dir: sd-webui-forge-neo/models/VAE
    embedding_dir: sd-webui-forge-neo/embeddings
    install:
      method: zip
      url: https://github.com/Haoming02/sd-webui-forge-classic/archive/refs/tags/2.31.zip
      target: sd-webui-forge-neo
      version: "2.31"
```

Relative paths are resolved against the data folder.

**Process fields**: `name`, `binary`, `args`, `env`, `workdir`, `health_url`, `target_url`, `proxy_path`, `internal_port` (loopback port under the port guard; default public + 1), `autostart`, `restart`, `max_restart`, `install`, `category` (`utility` — shown in a separate dashboard block).

**Backend fields**: `name`, `process_key` (which process runs it), `binary`, `args`, `workdir`, `models_dir`, `lora_dir`, `vae_dir`, `embedding_dir`, `auto_optimize` (pick memory flags by VRAM), `install` (`method: zip`, `url`, `target`, `version`).

**stable-diffusion.cpp backend** (`kind: sdcpp`). The server runs `sd-server` and answers `/api/sd/` in the Forge format, so clients need no changes. Extra fields:
- `text_encoder_dir`, `upscaler_dir`, `tokenizer_dir` — model folders;
- a model made of several files also loads without an alias: when `forge_additional_modules` contains a text encoder, the server runs `--diffusion-model <model> --llm <encoder> --vae <VAE>`, a `.json` module goes to `--tokenizer`; a VAE alone gives `--model <model> --vae <VAE>`. Bare names are looked up in `vae_dir`, `text_encoder_dir`, then `tokenizer_dir`;
- `aliases` — a model name and the files it is assembled from;
- `pose_models_dir` — folder with `yolox_l.onnx` and `dw-ll_ucoco_384.onnx` for `/controlnet/detect` (`dw_openpose_full`). Default: `<models_dir>/../ControlNetPreprocessor/openpose`;
- `onnxruntime_lib` — path to the ONNX Runtime library. If unset, the library is looked up next to the server executable (and in an `onnxruntime/` folder next to it), and on Linux also through the system loader. Without the library or the models, `/controlnet/detect` returns 503 and the rest of the server keeps working.

Environment overrides: `SD_PORT`, `SD_DATA_DIR`, `SD_ACTIVE_BACKEND`.

## GPU queue and port guard

### Queue

`gpuqueue` runs GPU-heavy requests only while the VRAM budget has room for them; the rest wait in a FIFO queue with manual priority. This keeps Forge Neo and Ollama from loading models at the same time and running out of video memory.

| Path | Method | Weight |
|------|--------|--------|
| `/api/sd/sdapi/v1/txt2img`, `img2img`, `interrogate`, `extra-single-image`, `extra-batch-images` | POST | checkpoint file size + `sd_overhead_mb` (the checkpoint is tracked from `POST .../options`; unknown → `sd_default_weight_mb`), capped by the budget |
| `/api/llm/api/generate`, `api/chat`, `api/embed`, `api/embeddings`, `v1/chat/completions`, `v1/completions`, `v1/embeddings` | POST | `llm_weight_mb` |
| everything else | * | 0 — passes freely |

- Leases have a TTL and are renewed while the request runs; a lease of a dead client frees itself.
- An internal queue error lets the request through and adds a warning to the status (fail-open).
- Waiting longer than `max_wait_seconds` → `503` with `Retry-After`.
- State is in memory; a server restart starts from an empty queue.
- Managed in the dashboard (`g`) — locally and in attach mode.

### Port guard

With `gpu.guard_ports: true` (default) the server listens on the public Forge port `7860` and Ollama port `11434` itself and moves the services it starts to `127.0.0.1:7861` / `127.0.0.1:11435`. Any program in the LAN talking to Forge or Ollama directly goes through the same queue as `/api/sd/` and `/api/llm/`.

- Only services with `autostart` and a loopback `target_url` are guarded.
- A busy public port (for example, an Ollama already running in the system) is left alone; the queue status shows a warning, and that service is used as is.
- For Forge the server replaces `--listen/--port/--server-name` with `--port <internal>`; for Ollama it sets `OLLAMA_HOST=127.0.0.1:<internal>`.

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
| `/` | GET | `{name, version, status}` |
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
| `/api/server/models/text_encoder` | GET / POST | the same for text encoders and tokenizers (`.json`); for Forge the `models/text_encoder` folder |
| `/api/server/models/llm` | GET / POST | list Ollama models / pull one |
| `/api/server/models/llm/pull` | GET | pull an Ollama model with streamed progress, `?name=` |
| `/api/server/models/download/stream` | GET | download a model with streamed progress, `?type=sd\|lora\|vae\|text_encoder&url=&filename=` |
| `/api/server/models/delete/{sd,lora,vae,text_encoder,llm}/{name}` | DELETE | delete a model |
| `/api/gpu/*` | | queue and leases, see above |
| `/api/sd/*` | * | proxy to Forge Neo (through the queue) |
| `/api/llm/*` | * | proxy to Ollama (through the queue) |

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
installer/         stable-diffusion.cpp, ONNX Runtime, DWPose (release.go), Forge Neo (zip + uv venv), uv, git, Ollama
models/            checkpoints, LoRA, VAE, Ollama models
process/           process lifecycle, logs, restarts
tui/               terminal dashboard (Bubble Tea)
```

## Limitations and not verified

- The Forge web UI opened directly at `:7860` passes the guard, but its generation requests are not all weighted by the queue (only the API paths in the table are).
- Not tested on real hardware yet: the Windows window and tray, the macOS launcher and Forge Neo on macOS. CUDA 13 PyTorch needs a recent NVIDIA driver (not checked which minimum).
- About 8 GB without models is an estimate: sd.cpp with CUDA libraries, ONNX Runtime and DWPose — up to 3 GB, Ollama — about 4 GB (1.6 GB archive plus unpacking; the unpacked size is not measured). Forge Neo takes about 20 GB.
- The minimum NVIDIA driver for the CUDA 12 builds is not verified; with an old driver sd-server will not start — update the driver or switch to Forge Neo.
- No authorization; don't expose the API outside a trusted LAN. Lease IDs are not tied to their owner — any LAN client can renew or cancel any lease.
