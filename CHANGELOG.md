# Changelog

## [Unreleased]

### Added
- Port guard (`gpu.guard_ports`, on by default): the server listens on the standard Forge `:7860` and Ollama `:11434` ports and moves the services it starts to loopback `127.0.0.1:7861` / `:11435`, so requests from any LAN program pass the same GPU budget queue as `/api/sd/` and `/api/llm/`. Only services with `autostart` are guarded, and only when the public port is free; a busy port is left alone with a warning in the queue status. Internal ports: `internal_port` per process

### Changed
- First-run wizard: unticking Forge or Ollama now really disables it (it used to be undone by install defaults; the Forge key didn't match its process; Ollama was always installed). The Python checkbox is gone — Python is installed with the SD backend. Startup installs only components that autostart; a disabled one is installed on its first manual start
- Installs of one component never run twice at once (startup install and a manual start wait for each other); the SD backend has a single install status
- `/` and mDNS report the real build version; new `--version` flag
- Ollama default env no longer sets `OLLAMA_DEBUG=1`
- Renamed to **Imagecaster Server** to match the Imagecaster client (ex SD Studio): Go module, binary `imagecaster-server`, Docker service, release assets, GitHub repo `Zazza/imagecaster-server`
- mDNS service type is now `_imagecaster._tcp` (instance "Imagecaster Server") — the one Imagecaster 1.0 browses; LAN auto-discovery works again
- Default data dir is `~/imagecaster-server`; an existing `~/sd-studio-server` is still used when the new one doesn't exist

### Removed
- Install methods `pip`, `binary`, `archive` (unused by any default; Forge installs from zip, Python from tgz, git and Ollama have per-OS code)
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
