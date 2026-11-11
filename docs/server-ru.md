# Imagecaster Server — документация

[English](server-en.md) · [README](../README-ru.md)

Imagecaster Server запускает Stable Diffusion Forge Neo и Ollama для приложения Imagecaster на компьютере с видеокартой. Он ставит их в одну папку данных, запускает и перезапускает, проверяет их работу, пропускает все запросы к GPU через одну очередь с бюджетом видеопамяти и сообщает о себе в локальной сети через mDNS.

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

**Windows.** `imagecaster-server.exe` открывает окно установки. Шаги: приветствие (видеокарта по данным `nvidia-smi`), папка данных (со свободным местом), ход установки (Forge Neo, Ollama, первый запуск Forge Neo со скачиванием PyTorch), готово (адрес для приложения). Дальше сервер работает со значком в трее. После нажатия **Установить** окно можно закрыть в любой момент: установка продолжится, а трей покажет состояние. При следующих запусках сервер сразу уходит в трей, если всё установлено, или открывает шаг установки, если она не закончена. Второй запуск с той же папкой покажет «уже запущен». Если порт 8080 занят, будет понятная ошибка. Дочерние процессы работают без окон консоли и останавливаются вместе с сервером (через объект задания Windows).

Меню трея: состояние Forge Neo и Ollama, адрес (по щелчку копируется), открыть Forge Neo в браузере (`http://127.0.0.1:7860`), **Панель управления** (запускает `imagecaster-server-console.exe attach` в консоли), журнал (`<data>/logs/server.log` в Блокноте), **Выход** (сначала спросит, если в очереди GPU есть задачи).

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
  sd-webui-forge-neo/        Forge Neo (venv/ inside, models/ for checkpoints, LoRA, VAE)
  python/                    Python 3.13 managed by uv
  bin/uv(.exe)               uv 0.12.19
  uv-cache/                  uv package cache (PyTorch wheels)
  ollama/                    Ollama when it isn't installed in the system
  models/ollama/             Ollama models
  logs/server.log            server log (Windows window build)
  imagecaster-server.lock    one server per folder (Windows window build)
```

Какая папка: `--data` → папка, выбранная при первом запуске (запоминается в `<user config dir>/imagecaster-server/data-dir`) → `~/imagecaster-server` → существующая `~/sd-studio-server` (старое название). `SD_DATA_DIR` важнее значения из конфига.

## Что устанавливается

**Forge Neo** 2.31 (`forge-neo`, [Haoming02/sd-webui-forge-classic](https://github.com/Haoming02/sd-webui-forge-classic), zip-архив тега). После распаковки ставятся git (если его нет), uv (закреплённая версия, контрольная сумма проверяется) и выполняется `uv venv venv --python 3.13 --managed-python --seed`. Forge Neo запускается командой `launch.py --listen --api --uv` и при первом запуске сам ставит PyTorch и свои зависимости (по умолчанию сборка под CUDA 13; на macOS `TORCH_COMMAND` ставит обычный torch). Сервер задаёт для него `VIRTUAL_ENV` и папки кэша и Python для uv. Если настройка сорвалась после скачивания, следующий запуск только доделает её. Бэкенд, у которого бинарник уже есть, никогда не переустанавливается.

**Ollama**: если Ollama уже есть в системе (или уже работает на 11434), используется она. Иначе она ставится в `<data>/ollama`: Windows — отдельный zip (без установки в систему и без автозапуска), Linux — архив `.tar.zst`, macOS — Homebrew или универсальный `ollama-darwin.tgz`. Распаковка идёт сначала во временную папку, поэтому прерванная установка выполняется заново.

Старые конфиги: `active_sd: forge` (классический Forge) переключается на Forge Neo. Старая папка Forge остаётся на диске — перенесите её модели в `sd-webui-forge-neo/models`. Старая запись процесса `python` удаляется.

## Настройка

`<data>/server-config.yaml`. Если поле не указано, остаётся значение по умолчанию. Запись в словаре (`processes.sd`, `backends.forge-neo`), указанная в файле, заменяет запись по умолчанию целиком.

```yaml
port: 8080
mdns: true
lang: ""                      # "", "ru" или "en"
data_dir: /home/user/imagecaster-server
active_sd: forge-neo

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

Относительные пути считаются от папки данных.

**Поля процесса**: `name`, `binary`, `args`, `env`, `workdir`, `health_url`, `target_url`, `proxy_path`, `internal_port` (локальный порт за сторожем портов; по умолчанию публичный + 1), `autostart`, `restart`, `max_restart`, `install`, `category` (`utility` — показывается отдельным блоком на панели).

