# Changelog

## [Unreleased]

### Added
- Windows: install with clicks, like the Partscaster worker. The release zip has `imagecaster-server.exe` — a setup window (data folder, graphics card, free space, install progress) and then a tray icon (Forge Neo and Ollama status, address for the app, open Forge Neo, dashboard, log, exit) — and `imagecaster-server-console.exe` (the TUI, also opened from the tray as the dashboard). No console windows for child processes; quitting stops all of them. A second start with the same folder only says it is already running. Nothing starts with Windows
- macOS: release archive has `Imagecaster Server.command` that clears the download quarantine and starts the server in Terminal
- Release archives have `.sha256` checksum files; Windows archives are `.zip`
- Port guard (`gpu.guard_ports`, on by default): the server listens on the standard Forge `:7860` and Ollama `:11434` ports and moves the services it starts to loopback `127.0.0.1:7861` / `:11435`, so requests from any LAN program pass the same GPU budget queue as `/api/sd/` and `/api/llm/`. Only services with `autostart` are guarded, and only when the public port is free; a busy port is left alone with a warning in the queue status. Internal ports: `internal_port` per process

### Changed
- GPU queue also weighs Forge upscaling (`extra-single-image`, `extra-batch-images`) and Ollama's embeddings and OpenAI-compatible API (`api/embed`, `api/embeddings`, `v1/chat/completions`, `v1/completions`, `v1/embeddings`) — programs using them no longer bypass the queue. The Forge web UI stays unweighted on purpose
- First terminal start creates nothing until the wizard is answered (no stray config in the default folder) and keeps `--port` (it used to be lost on the first run)
- The old Python 3.10 folder (`<data>/python`) is no longer put into the services' PATH
- `--headless` serves the API right away and installs/starts the services in the background, so a client can connect and see the install progress (it used to wait for the whole install). A busy port stops the server at once
- `--headless` first start with `--data DIR` writes that folder into the new config (it used to write `~/imagecaster-server`, so from the second start everything went there)
- No crash ("concurrent map read and map write") when the status is polled while Ollama finishes installing or on Retry in the window
- On exit the server also stops an Ollama it started from a system install (it used to stay running and hold VRAM); nothing is started after shutdown began; an interrupted `uv venv` is stopped and removed so the next start redoes it
- Forge Neo is unpacked next to its folder and renamed in one step (retried while antivirus holds files); a folder without `launch.py` (e.g. only downloaded models) gets the code without losing the models; an existing custom backend folder is never wiped
- MinGit on Windows is unpacked correctly (`cmd/git.exe` was put in the wrong place)
- Downloads (installer and models) abort after 60 s without data instead of hanging
- Port guard treats 7860/11434 as busy when something already answers on 127.0.0.1 (e.g. Ollama.app on macOS) and keeps the port taken until the guard starts
- The data folder typed in the first-run wizard is made absolute (`~` expanded)
- A child service's PATH no longer grows on every restart
- SD backend is **Forge Neo** 2.31 (`forge-neo`, Haoming02/sd-webui-forge-classic) — the one Imagecaster needs. Its venv is created with a bundled `uv` 0.12.19 (checksum-pinned, in `<data>/bin`) and Python 3.13 (in `<data>/python`); Neo installs PyTorch itself on first start (`--uv`, into the same venv, uv cache in `<data>/uv-cache`). An existing backend whose binary is already in place is never reinstalled; if setup failed after download, the next start only finishes the setup
- Ollama: the one already installed in the system is used; otherwise it is installed into `<data>/ollama`. On Windows this is now the standalone zip instead of `OllamaSetup.exe`, so nothing is installed system-wide and nothing autostarts; on macOS without Homebrew the universal `ollama-darwin.tgz` is used (the per-arch name was a 404)
- First-run wizard asks only for the data folder (Neo is required, Ollama is detected) and warns about the C: drive on Windows. Startup installs only components that autostart
- Interface language Russian or English, chosen from the system language (`IMAGECASTER_LANG` or `lang:` in config override it): wizard, install screen, dashboard, logs, GPU queue and attach mode. In attach mode the footer says `[q] detach` (the server keeps running); the empty UTILITIES block is hidden
- Installs of one component never run twice at once (startup install and a manual start wait for each other); the SD backend has a single install status
- `/` and mDNS report the real build version; new `--version` flag
- Ollama default env no longer sets `OLLAMA_DEBUG=1`
- Renamed to **Imagecaster Server** to match the Imagecaster client (ex SD Studio): Go module, binary `imagecaster-server`, Docker service, release assets, GitHub repo `Zazza/imagecaster-server`
- mDNS service type is now `_imagecaster._tcp` (instance "Imagecaster Server") — the one Imagecaster 1.0 browses; LAN auto-discovery works again
- Default data dir is `~/imagecaster-server`; an existing `~/sd-studio-server` is still used when the new one doesn't exist. A folder chosen in the wizard or the setup window is remembered (`<user config dir>/imagecaster-server/data-dir`) and its config is written there (it used to land in the default folder)

