# Imagecaster Server — документация

[English](server-en.md) · [README](../README-ru.md)

Imagecaster Server запускает stable-diffusion.cpp (или Forge Neo) и llama.cpp для приложения Imagecaster на компьютере с видеокартой. Он ставит их в одну папку данных, запускает и перезапускает, проверяет их работу, пропускает все запросы к GPU через одну очередь с бюджетом видеопамяти и сообщает о себе в локальной сети через mDNS.

## Содержание

- [Установка и первый запуск](#установка-и-первый-запуск)
- [Режимы работы](#режимы-работы)
- [Папка данных](#папка-данных)
- [Что устанавливается](#что-устанавливается)
- [Настройка](#настройка)
- [Очередь GPU и сторож портов](#очередь-gpu-и-сторож-портов)
- [API](#api)
- [Компоненты](#компоненты)
- [Ограничения и что не проверено](#ограничения-и-что-не-проверено)

## Установка и первый запуск

Архивы релизов (GitHub Releases, тег `v*`):

| Система | Архив | Внутри |
|--------|---------|--------|
| Windows amd64/arm64 | `.zip` | `imagecaster-server.exe` (окно установки + трей), `imagecaster-server-console.exe` (терминальный интерфейс) |
| Linux amd64/arm64 | `.tar.gz` | `imagecaster-server` |
| macOS amd64/arm64 | `.tar.gz` | `imagecaster-server`, `Imagecaster Server.command` |

Рядом с каждым архивом лежит файл `.sha256`.

**Windows.** `imagecaster-server.exe` открывает окно установки. Шаги: приветствие (видеокарта по данным `nvidia-smi`), папка данных (со свободным местом), ход установки (stable-diffusion.cpp, llama.cpp, первый запуск), готово (адрес для приложения). Дальше сервер работает со значком в трее. После нажатия **Установить** окно можно закрыть в любой момент: установка продолжится, а трей покажет состояние. При следующих запусках сервер сразу уходит в трей, если всё установлено, или открывает шаг установки, если она не закончена. Второй запуск с той же папкой покажет «уже запущен». Если порт 8080 занят, будет понятная ошибка. Дочерние процессы работают без окон консоли и останавливаются вместе с сервером (через объект задания Windows).

Меню трея: состояние Stable Diffusion и LLM, адрес (по щелчку копируется), открыть Stable Diffusion в браузере (`http://127.0.0.1:7860`), **Панель управления** (запускает `imagecaster-server-console.exe attach` в консоли), журнал (`<data>/logs/server.log` в Блокноте), **Выход** (сначала спросит, если в очереди GPU есть задачи).

**Linux.** `./imagecaster-server` — при первом запуске спросит папку данных (Enter оставляет папку по умолчанию), всё установит и покажет панель.

**macOS.** `Imagecaster Server.command` снимает атрибут карантина с вложенного бинарника и запускает его в Терминале (в первый раз: правый щелчок → Открыть).

Ничего не запускается вместе с системой: нет служб, записей в реестре и агентов запуска.

## Режимы работы

| Режим | Команда | Что это |
|------|---------|------------|
| Окно + трей | `imagecaster-server.exe` (сборка для Windows) | окно установки, затем трей |
| Терминальный интерфейс | `imagecaster-server`, `imagecaster-server-console.exe` | панель: сервисы, состояние, GPU, очередь, журналы |
| Подключение | `imagecaster-server attach [host[:port]]` | панель уже запущенного сервера (по умолчанию `127.0.0.1:8080`) |
| Без интерфейса | `imagecaster-server --headless` | без интерфейса, журнал в stdout (systemd); так же работает, когда stdin не терминал. API доступен сразу, установка идёт в фоне (`/api/server/install/status`) |

Флаги: `--data DIR`, `--config FILE` (по умолчанию `<data>/server-config.yaml`), `--port N`, `--headless`, `--version`. Оконная сборка для Windows не запускает терминальный интерфейс и открывает окно, если не указан `--headless`.

### Клавиши терминального интерфейса

| Экран | Клавиши |
|--------|------|
| Главный | `j`/`k` выбор, `s` запуск/остановка, `r` перезапуск, `l` журнал сервиса, `g` очередь GPU, `t` журнал сервера, `q` выход (при подключении — отключиться) |
| Журналы / журнал сервера | `j`/`k`, `pgup`/`pgdn`, `g`/`G` прокрутка, `q`/`esc` назад |
| Очередь GPU | `j`/`k` выбор, `u`/`d` приоритет, `x` отменить/освободить, `q`/`esc` назад |

### Подключение

Подключает панель к работающему серверу через его REST API, например с настольного компьютера к компьютеру с GPU. Опрашивает сервер раз в 3 с. Если связь пропала, на экране остаётся последнее состояние и плашка о переподключении (интервал растёт до 30 с). `q` закрывает только клиент.

### Язык

Английский или русский. Порядок выбора: `IMAGECASTER_LANG=ru|en` → `lang:` в конфиге → в Windows язык интерфейса пользователя (`GetUserDefaultUILanguage`) → `LC_ALL`/`LC_MESSAGES`/`LANG` → английский.

## Папка данных

Всё лежит в одной папке:

```
<data>/
  server-config.yaml
  engines/sd-cpp-<ОС>/        stable-diffusion.cpp (sd-server) и библиотеки CUDA (суффикс платформы: sd-cpp-windows, sd-cpp-linux)
  engines/onnxruntime/       ONNX Runtime для распознавания позы
  models/checkpoints/        модели из одного файла
  models/text_encoders/      текстовые энкодеры и токенизаторы
  models/vae/, models/loras/, models/upscalers/
  models/openpose/           yolox_l.onnx, dw-ll_ucoco_384.onnx
  sd-webui-forge-neo/        Forge Neo, только если выбран откат на него
  python/                    Python 3.13 managed by uv
  bin/uv(.exe)               uv 0.12.19
  uv-cache/                  uv package cache (PyTorch wheels)
  engines/llama-cpp-<ОС>/   llama.cpp (llama-server) для текстовых моделей (суффикс платформы, как у sd-cpp)
  llamacpp-models.ini        model list for llama-server, written by the server from models/ollama
  models/ollama/             text models in the Ollama layout (manifests + blobs), used by llama.cpp (`llm.models_dir`)
  logs/server.log            server log (Windows window build)
  imagecaster-server.lock    one server per folder (Windows window build)
```

Какая папка: `--data` → папка, выбранная при первом запуске (запоминается в `<user config dir>/imagecaster-server/data-dir`) → `~/imagecaster-server` → существующая `~/sd-studio-server` (старое название). `SD_DATA_DIR` важнее значения из конфига.

## Что устанавливается

**stable-diffusion.cpp** (`sdcpp`, по умолчанию). Сервер скачивает готовую сборку `sd-server` версии `master-1086-465adf3` под систему и видеокарту и проверяет sha256 каждого файла:

| Система | Видеокарта NVIDIA (`nvidia-smi -L`) | Другая видеокарта с Vulkan | Без видеокарты |
|---|---|---|---|
| Windows x64 | сборка CUDA 12 upstream + библиотеки CUDA (~0,9 ГБ) | сборка Vulkan upstream (есть `System32\vulkan-1.dll`) | сборка для процессора upstream |
| Linux x64 | своя сборка CUDA 12.8 (`sdcpp-linux-x86_64-cuda.tar.gz` из релиза `sdcpp-465adf3` этого репозитория) | своя сборка Vulkan (есть `/dev/dri/renderD*` и `libvulkan.so.1`) | своя сборка для процессора |
| macOS arm64 | сборка upstream (Metal) | | |

Свои Linux-сборки нужны потому, что сборки upstream требуют glibc 2.43 (Ubuntu 26.04) и не запускаются на Debian 13 и Ubuntu 24.04. Их собирает `.github/workflows/sdcpp.yml` (`tools/sdcpp-build.sh`, Ubuntu 22.04, glibc 2.35). Linux arm64, macOS Intel и Windows arm64 не поддерживаются: на них по умолчанию ставится Forge Neo.

Вместе с движком ставятся ONNX Runtime 1.31.1 (в `engines/onnxruntime`) и модели DWPose с Hugging Face (`yzd-v/DWPose`, в `pose_models_dir`). Обрыв загрузки докачивается с того же места, уже лежащий файл с верной контрольной суммой не скачивается. Готовая установка отмечается файлом `engines/sd-cpp-<ОС>/.installed` с версией. Версия движка задаётся сервером, а не конфигом: новая версия сервера с новой версией sd.cpp ставит движок заново поверх (значение `install.version` в конфиге при загрузке заменяется). Модели картинок установщик не качает — их скачивает Imagecaster. Каталоги движков sd.cpp и llama.cpp получают суффикс платформы (`sd-cpp-windows`, `llama-cpp-linux`, …): папка данных может быть общим томом у Windows и Linux, а сборки движков несовместимы между ОС — каждая держит свою. Старый каталог без суффикса при первом запуске переносится на новое имя, если в нём лежит бинарник текущей ОС (чужая сборка остаётся на месте), пути в старых конфигах обновляются автоматически.

Если у старого конфига нет секции `sdcpp`, а папка моделей Forge есть на диске, `sdcpp` берёт её папки как есть (`Stable-diffusion`, `Lora`, `VAE`, `text_encoder`, `ControlNetPreprocessor/openpose`, `ESRGAN`), ничего не копируя. Если папки активного движка нет, берётся папка прежнего активного движка, затем Forge Neo, затем любого другого движка из конфига.

**Forge Neo** 2.31 (`forge-neo`, вариант отката: `active_sd: forge-neo`, [Haoming02/sd-webui-forge-classic](https://github.com/Haoming02/sd-webui-forge-classic), zip-архив тега). После распаковки ставятся git (если его нет), uv (закреплённая версия, контрольная сумма проверяется) и выполняется `uv venv venv --python 3.13 --managed-python --seed`. Forge Neo запускается командой `launch.py --listen --api --uv` и при первом запуске сам ставит PyTorch и свои зависимости (по умолчанию сборка под CUDA 13; на macOS `TORCH_COMMAND` ставит обычный torch). Сервер задаёт для него `VIRTUAL_ENV` и папки кэша и Python для uv. Если настройка сорвалась после скачивания, следующий запуск только доделает её. Бэкенд, у которого бинарник уже есть, никогда не переустанавливается.

**llama.cpp** (`active_llm: llamacpp`, по умолчанию для новых установок). Сервер скачивает готовую сборку `llama-server` версии `b12283` в `engines/llama-cpp-<ОС>` и проверяет sha256:

| Система | NVIDIA | Vulkan | Без видеокарты |
|---|---|---|---|
| Windows x64 | `win-cuda-12.4` (библиотеки CUDA берутся из `engines/sd-cpp-<ОС>`, если их там нет — качается `cudart-llama-bin-win-cuda-12.4`, 391 МБ) | `win-vulkan` | `win-cpu` |
| Linux x64 | `ubuntu-cuda-12.8` (библиотеки CUDA — из `engines/sd-cpp-<ОС>`, иначе `cudart-…-ubuntu-cuda-12.8`, 594 МБ) | `ubuntu-vulkan` | `ubuntu-x64` |
| macOS | `macos-arm64` / `macos-x64` (Metal) | | |

Сборки Linux требуют `libssl.so.3` в системе. `llama-server` работает в режиме нескольких моделей (router) на `127.0.0.1:11435`, а сервер сам отвечает на порту `11434` и на `/api/llm/` как Ollama (см. «API»). Модели берутся из `models/ollama` в формате Ollama как есть, без копирования: блоб модели и, у моделей с картинками, отдельный блоб projector. Модели, которые Ollama собрала в свой формат (например `qwen2.5vl:7b`, один блоб без projector), llama.cpp не открывает — запрос к ним и `/api/pull` отвечают понятной ошибкой; берите GGUF с hf.co (`hf.co/<user>/<repo>:<квант>`).

**Программа Ollama** не ставится и не запускается: текстовые модели работают только на llama.cpp. Для Linux ARM и Windows ARM сборки llama.cpp нет, поэтому там текстовых моделей нет: процесс `llm` не запускается, в `GET /` нет признака `llamacpp`, маршруты LLM отвечают 503.

**Модели установленной Ollama** используются без перекачки, только на чтение: сервер дополнительно читает папку `OLLAMA_MODELS` из своего окружения, иначе `~/.ollama/models` (Windows: `%USERPROFILE%\.ollama\models`), а на Linux ещё `/usr/share/ollama/.ollama/models`. Подключённые папки пишутся в лог. Если модель есть в обоих местах, берётся своя. Удалить такую модель через сервер нельзя (409 с подсказкой `ollama rm`); `pull` всегда качает в свою папку. Модели, которые llama.cpp не открывает, в списках не показываются (свои — показываются в `/api/server/models/llm` с `unsupported`, чтобы их можно было удалить). Сервер, запущенный службой под другим пользователем, видит домашнюю папку этого пользователя. Выключить: `llm.read_ollama_models: false`.

**Переход со старой версии.** Конфиг без поля `active_llm` (записан версией до llama.cpp) при первом запуске новой версии переводится на новые движки: `active_sd: sdcpp` и `active_llm: llamacpp`. Только на системах, для которых есть сборка sd.cpp (Windows x64, Linux x64, macOS на Apple Silicon); на остальных конфиг остаётся на Forge Neo. Прежний конфиг сохраняется рядом как `server-config.yaml.bak-before-sdcpp` (уже существующая копия не перезаписывается), комментарии в конфиге остаются. Модели не копируются и не качаются: sd.cpp берёт папки Forge (см. выше), llama.cpp — хранилище моделей Ollama (`models/ollama`, или папка из `OLLAMA_MODELS` старого процесса `ollama`). Forge Neo не удаляется. Если конфиг нельзя записать, переход действует только на этот запуск, причина пишется в журнал. Откат — вернуть копию конфига (пока копия `.bak-before-sdcpp` лежит рядом, конфиг без `active_llm` повторно не переводится и работает на Forge Neo) или поменять строку на `active_sd: forge-neo`. Конфиг, где `active_llm` уже есть, не трогается; папки моделей при следующих запусках берутся у того же прежнего движка, что и в первый раз.

**Старый процесс `ollama`** в конфиге подхватывается при запуске, файл не переписывается: его `OLLAMA_MODELS` становится `llm.models_dir`, `OLLAMA_KEEP_ALIVE` — `llm.keep_alive` (значения из `llm:` главнее), сам процесс отбрасывается, `active_llm: ollama` читается как `llamacpp`.

Старые конфиги: `active_sd: forge` (классический Forge) переключается на Forge Neo. Старая папка Forge остаётся на диске — перенесите её модели в `sd-webui-forge-neo/models`. Старая запись процесса `python` удаляется.

## Настройка

`<data>/server-config.yaml`. Если поле не указано, остаётся значение по умолчанию. Запись в словаре (`processes.sd`, `backends.forge-neo`), указанная в файле, заменяет запись по умолчанию целиком.

```yaml
port: 8080
mdns: true
lang: ""                      # "", "ru" или "en"
data_dir: /home/user/imagecaster-server
active_sd: forge-neo
active_llm: llamacpp          # всегда llamacpp
llm:
  context_size: 8192          # контекст каждой модели (-c), один на все запросы; options.num_ctx из запроса не учитывается
  models_max: 1               # сколько моделей держать загруженными одновременно
  models_dir: models/ollama   # папка текстовых моделей (раскладка Ollama); по умолчанию <data_dir>/models/ollama
  keep_alive: 24h             # сколько модель держится загруженной без keep_alive в запросе; на картах меньше 24 ГБ — 5m
  read_ollama_models: true    # также брать модели установленной Ollama, только на чтение

gpu:
  total_budget_mb: 0          # 0 = найденная видеопамять - reserve_mb; <= 0 выключает очередь
  reserve_mb: 500
  max_wait_seconds: 120       # ждать бюджет, затем 503 + Retry-After
  lease_ttl_seconds: 90
  sd_default_weight_mb: 11000 # вес задачи SD, когда чекпоинт неизвестен
  sd_overhead_mb: 4500        # вес SD = размер файла чекпоинта + это
  llm_weight_mb: 11000
  guard_ports: true           # сторож портов, см. ниже

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
  llm:                        # llama-server, запускается только при active_llm: llamacpp
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

Относительные пути считаются от папки данных.

**Поля процесса**: `name`, `binary`, `args`, `env`, `workdir`, `health_url`, `target_url`, `proxy_path`, `internal_port` (локальный порт за сторожем портов; по умолчанию публичный + 1), `autostart`, `restart`, `max_restart`, `install`, `category` (`utility` — показывается отдельным блоком на панели).

**Поля бэкенда**: `name`, `process_key` (какой процесс его запускает), `binary`, `args`, `workdir`, `models_dir`, `lora_dir`, `vae_dir`, `embedding_dir`, `auto_optimize` (подбирать флаги памяти по объёму видеопамяти), `install` (`method: zip`, `url`, `target`, `version`).

**Бэкенд stable-diffusion.cpp** (`kind: sdcpp`). Сервер запускает `sd-server` и отвечает на `/api/sd/` в формате Forge, поэтому клиентам ничего менять не нужно. Дополнительные поля:
- `text_encoder_dir`, `upscaler_dir`, `tokenizer_dir` — папки моделей;
- `aliases` — имя модели и файлы, из которых она собирается;
- модель из нескольких файлов собирается и без псевдонима: если в `forge_additional_modules` пришёл энкодер текста, запускается `--diffusion-model <модель> --llm <энкодер> --vae <VAE>`, файл `.json` среди модулей уходит в `--tokenizer`; только VAE — `--model <модель> --vae <VAE>`. Имена без пути ищутся в `vae_dir`, `text_encoder_dir`, затем `tokenizer_dir`;
- `pose_models_dir` — папка с `yolox_l.onnx` и `dw-ll_ucoco_384.onnx` для `/controlnet/detect` (`dw_openpose_full`). По умолчанию `<models_dir>/../ControlNetPreprocessor/openpose`;
- `onnxruntime_lib` — путь к библиотеке ONNX Runtime. Если не указан, библиотека ищется рядом с исполняемым файлом сервера (и в папке `onnxruntime/` рядом с ним), а в Linux ещё и системным загрузчиком. Если библиотеки или моделей нет, `/controlnet/detect` отвечает 503, остальной сервер работает.

Переопределение через переменные окружения: `SD_PORT`, `SD_DATA_DIR`, `SD_ACTIVE_BACKEND`.

## Очередь GPU и сторож портов

### Очередь

`gpuqueue` выполняет тяжёлые для GPU запросы, только пока в бюджете видеопамяти есть для них место. Остальные ждут в очереди по порядку (FIFO) с ручным приоритетом. Так движок картинок и движок текстовых моделей не загружают модели одновременно и не исчерпывают видеопамять.

| Путь | Метод | Вес |
|------|--------|--------|
| `/api/sd/sdapi/v1/txt2img`, `img2img`, `interrogate`, `extra-single-image`, `extra-batch-images` | POST | размер файла чекпоинта + `sd_overhead_mb` (чекпоинт отслеживается по `POST .../options`; если неизвестен — `sd_default_weight_mb`), не больше бюджета |
| `/api/llm/api/generate`, `api/chat`, `api/embed`, `api/embeddings`, `v1/chat/completions`, `v1/completions`, `v1/embeddings` | POST | `llm_weight_mb` |
| всё остальное | * | 0 — проходит свободно |

- У аренд есть срок жизни (TTL), он продлевается, пока идёт запрос. Аренда упавшего клиента освобождается сама.
- При внутренней ошибке очереди запрос пропускается, а в статус добавляется предупреждение (fail-open).
- Ожидание дольше `max_wait_seconds` → `503` с `Retry-After`.
- Состояние хранится в памяти. После перезапуска сервера очередь пустая.
- Управление — на панели (`g`), локально и при подключении.
- С llama.cpp перед тяжёлым запросом SD сервер выгружает простаивающие текстовые модели, если SD и LLM вместе не помещаются в бюджет (`sd + llm_weight_mb > бюджета` или бюджет неизвестен). Модель, которая сейчас отвечает на запрос, не выгружается. Снова она загрузится сама при следующем запросе (1,5–3 с).

### Сторож портов

При `gpu.guard_ports: true` (по умолчанию) сервер сам слушает публичный порт Forge `7860` и порт LLM с API Ollama `11434`, а запускаемые им сервисы переносит на `127.0.0.1:7861` / `127.0.0.1:11435`. Любая программа в локальной сети, которая обращается к этим портам напрямую, проходит через ту же очередь, что и `/api/sd/` и `/api/llm/`.

- Сторож работает только для сервисов с `autostart` и локальным `target_url`.
- Занятый публичный порт (например, Ollama, уже запущенная в системе) не трогается. В статусе очереди появляется предупреждение, а этот сервис используется как есть.
- Для Forge сервер заменяет `--listen/--port/--server-name` на `--port <internal>`, для llama-server — `--host 127.0.0.1 --port <internal>`.

### Lease API (внешние потребители GPU)

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

Тело запроса — не больше 1 МБ. `client` обрезается до 32 символов. `wait_seconds` не может быть больше `max_wait_seconds`.

## API

Всё по адресу `http://<host>:8080`. Авторизации нет — сервер рассчитан только на доверенную локальную сеть.

| Эндпоинт | Метод | Что делает |
|----------|--------|--------------|
| `/` | GET | `{name, version, status, features}`; `features` — активные движки и возможности: `sdcpp` или `forge`, `llamacpp` (если текстовые модели доступны), `download_stream`. Старые версии сервера поля не отдают |
| `/api/server/status` | GET | `processes`, `health`, `gpu`, `installs`, `models`, процессор и память компьютера `sys` |
| `/api/server/start/{name}` | POST | запустить процесс |
| `/api/server/stop/{name}` | POST | остановить процесс |
| `/api/server/restart/{name}` | POST | перезапустить процесс |
| `/api/server/logs/{name}` | GET | журнал процесса, `?lines=100` |
| `/api/server/install/status` | GET | состояние установки всех компонентов |
| `/api/server/install/status/{key}` | GET | состояние установки одного компонента |
| `/api/server/install/{key}` | POST | установить компонент |
| `/api/server/install/logs/{key}` | GET | журнал установки |
| `/api/server/backends/active` | GET | активный бэкенд `{key, name, process}` |
| `/api/server/models/sd` | GET / POST | список чекпоинтов / скачать `{url, filename}` |
| `/api/server/models/lora` | GET / POST | то же для LoRA |
| `/api/server/models/vae` | GET / POST | то же для VAE |
| `/api/server/models/text_encoder` | GET / POST | то же для энкодеров текста и токенизаторов (`.json`); у Forge — папка `models/text_encoder` |
| `/api/server/models/llm` | GET / POST | список текстовых моделей / скачать модель (тем же кодом, что `/api/pull`; без llama.cpp — 503) |
| `/api/server/models/llm/pull` | GET | скачать текстовую модель с ходом загрузки в потоке, `?name=` |
| `/api/server/models/download/stream` | GET | скачать модель с ходом загрузки в потоке, `?type=sd\|lora\|vae\|text_encoder&url=&filename=` |
| `/api/server/models/delete/{sd,lora,vae,text_encoder,llm}/{name}` | DELETE | удалить модель |
| `/api/gpu/*` | | очередь и аренды, см. выше |
| `/api/sd/*` | * | прокси к Forge Neo (через очередь) |
| `/api/llm/*` | * | API, совместимый с Ollama (через очередь): отвечает сам сервер поверх llama.cpp; без llama.cpp — 503 |

**Ollama API поверх llama.cpp** (`/api/llm/…` и порт `11434`): `GET /api/tags`, `GET /api/ps`, `POST /api/show`, `GET /api/version`, `POST /api/chat` и `POST /api/generate` (поток NDJSON по умолчанию, `stream:false` — один ответ; `images` уходят в llama-server как картинки; пустой `prompt`/`messages` загружает модель, а с `keep_alive: 0` выгружает), `POST /api/pull` (из `registry.ollama.ai` и `hf.co`, докачка, проверка sha256), `DELETE /api/delete`, а также `GET /v1/models`, `POST /v1/chat/completions` и `/v1/completions` с именами моделей в формате Ollama. `keep_alive` в запросе понимается как у Ollama (секунды, `"10m"`, `-1` — не выгружать, `0` — выгрузить сразу после ответа; если модель сейчас отвечает на другой запрос — сразу после него). Без `keep_alive` в запросе действует `llm.keep_alive` (по умолчанию 24h), а на картах меньше 24 ГБ — 5 минут. Размер контекста — `llm.context_size` (8192) для всех запросов: `options.num_ctx` не учитывается, и запрос длиннее контекста llama-server отклонит с ошибкой (у Ollama такой запрос с большим `num_ctx` проходит). llama-server всегда слушает `127.0.0.1:11435`; если порт `11434` занят (например, Ollama уже работает в системе как служба), сервер его не трогает, а Ollama API доступен через `http://<host>:8080/api/llm/`. Системный промпт и параметры из модели Ollama (`system`, `params`) подставляются, если их нет в запросе; шаблон Ollama не используется — llama.cpp берёт шаблон из самого GGUF.

mDNS: сервис `_imagecaster._tcp`, экземпляр «Imagecaster Server», TXT `version=<version>`. `mdns: false` выключает объявление.

## Компоненты

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
installer/         stable-diffusion.cpp, ONNX Runtime, DWPose (release.go), llama.cpp (llamacpp.go), Forge Neo (zip + uv venv), uv, git
llamacpp/          Ollama API over llama-server router: model store, pull, chat/generate conversion, keep_alive
llm_wire.go        llama.cpp wiring: facade on /api/llm/, unload before SD jobs, /api/server/models/llm*
models/            checkpoints, LoRA, VAE, Ollama models
process/           process lifecycle, logs, restarts
tui/               terminal dashboard (Bubble Tea)
```

## Ограничения и что не проверено

- Веб-интерфейс Forge, открытый напрямую на `:7860`, проходит через сторож, но не все его запросы на генерацию взвешиваются очередью (только пути API из таблицы).
- На реальном железе пока не проверены: окно и трей в Windows, запуск на macOS и Forge Neo на macOS. PyTorch под CUDA 13 требует свежий драйвер NVIDIA (какой минимальный — не проверено).
- Около 8 ГБ без моделей — оценка: sd.cpp с библиотеками CUDA, ONNX Runtime и DWPose — до 3 ГБ, llama.cpp — 0,2–1 ГБ (с CUDA; без общих библиотек sd.cpp — до 1,4 ГБ). Forge Neo займёт около 20 ГБ.
- С llama.cpp: эмбеддинги (`/api/embed`) не поддерживаются (501); `tools` в `/api/chat` не передаются; размер контекста задаётся один на все модели (`llm.context_size`), `options.num_ctx` из запроса не меняет его. Общие библиотеки CUDA sd.cpp и llama.cpp на Windows не проверены вживую (версии совпадают: cudart 12.8 у sd.cpp, сборка llama.cpp под 12.4).
- Минимальная версия драйвера NVIDIA для сборок CUDA 12 не проверена; при старом драйвере sd-server не запустится — обновите драйвер или переключитесь на Forge Neo.
- Авторизации нет: не открывайте API за пределы доверенной локальной сети. Идентификаторы аренд не привязаны к владельцу — любой клиент в сети может продлить или отменить любую аренду.
