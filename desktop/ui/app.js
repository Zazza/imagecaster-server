'use strict'

const api = () => window.go.desktop.Window
const $ = (id) => document.getElementById(id)

const I18N = {
  en: {
    'sub': 'Setup',
    'welcome.title': 'Welcome',
    'welcome.text': 'Imagecaster Server runs image generation and the text model on this computer’s graphics card for the Imagecaster app. Everything goes into one folder; nothing is installed system-wide and nothing starts with Windows.',
    'welcome.neo': 'stable-diffusion.cpp — image generation (about 1 GB; models are downloaded later from Imagecaster)',
    'welcome.ollama': 'Ollama — text model; the one already installed on this computer is used if present',
    'gpu': 'Graphics card: {gpu}, {vram} GB of video memory.',
    'home.title': 'Data folder',
    'home.text': 'The image engine, Ollama and the models will be stored here — models take tens of GB.',
    'home.browse': 'Browse…',
    'home.need': 'About {need} GB is needed without models; free space could not be determined.',
    'home.free': '{free} GB free, about {need} GB needed without models.',
    'home.low': ' There may not be enough space.',
    'home.choose': 'Choose a data folder',
    'install.title': 'Installing',
    'install.failed': 'The installation did not finish',
    'install.retryHint': 'What has been downloaded is kept — “Retry” continues from there.',
    'install.bg': 'You can close this window: the installation continues in the background, the icon is in the tray (by the clock).',
    'install.more': 'Details ▾',
    'install.less': 'Hide details ▴',
    'stage.first': 'First start of Stable Diffusion',
    'sd.failed': 'Stable Diffusion stopped after several attempts — details in the log.',
    'p.downloading': 'downloading',
    'p.extracting': 'extracting',
    'p.installing': 'installing',
    'p.starting': 'starting',
    'p.done': 'done',
    'p.failed': 'failed',
    'done.title': 'Ready',
    'done.text': 'The server runs in the background; its icon is in the tray (by the clock). From there: status, Stable Diffusion, log and exit. Next time start it from the same file.',
    'done.addr': 'Imagecaster finds the server on the network by itself; if not, enter this address:',
    'done.copy': 'Copy',
    'done.copied': 'Copied',
    'btn.back': 'Back',
    'btn.retry': 'Retry',
    'btn.openlog': 'Open log',
    'btn.next': 'Next',
    'btn.install': 'Install',
    'btn.close': 'Close',
  },
  ru: {
    'sub': 'Установка',
    'welcome.title': 'Добро пожаловать',
    'welcome.text': 'Imagecaster Server запускает генерацию картинок и текстовую модель на видеокарте этого компьютера для приложения Imagecaster. Всё ставится в одну папку: в систему ничего не прописывается и с Windows не запускается.',
    'welcome.neo': 'stable-diffusion.cpp — генерация картинок (около 1 ГБ; модели скачиваются потом из Imagecaster)',
    'welcome.ollama': 'Ollama — текстовая модель; если она уже есть на компьютере, используется она',
    'gpu': 'Видеокарта: {gpu}, {vram} ГБ видеопамяти.',
    'home.title': 'Папка данных',
    'home.text': 'Здесь будут движок картинок, Ollama и модели — модели занимают десятки ГБ.',
    'home.browse': 'Выбрать…',
    'home.need': 'Нужно около {need} ГБ без моделей; свободное место узнать не удалось.',
    'home.free': 'Свободно {free} ГБ, нужно около {need} ГБ без моделей.',
    'home.low': ' Места может не хватить.',
    'home.choose': 'Выберите папку данных',
    'install.title': 'Установка',
    'install.failed': 'Установка не закончилась',
    'install.retryHint': 'Скачанное не пропадёт — «Повторить» продолжит с того же места.',
    'install.bg': 'Окно можно закрыть: установка продолжится в фоне, значок — в трее (у часов).',
    'install.more': 'Подробнее ▾',
    'install.less': 'Скрыть подробности ▴',
    'stage.first': 'Первый запуск Stable Diffusion',
    'sd.failed': 'Stable Diffusion остановился после нескольких попыток — подробности в журнале.',
    'p.downloading': 'скачивание',
    'p.extracting': 'распаковка',
    'p.installing': 'установка',
    'p.starting': 'запуск',
    'p.done': 'готово',
    'p.failed': 'ошибка',
    'done.title': 'Готово',
    'done.text': 'Сервер работает в фоне, его значок — в трее (у часов). Оттуда — состояние, Stable Diffusion, журнал и выход. В следующий раз запускайте тот же файл.',
    'done.addr': 'Imagecaster сам найдёт сервер в сети; если нет — укажите адрес:',
    'done.copy': 'Скопировать',
    'done.copied': 'Скопировано',
    'btn.back': 'Назад',
    'btn.retry': 'Повторить',
    'btn.openlog': 'Открыть журнал',
    'btn.next': 'Далее',
    'btn.install': 'Установить',
    'btn.close': 'Закрыть',
  },
}

let lang = 'en'
let info = null
let steps = []
let at = 0
let phase = 'choose'
let timer = null

function L(key, vars) {
  let s = (I18N[lang] && I18N[lang][key]) || I18N.en[key] || key
  for (const k in vars || {}) s = s.replace('{' + k + '}', vars[k])
  return s
}

function applyTexts() {
  document.documentElement.lang = lang
  for (const el of document.querySelectorAll('[data-i]')) el.textContent = L(el.dataset.i)
}

