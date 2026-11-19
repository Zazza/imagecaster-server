# Changelog

## [Unreleased]

### Added
- LoRA training: a person's display name — `display_name` in the person card and in `GET /api/train/people`, form field `display_name` on `POST /api/train/jobs`, `POST /api/train/people/{name}/display` `{display_name, replace}` (up to 40 characters, empty removes it; the name keeps the spelling it already has on the person's other LoRAs; one LoRA per name and base, queued and running trainings included — `409 display_taken`, `replace: true` (form field `display_replace=1` when training) moves the name); rename keeps it
- LoRA training: `POST /api/train/jobs` takes `start=false` — the job is created `paused` with its photos on the server and waits for `.../resume`; several such jobs can wait at once. Resubmitting the same `client` + `client_ref` returns a paused job too (was `409 name_taken`)
- LoRA training: `POST /api/train/jobs/{id}/pause` and `.../resume` (new status `paused`, the trainer saves every 250 steps and resumes from the last save); a server restart pauses unfinished training instead of losing it; `POST /api/train/people/{name}/rename`
- Face swap: SD job `kind: face_swap` (`params.image`, `person`, `restore_blend` 0..1, default 0.6) puts a trained person's face into a picture on the CPU (ONNX Runtime: SCRFD, inswapper 128, GFPGAN 1.4, BiSeNet mask), without the GPU queue, one at a time; frames with no face, a face under 60 px or turned over 65° are skipped (`images: []`, `info.skip`). Errors `404 person_not_found`, `409 identity_missing`, `503 face_models_missing`. Model type `face` (`.onnx`, folder `face` next to `models_dir`); root `features` lists `face_swap` when ONNX Runtime and all six models are there
- LoRA training computes the person's face identity before the GPU lease and keeps it in `<name>.person.json` (API: `identity: true|false`; a failure does not stop training, job `identity: "skipped:<reason>"`); `POST /api/train/people/{name}/identity` computes it from 3–50 new photos; rename keeps it
- SD jobs: img2img takes an optional `params.ref_image` — with `model.klein_reference` it goes to sd.cpp as the second reference picture after `init_image` (garment try-on by photo); without `klein_reference` → 400 `invalid_params`

## [2.0.0] — 2026-11-16

The server generates images with stable-diffusion.cpp and runs text models on llama.cpp: no Python, Forge Neo or Ollama program. Imagecaster 2.0 needs this version.

