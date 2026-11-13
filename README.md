# Imagecaster Server

[Русский](README-ru.md)

Runs image generation (**Stable Diffusion Forge Neo**) and text models (**llama.cpp**, with an Ollama-compatible API) on a computer with a graphics card for the [Imagecaster](https://github.com/Zazza/imagecaster) app. It installs everything itself into one folder, starts and restarts the services, and puts every GPU request through one queue so the card doesn't run out of memory. Imagecaster finds the server on the local network by itself (mDNS).

- Windows, Linux, macOS — no WSL, no Docker
- Forge Neo is installed automatically (with its own Python 3.13 and PyTorch); llama.cpp for text models is installed into the same folder (no build for Linux ARM and Windows ARM — no text models there)
- Nothing is registered in the system and nothing starts with it: you start the server when you need it
- Interface in English or Russian, chosen from the system language

## Install

Download the archive for your system from [Releases](https://github.com/Zazza/imagecaster-server/releases) and unpack it anywhere. The first start downloads Forge Neo, Python, PyTorch and llama.cpp — about 20 GB without models, so it takes a while.

### Windows

1. Start `imagecaster-server.exe`.
2. In the setup window choose the data folder (a drive with free space; not necessarily C:) and press **Install**.
3. When it says **Ready**, the window can be closed: the server keeps running, its icon is in the tray (by the clock).

The tray menu shows the state of the image engine and the LLM and has: the address for the app (click to copy), **Dashboard** (console with services, logs and the GPU queue), the log, and **Exit** — it stops everything. Next time just start `imagecaster-server.exe` again.

The setup window can be closed during installation — it continues in the background. A second start with the same folder only says that the server is already running.

`imagecaster-server-console.exe` is the same server with the terminal interface (see below).

### Linux

```bash
tar xzf imagecaster-server-*-linux-amd64.tar.gz
cd imagecaster-server-*-linux-amd64
./imagecaster-server
```

The first start asks for the data folder (Enter — default `~/imagecaster-server`) and installs everything. Then the terminal dashboard is shown. `--headless` runs without it (for a systemd service).

### macOS

Unpack the archive and open `Imagecaster Server.command` (the first time: right click → Open). It removes the download quarantine from the server and starts it in Terminal; the rest is the same as on Linux.

> Forge Neo on macOS runs on Apple Silicon without CUDA; this setup has not been tested on a Mac yet.

## Requirements

- NVIDIA graphics card with a recent driver (Forge Neo installs PyTorch for CUDA 13). Without one, generation is very slow or does not work.
- Free disk space: about 20 GB plus models (an SDXL checkpoint is ~6.5 GB).
- Internet for the first start.

## Terminal interface

| Key | Main screen |
|-----|-------------|
| `j`/`k` | select a service |
| `s` / `r` | start/stop, restart |
| `l` | service log |
| `g` | GPU queue: priority `u`/`d`, cancel `x` |
| `t` | server log |
| `q` | quit (in sub-screens: back) |

`imagecaster-server attach [host[:port]]` opens the same dashboard for a server that is already running — locally or on another computer; `q` closes only the dashboard.

## Command line

| Flag | Meaning |
|------|---------|
| `--data DIR` | data folder (default: the one chosen at the first start, otherwise `~/imagecaster-server`) |
| `--config FILE` | config file (default `<data>/server-config.yaml`) |
| `--port N` | HTTP API port (default 8080) |
| `--headless` | no interface, log to stdout |
| `--version` | print the version |

Environment: `IMAGECASTER_LANG=ru|en` — interface language; `SD_PORT`, `SD_DATA_DIR`, `SD_ACTIVE_BACKEND` override the config.

## How it works

```
 Imagecaster / any program in the LAN
        │                     │
   :8080 /api/sd/  /api/llm/  │ :7860 (Forge)  :11434 (LLM)
        └──────────┬──────────┘
           Imagecaster Server ── GPU queue (VRAM budget)
             │            │
     Forge Neo :7861   llama.cpp :11435   (loopback only)
```

The server takes the standard Forge (`7860`) and Ollama-compatible LLM (`11434`) ports and moves the services to loopback ports, so every request — from Imagecaster or any other program — goes through the same GPU queue. Details: [docs/server-en.md](docs/server-en.md).

## Development

```bash
go build -o imagecaster-server .
go test ./... -count=1 -timeout 120s
go vet ./...
make build-windows   # imagecaster-server.exe (window + tray) and imagecaster-server-console.exe
```

The Windows window/tray build uses `-tags desktop,production -ldflags -H windowsgui` (Wails + systray, no cgo). Releases are built by `.github/workflows/release.yml` on a `v*` tag.

## Documentation

- [Full documentation (English)](docs/server-en.md)
- [Полная документация (Русский)](docs/server-ru.md)
- [Changelog](CHANGELOG.md)