function show(name) {
  for (const s of document.querySelectorAll('.step')) s.hidden = s.id !== 's-' + name
}

function setError(text) {
  $('err').textContent = text || ''
  $('err').hidden = !text
}

function buttons() {
  const last = at === steps.length - 1
  $('back').hidden = phase !== 'choose' || at === 0
  $('retry').hidden = phase !== 'failed'
  $('openlog').hidden = phase !== 'failed'
  const next = $('next')
  next.hidden = phase === 'installing' || phase === 'failed'
  next.textContent = phase === 'done' ? L('btn.close') : last ? L('btn.install') : L('btn.next')
}

async function refreshFree() {
  const free = await api().FreeGB($('home').value)
  const el = $('free')
  if (free < 0) {
    el.textContent = L('home.need', { need: info.need_gb })
    el.className = 'hint'
    return
  }
  el.textContent = L('home.free', { free: Math.floor(free), need: info.need_gb })
  el.className = free < info.need_gb ? 'hint warn' : 'hint'
  if (free < info.need_gb) el.textContent += L('home.low')
}

function progressText(p) {
  if (!p) return ''
  const m = /^(\w+)(.*)$/.exec(p)
  if (!m) return p
  const key = 'p.' + m[1].toLowerCase()
  return I18N.en[key] ? L(key) + m[2] : p
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])
}

function render(st) {
  const items = (st.components || []).map((c) => {
    const p = c.state === 'run' ? progressText(c.progress) : ''
    return `<li class="${c.state}">${esc(c.label)}${p ? `<span class="p">${esc(p)}</span>` : ''}</li>`
  })
  let first = ''
  if (st.phase === 'starting') first = 'run'
  else if (st.phase === 'ready') first = 'ok'
  else if (st.phase === 'failed' && !st.error) first = 'fail'
  items.push(`<li class="${first}">${L('stage.first')}</li>`)
  $('stages').innerHTML = items.join('')
  const lines = st.lines || []
  const pre = $('log')
  pre.textContent = lines.join('\n')
  if (!pre.hidden) pre.scrollTop = pre.scrollHeight
  const last = lines.filter((l) => l.trim()).pop()
  $('now').textContent = last || ''

  const busy = (st.components || []).some((c) => c.state === 'run')
  if (st.phase === 'failed' && !busy) {
    phase = 'failed'
    $('inst-title').textContent = L('install.failed')
    $('fail-text').textContent = st.error || L('sd.failed')
    $('fail').hidden = false
  } else if (st.phase === 'ready') {
    phase = 'done'
    $('addr').textContent = st.addr
    show('done')
    stopPolling()
  } else {
    phase = 'installing'
    $('inst-title').textContent = L('install.title')
    $('fail').hidden = true
  }
  buttons()
}

async function poll() {
  try {
    render(await api().State())
  } catch (e) {
    setError(String(e))
  }
}

function startPolling() {
  stopPolling()
  poll()
  timer = setInterval(poll, 800)
}

function stopPolling() {
  if (timer) clearInterval(timer)
  timer = null
}

async function start() {
  setError('')
  $('next').disabled = true
  const err = await api().Start($('home').value.trim())
  $('next').disabled = false
  if (err) {
    setError(err)
    return
  }
  phase = 'installing'
  show('install')
  buttons()
  startPolling()
}

$('next').addEventListener('click', async () => {
  if (phase === 'done') {
    api().Close()
    return
  }
  if (steps[at] === 'home' && !$('home').value.trim()) {
    setError(L('home.choose'))
    return
  }
  setError('')
  if (at < steps.length - 1) {
    at++
    show(steps[at])
    buttons()
    if (steps[at] === 'home') refreshFree()
    return
  }
  await start()
})
$('back').addEventListener('click', () => {
  if (at > 0) at--
  setError('')
  show(steps[at])
  buttons()
})
$('retry').addEventListener('click', async () => {
  await api().Retry()
  phase = 'installing'
  $('fail').hidden = true
  buttons()
  startPolling()
})
$('openlog').addEventListener('click', () => api().OpenLog())
$('browse').addEventListener('click', async () => {
  $('home').value = await api().ChooseFolder($('home').value)
  refreshFree()
})
$('home').addEventListener('change', refreshFree)
$('copy').addEventListener('click', () => {
  api().Copy($('addr').textContent)
  $('copy').textContent = L('done.copied')
})
$('toggle-log').addEventListener('click', () => {
  const pre = $('log')
  pre.hidden = !pre.hidden
  $('toggle-log').textContent = pre.hidden ? L('install.more') : L('install.less')
  if (!pre.hidden) pre.scrollTop = pre.scrollHeight
})

async function init() {
  info = await api().Info()
  lang = info.lang === 'ru' ? 'ru' : 'en'
  applyTexts()
  $('ver').textContent = info.version
  const gpu = $('gpu')
  if (info.gpu_error) {
    gpu.textContent = info.gpu_error
    gpu.className = 'card bad'
  } else {
    gpu.textContent = L('gpu', { gpu: info.gpu, vram: info.vram_gb })
  }
  $('home').value = info.home
  if (!info.ask_home) {
    steps = ['install']
    at = 0
    phase = 'installing'
    show('install')
    buttons()
    startPolling()
    return
  }
  steps = ['welcome', 'home']
  at = 0
  show('welcome')
  buttons()
}

init()