### Changed (docs)
- README, README-ru and docs/server-en/ru rewritten: install per OS, data folder, Forge Neo/Ollama setup, current config, port guard, real API list, limitations

### Removed
- Classic Forge backend (an old config with `active_sd: forge` switches to Forge Neo; the old Forge folder is left on disk — move its models to `sd-webui-forge-neo/models`) and the separate Python 3.10 component (python-build-standalone, base packages, venv pre-install of torch/deps)
- Install methods `pip`, `binary`, `archive`, `tgz` (no default uses them)
- Docker files and the stale `server-config.yaml` shipped in release archives (config is generated in the data dir on first run)
- Legacy standalone GPU proxy `gpuproxy` and the `proxy:` config section (superseded by the port guard; an old `proxy:` section is ignored), and the internal `X-SD-Studio` header
- GPU lease kind `yue`: no client ever used it; `POST /api/gpu/lease` now accepts `sd` and `llm` only (`yue` → 400 `invalid kind`)

## [1.1.0] — 2026-10-08

### Added
- Attach mode: `sd-studio-server attach [host[:port]]` — remote TUI dashboard over the daemon REST API (3s polling, reconnect banner with backoff, process control, logs view); `q` exits the client only, the daemon keeps running; daemon strings sanitized against terminal ESC injection, 2 MB response cap
- `/api/server/status` now includes a `sys` block (daemon host CPU/RAM) for remote dashboards
- GPU budget queue (`gpuqueue/`): weighted VRAM semaphore with FIFO queue + priority, TTL leases with heartbeat, fail-open semantics, bounded wait → 503 + Retry-After
- Proxy gating: heavy SD/LLM paths acquire GPU budget before forwarding; SD weight = checkpoint file size + overhead (checkpoint tracked by sniffing `POST /sdapi/v1/options`)
- Lease API for external GPU consumers (yue-worker): `POST /api/gpu/lease`, heartbeat, `DELETE`, `PATCH /api/gpu/queue/{id}`, `GET /api/gpu/status`
- Embedded web panel at `/ui` (budget bar, running jobs, queue move/cancel, warnings; 2s polling)
- TUI: GPU queue line (budget bar, run/queue counts, warnings)
- Config section `gpu:` (auto budget from detected VRAM, weights, TTL, max wait)

### Changed
- `gpuproxy/` marked legacy (standalone mode, off by default) — superseded by `gpuqueue`
- CORS middleware now allows PATCH (queue reorder)

### Removed
- Web panel `/ui` (package `webui`); GPU queue management moved to the TUI (`g` screen, local + attach)
- Rembg component: process config, `/api/rembg/` proxy route, installer step, wizard entry, docs (client dropped it in v0.8.0)

## [1.0.0] — 2025-05-30

### Added
- REST API server for managing AI services (SD WebUI Forge, Ollama, Rembg)
- Process lifecycle management (start, stop, restart, watch, auto-restart)
- Health monitoring with HTTP checks and latency tracking
- GPU monitor — VRAM tracking, utilization, auto-configure Ollama based on VRAM
- GPU proxy with priority queue and VRAM guard for concurrent inference
- Component installer — SD WebUI Forge, Ollama, Rembg with Python venv
- Model manager — download, list, delete SD models
- mDNS service discovery for LAN clients
- Bubble Tea TUI dashboard with setup wizard
- Docker support (multi-stage Alpine build)
- GitHub Actions CI (test + vet on push/PR)
- GitHub Actions Release (cross-platform binaries on tag)

### Fixed
- Run Ollama on CPU when VRAM < 16GB to free memory for SD generation
- Auto-configure Ollama keep_alive based on available VRAM
- Bind Ollama to 0.0.0.0 for external access on LAN
- Set LD_LIBRARY_PATH for Ollama to find CUDA libraries
- Preserve Ollama Args/WorkDir when updating binary path after install
- Only stop managed processes on shutdown, not system services
- Download progress for Ollama installer in TUI
- Generation stability, compound presets, LLM JSON mode, UI fixes