### Changed
- With sd.cpp, `/api/sd/` is a job API instead of the Forge-compatible one: `POST /api/sd/jobs` answers at once with a job id, the job runs in the background, the client polls `GET /api/sd/jobs/{id}`, takes `.../result` and deletes the job. Each job carries its own model, VAE, modules and Klein/Moss flags — there is no shared "current model" any more. A client that disconnects no longer cancels the generation; only `POST .../cancel` (or cancelling the job's lease in the GPU queue) does. Resubmitting with the same `client_ref` returns the existing job. Lists moved to `/api/sd/checkpoints`, `vaes`, `loras`, `samplers`, `schedulers`, `upscalers`, plus `memory`, `state`, `unload`. Only sd.cpp names are accepted (`euler_a`, `karras`, upscaler file names, `lanczos`, `nearest`); Forge names give `invalid_params`. `GET /` lists the `sd_jobs` feature
- An SD job takes its GPU lease once, when it is queued, and waits as long as needed; its weight comes from the job's own checkpoint. The gate no longer leases anything for `/api/sd/*`
- Finished results are kept in `<data>/sdjobs` for 24 h (`sd.job_result_ttl_hours`) and survive a restart; jobs that were queued or running become `lost` (`server_restarted`)
- The sd.cpp health check uses `GET /sdcpp/v1/capabilities`
- `model.checkpoint: ""` is fixed at submit to the model of the last unfinished job, or the loaded one if the queue is empty (with its VAE and modules unless the job sets them); neither → `409 model_not_found`. Repeating a `client_ref` returns the existing job before any validation; a broken `init_image`/`mask` is rejected with 400 at submit; SD jobs do not count toward the GPU queue length limit (their own limit is 100 unfinished jobs), so they are never accepted and then failed with `queue_full`; a result that cannot be written to disk fails the job instead of completing it; `info` width/height come from the result image when the job did not set them. Result `info` carries the actual generation parameters under sd.cpp names (`checkpoint`, `vae`, `sampler`, `scheduler`, `steps`, `cfg_scale`, `seed`, size, prompts, `denoising_strength` for img2img). Cancelling a queued job answers `cancelled` at once. Only one SD job holds a GPU lease at a time, so waiting jobs no longer take budget from text models. `<data>/sdjobs` is private to the server user (0700/0600); `/api/sd/*` sends no CORS headers
- Upgrading from an old version moves the server to sd.cpp and llama.cpp on the first start: models stay where they are (Forge folders, Ollama model store), nothing is downloaded; the old config is kept as `server-config.yaml.bak-before-sdcpp`
- The server no longer installs or starts the Ollama program: text models run on llama.cpp only (Ollama-compatible API on `:11434` and `/api/llm/`, models in the Ollama layout). An old config is taken over on start without rewriting the file: `OLLAMA_MODELS` becomes `llm.models_dir`, `OLLAMA_KEEP_ALIVE` becomes `llm.keep_alive` (default 24h, 5m on cards under 24 GB), the `ollama` process is dropped. Linux ARM, Windows ARM and Intel Macs have no llama.cpp build, so they have no text models; `GET /` then has no `llamacpp` feature and the LLM routes answer 503
- Engine folders get a platform suffix: `engines/sd-cpp-windows`, `engines/llama-cpp-linux` and so on. The data folder may be a shared volume between Windows and Linux, and engine builds are not compatible across OSes — each OS now keeps its own. On the first start the old unsuffixed folder is moved to the new name if it holds a binary for this OS (a foreign build is left in place and its OS reinstalls fresh), and engine paths in old configs are updated automatically
- Process-control API errors: an unknown process answers 404, an already running one 409, a missing model 404; every error is JSON `{"error": ...}`

### Added
- sd.cpp step cache: SD jobs accept `params.cache_mode` (`easycache`, `ucache`, `dbcache`, `taylorseer`, `cache-dit`, `spectrum`) and `cache_option`. Off unless the job asks; bad values give 400; the mode is written to the result info and `GET /` lists the `sd_cache` feature. Moss 50 steps: 67 s → 26–30 s
- Model downloads resume a `.part` file with HTTP Range, start over when the source changed or ignores Range, verify sha256 when given, skip complete files and refuse a second download of the same file
- On GPUs below 15 GB sd-server gets `--max-vram -3` (part of the model goes to RAM instead of failing) unless a memory flag is set by hand
- Multi-file models: text encoders, VAE and modules are passed as `--llm`, `--clip_l`, `--t5xxl`, `--tokenizer`; the mmproj vision module as `--llm_vision` (Qwen-Image edits); Z-Image from split files under any name; Chroma gets `chroma_use_dit_mask=false`; a checkpoint name whose extension does not exist falls back to the same stem with another extension
- DWPose detection for ControlNet in the server, matching Forge output
- Models of an installed Ollama (`OLLAMA_MODELS`, `~/.ollama/models`, on Linux `/usr/share/ollama/.ollama/models`) are used without downloading again, read-only: the server's own copy wins, deleting them answers 409 with a hint to run `ollama rm`, models llama.cpp cannot open are not listed. Switch off: `llm.read_ollama_models: false`
- `GET /` lists `features` (active engines and capabilities), so Imagecaster can tell an old server from a new one

### Fixed
- An LLM request no longer bypasses a full GPU queue: it answers 503 `gpu queue full` with `Retry-After`
- Cancel during a model switch returns at once; Unix stop sends SIGTERM to the process group and SIGKILL after the grace period, Windows stop uses `taskkill /T`
- Process status races: a manually stopped process stays stopped; a health check that never answers gives the `unhealthy` status instead of `running`
- Service logs and `server.log` rotate at 20 MB
- `llama pull` no longer hangs on a frozen connection: downloads share one resumable downloader with a stall detector
- Queued jobs keep their init image and mask on disk instead of memory
- A missing LoRA is skipped instead of failing the generation
- SDXL models gave white images: the Forge default `clip_skip=1` is no longer passed to sd.cpp
- Windows: the tray **Dashboard** button opened a console that flashed and closed. The console exe got a NUL stdin instead of the new console's keyboard (spawned from the windowed exe), treated it as "not a terminal" and exited; it now opens `CONIN$`/`CONOUT$` of its own console window and shows the dashboard

### Removed
- Release builds for Linux arm64, Intel macOS and Windows arm64: there is no sd.cpp for them, so no image generation; the server is released for Windows x64, Linux x64 and macOS arm64
- Forge and Forge Neo support: the server no longer installs or starts them (no Python, uv, git, PyTorch downloads). On load, an old config loses its `forge`/`forge-neo` (and other non-sd.cpp) backends and `active_sd` switches to `sdcpp`; if the file had no `sdcpp` section, sd.cpp takes the Forge model folders as they are. An existing `sdcpp` section is kept untouched. The `python` process and Forge launch data in the `sd` process are removed too (`sd` is rewritten from the sd.cpp backend; backup `.bak-before-forge-cleanup` when only these leftovers remain). The config is rewritten once, the old one is kept as `server-config.yaml.bak-before-forge-removal`. Forge folders on disk are not deleted. Linux ARM, Intel Mac and Windows ARM have no sd.cpp build and so no image generation any more. `GET /` no longer reports the `forge` feature
- The Forge-compatible routes of the sd.cpp backend (`/api/sd/sdapi/v1/*`, including options, progress and interrupt): they answer 404. Imagecaster uses the job API
- Tray item "Open Stable Diffusion in the browser": with sd.cpp active it opened a 404 (the engine's own page is not proxied), and the Imagecaster app is the UI anyway. The item is gone; the engine's built-in page stays on loopback `127.0.0.1:7861`, unreachable from the network

## Earlier versions

Versions 1.0.0 – 1.2.0 (May – November 2026) ran the image engine on Forge / Forge Neo and text models on the Ollama program, shipped a Docker image and an embedded web panel, and were released for more platforms. That stack is gone: the server moved to stable-diffusion.cpp and llama.cpp, Docker and the web panel were removed, and releases are built for Windows x64, Linux x64 and macOS arm64 only. The section above describes the move; older details are in the git history (tags up to `v1.2.0`).