**Поля бэкенда**: `name`, `process_key` (какой процесс его запускает), `binary`, `args`, `workdir`, `models_dir`, `lora_dir`, `vae_dir`, `embedding_dir`, `auto_optimize` (подбирать флаги памяти по объёму видеопамяти), `install` (`method: zip`, `url`, `target`, `version`).

**Бэкенд stable-diffusion.cpp** (`kind: sdcpp`). Сервер запускает `sd-server` и отвечает на `/api/sd/` в формате Forge, поэтому клиентам ничего менять не нужно. Дополнительные поля:
- `text_encoder_dir`, `upscaler_dir`, `tokenizer_dir` — папки моделей;
- `aliases` — имя модели и файлы, из которых она собирается;
- `pose_models_dir` — папка с `yolox_l.onnx` и `dw-ll_ucoco_384.onnx` для `/controlnet/detect` (`dw_openpose_full`). По умолчанию `<models_dir>/../ControlNetPreprocessor/openpose`;
- `onnxruntime_lib` — путь к библиотеке ONNX Runtime. Если не указан, библиотека ищется рядом с исполняемым файлом сервера (и в папке `onnxruntime/` рядом с ним), а в Linux ещё и системным загрузчиком. Если библиотеки или моделей нет, `/controlnet/detect` отвечает 503, остальной сервер работает.

Переопределение через переменные окружения: `SD_PORT`, `SD_DATA_DIR`, `SD_ACTIVE_BACKEND`.

## Очередь GPU и сторож портов

### Очередь

`gpuqueue` выполняет тяжёлые для GPU запросы, только пока в бюджете видеопамяти есть для них место. Остальные ждут в очереди по порядку (FIFO) с ручным приоритетом. Так Forge Neo и Ollama не загружают модели одновременно и не исчерпывают видеопамять.

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

### Сторож портов

При `gpu.guard_ports: true` (по умолчанию) сервер сам слушает публичный порт Forge `7860` и порт Ollama `11434`, а запускаемые им сервисы переносит на `127.0.0.1:7861` / `127.0.0.1:11435`. Любая программа в локальной сети, которая обращается к Forge или Ollama напрямую, проходит через ту же очередь, что и `/api/sd/` и `/api/llm/`.

- Сторож работает только для сервисов с `autostart` и локальным `target_url`.
- Занятый публичный порт (например, Ollama, уже запущенная в системе) не трогается. В статусе очереди появляется предупреждение, а этот сервис используется как есть.
- Для Forge сервер заменяет `--listen/--port/--server-name` на `--port <internal>`, для Ollama задаёт `OLLAMA_HOST=127.0.0.1:<internal>`.

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
| `/` | GET | `{name, version, status}` |
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
| `/api/server/models/llm` | GET / POST | список моделей Ollama / скачать модель |
| `/api/server/models/llm/pull` | GET | скачать модель Ollama с ходом загрузки в потоке, `?name=` |
| `/api/server/models/download/stream` | GET | скачать модель с ходом загрузки в потоке, `?type=sd\|lora\|vae&url=&filename=` |
| `/api/server/models/delete/{sd,lora,vae,llm}/{name}` | DELETE | удалить модель |
| `/api/gpu/*` | | очередь и аренды, см. выше |
| `/api/sd/*` | * | прокси к Forge Neo (через очередь) |
| `/api/llm/*` | * | прокси к Ollama (через очередь) |

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
installer/         Forge Neo (zip + uv venv), uv, git, Ollama
models/            checkpoints, LoRA, VAE, Ollama models
process/           process lifecycle, logs, restarts
tui/               terminal dashboard (Bubble Tea)
```

## Ограничения и что не проверено

- Веб-интерфейс Forge, открытый напрямую на `:7860`, проходит через сторож, но не все его запросы на генерацию взвешиваются очередью (только пути API из таблицы).
- На реальном железе пока не проверены: окно и трей в Windows, запуск на macOS и Forge Neo на macOS. PyTorch под CUDA 13 требует свежий драйвер NVIDIA (какой минимальный — не проверено).
- Около 20 ГБ без моделей — это оценка (PyTorch с библиотеками CUDA, кэш uv, Ollama).
- Авторизации нет: не открывайте API за пределы доверенной локальной сети. Идентификаторы аренд не привязаны к владельцу — любой клиент в сети может продлить или отменить любую аренду.
