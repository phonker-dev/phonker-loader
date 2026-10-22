"use strict";
// Phonker Loader — окно. Раз в секунду забирает состояние программы (/api/state)
// и перерисовывает только то, что изменилось. Команды — POST-запросами к /api/....

const TOKEN = new URLSearchParams(location.search).get("t") || "";
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const LOST = "Нет связи с программой — похоже, она закрылась. Запусти её снова; если повторится — " +
  "в Настройках есть кнопка «Открыть журнал», пришли его разработчику.";

let state = null;
let tab = "download";
let botSub = "panel";
const cardUI = {};   // выбранный формат и фрагмент каждой карточки — переживают перерисовку
const rendered = {}; // что уже нарисовано: пока данные те же, поля ввода не трогаем
const lists = { history: null, stats: null, users: null, journal: null };

// ---------- запросы ----------

async function api(path, body) {
  const opts = { method: body === undefined ? "GET" : "POST", headers: { "X-Token": TOKEN } };
  if (body !== undefined) {
    if (body instanceof ArrayBuffer) {
      opts.body = body;
    } else {
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
  }
  const resp = await fetch("/api/" + path, opts);
  let data = {};
  try { data = await resp.json(); } catch (_) { /* пустой ответ */ }
  if (!resp.ok) throw new Error((data && data.error) || "ошибка " + resp.status);
  return data;
}

async function act(path, body, okText) {
  try {
    const res = await api(path, body);
    if (okText) toast(typeof okText === "function" ? okText(res) : okText, "ok");
    refresh();
    return res;
  } catch (e) {
    toast(e instanceof TypeError ? LOST : e.message, "err");
    return null;
  }
}

function toast(text, kind = "") {
  const el = document.createElement("div");
  el.className = kind;
  el.textContent = text;
  $("#toast").appendChild(el);
  setTimeout(() => el.remove(), kind === "err" ? 8000 : 3500);
}

// ---------- разделы ----------

$$("#nav button").forEach((b) => b.addEventListener("click", () => showTab(b.dataset.tab)));
$$("#bot-subtabs button").forEach((b) => b.addEventListener("click", () => showSub(b.dataset.sub)));

function showTab(name) {
  tab = name;
  $$("#nav button").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
  $$(".tab").forEach((s) => s.classList.toggle("active", s.id === "tab-" + name));
  if (name === "download") $("#url").focus();
  loadLists(true);
  render();
}

function showSub(name) {
  botSub = name;
  $$("#bot-subtabs button").forEach((b) => b.classList.toggle("active", b.dataset.sub === name));
  $$("#tab-bot .sub").forEach((s) => s.classList.toggle("active", s.id === "bot-" + name));
  loadLists(true);
  render();
}

// Ctrl+1…6 — разделы.
document.addEventListener("keydown", (e) => {
  if (!e.ctrlKey || e.altKey || e.shiftKey) return;
  const tabs = ["download", "bot", "history", "stats", "settings", "help"];
  const i = Number(e.key) - 1;
  if (tabs[i] === "bot" && state && !state.bot.visible) return;
  if (i >= 0 && i < tabs.length) { e.preventDefault(); showTab(tabs[i]); }
});

// Ctrl+V вне полей ввода — сразу ищем вставленную ссылку.
document.addEventListener("paste", (e) => {
  const t = e.target;
  if (t.tagName === "INPUT" && t.id !== "url") return;
  const text = (e.clipboardData || window.clipboardData).getData("text").trim();
  if (!looksLikeURL(text)) return;
  e.preventDefault();
  probe(text);
});

const looksLikeURL = (v) => /^https?:\/\/\S+$/i.test(v) || /^[\w.-]+\.[a-z]{2,}\/\S*$/i.test(v);

// ---------- опрос состояния ----------

let refreshTimer = null;
async function refresh() {
  clearTimeout(refreshTimer);
  try {
    state = await api("state");
    render();
  } catch (_) {
    rendered.status = null;
    $("#status").innerHTML = `<div class="item"><span class="label">связь</span><span class="value err">программа закрылась?</span></div>`;
  }
  refreshTimer = setTimeout(refresh, document.hidden ? 3000 : 900);
}

// Списки (история, статистика, пользователи, журнал) обновляются реже.
let listsAt = 0;
async function loadLists(force) {
  if (!force && Date.now() - listsAt < 5000) return;
  listsAt = Date.now();
  try {
    if (tab === "history" || tab === "download") lists.history = (await api("history")) || [];
    if (tab === "stats") lists.stats = await api("stats");
    if (tab === "bot" && botSub === "users") lists.users = (await api("users")) || [];
    if (tab === "bot" && botSub === "journal") lists.journal = (await api("journal")) || [];
    render();
  } catch (_) { /* в следующий раз */ }
}
setInterval(() => loadLists(false), 5000);

// ---------- отрисовка ----------

function render() {
  if (!state) return;
  renderSide();
  renderCrash();
  if (tab === "download") { renderDownloads(); renderRecent(); }
  if (tab === "bot") renderBot();
  if (tab === "history") renderHistory();
  if (tab === "stats") renderStats();
  if (tab === "settings") renderSettings();
}

function setHTML(el, key, html) {
  if (rendered[key] === html) return;
  rendered[key] = html;
  el.innerHTML = html;
}

function renderSide() {
  $("#version").textContent = "v" + state.version;
  const b = state.bot, p = state.proxy, y = state.ytdlp;
  const item = (label, value, cls = "", attrs = "") =>
    `<div class="item"><span class="label">${label}</span><span class="value ${cls}" ${attrs}>${value}</span></div>`;

  let bot = `<span class="dim">○ выключен</span>`;
  if (b.running) bot = `<span class="ok">● online</span> <span class="dim">@${esc(b.name)}</span>`;
  else if (b.starting) bot = `<span class="warn">◌ запускается</span>`;
  else if (!b.hasToken) bot = `<span class="dim">не настроен</span>`;

  let proxy = p.addr ? esc(p.source) : (p.mode === "off" ? "выключен" : "напрямую");
  const proxyCls = p.addr && !p.alive ? "err" : "";
  if (p.addr && !p.alive) proxy = "не отвечает";

  // Обычному пользователю бот не нужен: раздел скрыт, пока его не включат в Настройках.
  $('#nav [data-tab="bot"]').hidden = !b.visible;
  if (!b.visible && tab === "bot") showTab("download");
  const html =
    (b.visible ? item("бот", bot, "click", `data-goto="bot"`) : "") +
    (b.visible && b.requests && b.requests.length ? item("запросы", `<span class="warn">${b.requests.length} ждут ответа</span>`, "click", `data-goto="bot"`) : "") +
    item("прокси", proxy, proxyCls + " click", `data-goto="settings"`) +
    item("скачано", esc(state.summary), "click", `data-goto="stats"`) +
    item("yt-dlp", y.update ? `<span class="warn">вышла ${esc(y.update)}</span>` : esc(y.version), "click", `data-goto="settings"`);
  setHTML($("#status"), "status", html);

  const n = b.requests ? b.requests.length : 0;
  const badge = $("#bot-badge");
  badge.hidden = n === 0;
  badge.textContent = n;
}

function renderCrash() {
  setHTML($("#crash"), "crash", state.prevCrash ? `<div class="card danger">
      <h2>В прошлый раз программа аварийно закрылась</h2>
      <p>Текст ошибки сохранён в журнале. Пришли его разработчику — так проще всего найти причину.</p>
      <div class="actions"><button class="btn primary" data-action="open-log">Открыть журнал</button>
        <button class="btn ghost" data-action="crash-dismiss">Скрыть</button></div></div>` : "");
}

// ---------- скачать ----------

function bar(pct, width = 40) {
  const filled = Math.max(0, Math.min(width, Math.round((pct / 100) * width)));
  return `<div class="bar"><span class="fill">${"█".repeat(filled)}</span><span class="rest">${"█".repeat(width - filled)}</span></div>`;
}

function renderDownloads() {
  const root = $("#downloads");
  const list = state.downloads || [];
  const ids = list.map((d) => "dl" + d.id);
  $$(".card[id^=dl]", root).forEach((el) => { if (!ids.includes(el.id)) { el.remove(); delete rendered[el.id]; } });
  list.forEach((d, i) => {
    let el = $("#dl" + d.id);
    if (!el) {
      el = document.createElement("div");
      el.className = "card";
      el.id = "dl" + d.id;
    }
    if (root.children[i] !== el) root.insertBefore(el, root.children[i] || null);
    const ui = cardUI[d.id] || (cardUI[d.id] = { option: "", start: "", end: "" });
    if (d.options && d.options.length && !d.options.some((o) => o.key === ui.option)) ui.option = d.options[0].key;
    setHTML(el, el.id, cardHTML(d, ui));
  });
}

const STATUS_TAG = {
  probing: `<span class="tag">ищу</span>`,
  ready: `<span class="tag">готово к загрузке</span>`,
  downloading: `<span class="tag warn">качаю</span>`,
  done: `<span class="tag ok">готово</span>`,
  cancelled: `<span class="tag">отменено</span>`,
  error: `<span class="tag err">ошибка</span>`,
};

function cardHTML(d, ui) {
  const thumb = d.thumb ? `<img class="thumb ${d.music ? "square" : ""}" src="${esc(d.thumb)}" alt="" onerror="this.remove()">` : "";
  const meta = [d.author, d.duration, d.playlist ? `плейлист · ${d.count} видео` : "", d.music ? "музыка" : ""]
    .filter(Boolean).map(esc).join(" · ");
  const head = `<div class="top"><div style="min-width:0">
      <div class="title">${esc(d.title || d.url)}</div>
      ${meta ? `<div class="meta">${meta}</div>` : ""}
      ${d.title ? `<div class="url">${esc(d.url)}</div>` : ""}
    </div>${STATUS_TAG[d.status] || ""}</div>`;
  let body = "";

  switch (d.status) {
    case "probing":
      body = `<div class="progress"><div class="line"><span class="what"><span class="spinner"></span> Получаю информацию о видео...</span></div></div>
        <div class="actions"><button class="btn ghost" data-action="cancel" data-id="${d.id}">Отмена</button></div>`;
      break;
    case "ready": {
      const opts = d.options.map((o) =>
        `<button class="${o.key === ui.option ? "sel" : ""}" data-action="pick" data-id="${d.id}" data-key="${o.key}">${esc(o.label)}</button>`).join("");
      const frag = d.playlist ? "" : `<div class="section"><div class="label">Фрагмент — необязательно</div>
        <div class="frag">с <input class="short" data-field="start" data-id="${d.id}" placeholder="0:00" value="${esc(ui.start)}">
          до <input class="short" data-field="end" data-id="${d.id}" placeholder="${esc(d.duration || "конец")}" value="${esc(ui.end)}"></div></div>`;
      body = `<div class="section"><div class="label">${d.music ? "Видео здесь нет — формат" : "Формат"}</div>
          <div class="seg options">${opts}</div></div>${frag}
        <div class="actions">
          <button class="btn primary" data-action="start" data-id="${d.id}">${d.playlist ? "Скачать плейлист" : "Скачать"}</button>
          <button class="btn ghost" data-action="dismiss" data-id="${d.id}">Убрать</button>
        </div>`;
      break;
    }
    case "downloading": {
      const lines = (d.lines || []).map((l) => `<div class="meta">${esc(l)}</div>`).join("");
      const pct = d.percent >= 0;
      body = `${lines}<div class="progress">
          <div class="line"><span class="what">${pct ? "" : `<span class="spinner"></span> `}${esc(d.label)}${pct ? "" : "..."}</span>
            <span class="nums">${pct ? d.percent.toFixed(1) + "%  " : ""}${esc(d.detail)}</span></div>
          ${pct ? bar(d.percent) : ""}
        </div>
        <div class="actions"><button class="btn danger" data-action="cancel" data-id="${d.id}">Отмена</button></div>`;
      break;
    }
    case "done": {
      const file = d.files && d.files.length === 1 ? d.files[0] : "";
      const name = file ? file.split("\\").pop() : (d.files && d.files.length ? `файлов: ${d.files.length}` : "файл уже был скачан раньше");
      body = `<div class="section ok">✓ ${esc(name)}${d.size ? ` <span class="dim">· ${esc(d.size)}</span>` : ""}</div>
        ${d.hint ? `<div class="hint warn">${esc(d.hint)}</div>` : ""}
        <div class="actions">
          ${file ? `<button class="btn primary" data-action="play" data-file="${esc(file)}">Открыть</button>
          <button class="btn" data-action="reveal" data-file="${esc(file)}">Показать в папке</button>`
            : `<button class="btn" data-action="open-downloads">Открыть папку</button>`}
          <button class="btn ghost" data-action="dismiss" data-id="${d.id}">Убрать</button>
        </div>`;
      break;
    }
    case "cancelled":
      body = `<div class="actions">
          <button class="btn" data-action="retry" data-url="${esc(d.url)}">Заново</button>
          <button class="btn ghost" data-action="dismiss" data-id="${d.id}">Убрать</button></div>`;
      break;
    default: // error
      body = `<div class="section err">${esc(d.error)}</div>
        ${d.hint ? `<div class="hint">${esc(d.hint)}</div>` : ""}
        <div class="actions">
          <button class="btn" data-action="retry" data-url="${esc(d.url)}">Попробовать снова</button>
          <button class="btn ghost" data-action="dismiss" data-id="${d.id}">Убрать</button>
        </div>`;
  }
  return `<div class="dl">${thumb}<div class="body">${head}${body}</div></div>`;
}

// Под карточками — последние загрузки, чтобы страница не была пустой и можно было повторить.
function renderRecent() {
  const list = (lists.history || []).slice(0, 6);
  if (!list.length) {
    return setHTML($("#recent"), "recent", (state.downloads || []).length ? "" :
      `<div class="empty">Здесь появятся загрузки. Скопируй ссылку на видео и нажми Ctrl+V.</div>`);
  }
  const rows = list.map((h) => `<div class="recent-row">
      <span class="when">${esc(h.when.slice(5))}</span>
      <span class="what" title="${esc(h.title)}">${esc(h.title)}</span>
      <span class="size">${esc(h.size || "")}</span>
      ${h.exists ? `<button class="btn ghost small" data-action="reveal" data-file="${esc(h.file)}">в папке</button>` : ""}
      <button class="btn ghost small" data-action="retry" data-url="${esc(h.url)}">ещё раз</button>
    </div>`).join("");
  setHTML($("#recent"), "recent", `<div class="card"><h2>Недавние</h2>${rows}
    <div class="actions"><button class="btn ghost small" data-goto="history">Вся история →</button>
    <button class="btn ghost small" data-action="open-downloads">Открыть папку загрузок</button></div></div>`);
}

async function probe(url) {
  url = (url || "").trim();
  if (!url) return;
  $("#url").value = "";
  showTab("download");
  await act("probe", { url });
}

$("#probe-form").addEventListener("submit", (e) => { e.preventDefault(); probe($("#url").value); });
$("#url").addEventListener("paste", () => setTimeout(() => {
  const v = $("#url").value.trim();
  if (looksLikeURL(v)) probe(v);
}, 0));

// ---------- бот ----------

function renderBot() {
  const b = state.bot;
  if (botSub === "panel") {
    const root = $("#bot-panel");
    if (!(root.contains(document.activeElement) && document.activeElement.tagName === "INPUT")) {
      setHTML(root, "bot-panel", botPanelHTML(b));
    }
  }
  if (botSub === "users" && !(document.activeElement && document.activeElement.closest && document.activeElement.closest("summary"))) {
    setHTML($("#bot-users"), "bot-users", usersHTML());
  }
  if (botSub === "journal") setHTML($("#bot-journal"), "bot-journal", journalHTML());
  if (botSub === "botsettings") {
    const root = $("#bot-botsettings");
    if (!(root.contains(document.activeElement) && document.activeElement.tagName === "INPUT")) {
      setHTML(root, "bot-settings", botSettingsHTML(b));
    }
  }
}

function setupHTML() {
  return `<div class="card attention">
    <h2>Бот ещё не настроен — это займёт минуту</h2>
    <ol>
      <li>В Telegram открой <b>@BotFather</b> и отправь <code>/newbot</code>.</li>
      <li>Придумай имя и адрес бота (адрес заканчивается на <code>bot</code>).</li>
      <li>Вставь сюда токен, который пришлёт BotFather:</li>
    </ol>
    <div class="row" style="margin-top:12px"><input class="grow" id="token-input" placeholder="123456789:AAH..." spellcheck="false">
      <button class="btn primary" data-action="save-token">Сохранить</button></div>
  </div>`;
}

function botPanelHTML(b) {
  if (!b.hasToken) return setupHTML();
  let big, buttons;
  if (b.running) {
    big = `<div><div class="big on"><span class="dot"></span>ONLINE</div>
      <div class="dim" style="margin-top:4px"><a href="#" data-url="https://t.me/${esc(b.name)}">t.me/${esc(b.name)}</a> — ссылка на бота</div></div>`;
    buttons = `<button class="btn" data-action="bot-restart">Перезапустить</button>
      <button class="btn danger" data-action="bot-stop">Остановить</button>`;
  } else if (b.starting || b.stopping) {
    big = `<div class="big busy"><span class="dot"></span><span class="spinner"></span> ${b.starting ? "ЗАПУСК" : "ОСТАНОВКА"}</div>`;
    buttons = "";
  } else {
    big = `<div><div class="big off"><span class="dot"></span>OFFLINE</div>
      <div class="dim" style="margin-top:4px">У пользователей закреплён статус 🔴 Offline</div></div>`;
    buttons = `<button class="btn primary" data-action="bot-start">Запустить</button>`;
  }
  const err = b.error && !b.running ? `<div class="hint err">${esc(b.error)}</div>` : "";

  const tiles = `<div class="tiles">
      <div class="tile"><div class="n">${b.users}</div><div class="l">пользователей</div></div>
      <div class="tile"><div class="n">${b.running ? b.sent : "—"}</div><div class="l">отправлено</div></div>
      <div class="tile"><div class="n">${b.running ? esc(b.bytes) : "—"}</div><div class="l">объём</div></div>
      <div class="tile"><div class="n">${b.running ? b.active : "—"}</div><div class="l">качается сейчас</div></div>
    </div>`;

  let requests = "";
  if (b.requests && b.requests.length) {
    requests = `<div class="card attention"><h2>Запросы на доступ · ${b.requests.length}</h2>` +
      b.requests.map((r) => `<div class="request"><span class="name" title="${esc(r.name)}">${esc(r.name)}</span>
        <span class="dim">${esc(r.when)}</span>
        <button class="btn primary small" data-action="allow" data-id="${r.id}">Разрешить</button>
        <button class="btn danger small" data-action="deny" data-id="${r.id}">Отказать</button></div>`).join("") +
      (b.running ? "" : `<div class="hint">Бот выключен: пользователь получит уведомление о доступе при следующем запуске.</div>`) +
      `</div>`;
  }

  // Журнал событий хранится на диске: виден и после перезапуска, и при выключенном боте.
  const evList = b.events && b.events.length
    ? b.events.map((e) => `<div class="ev"><span class="t">${esc(e.time)}</span>
        <span class="x">${esc(e.text)}<span class="s ${e.kind === "ok" ? "ok" : e.kind === "err" ? "err" : "dim"}">${esc(e.status)}</span></span></div>`).join("")
    : `<div class="empty">Событий пока нет.</div>`;
  const events = `<div class="card"><h2>Журнал событий</h2><div class="log">${evList}</div></div>`;
  return `<div class="card"><div class="botstate">${big}<div class="row">${buttons}</div></div>${err}</div>
    ${requests}${tiles}${events}`;
}

// Пользователи бота: у каждого раскрывающаяся история со всеми ссылками, которые он присылал.
const openUsers = new Set(); // какие истории раскрыты — переживает перерисовку
document.addEventListener("toggle", (e) => {
  const d = e.target;
  if (!d.matches || !d.matches("details[data-user]")) return;
  if (d.open) openUsers.add(d.dataset.user); else openUsers.delete(d.dataset.user);
  rendered["bot-users"] = null;
}, true);

function linkRow(l) {
  const what = l.ok ? (l.title || l.url) : `ошибка: ${l.error || ""}`;
  return `<div class="link">
    <span class="t">${esc(l.time)}</span>
    <span class="w"><div class="title ${l.ok ? "" : "err"}" title="${esc(what)}">${l.audio ? `<span class="dim">[mp3]</span> ` : ""}${esc(what)}</div>
      <div class="u"><a href="#" data-url="${esc(l.url)}">${esc(l.url)}</a></div></span>
    <span class="s">${esc(l.size || "")}</span>
  </div>`;
}

function usersHTML() {
  const list = lists.users;
  if (!list) return `<div class="empty"><span class="spinner"></span> Загружаю...</div>`;
  if (!list.length) return `<div class="card"><div class="empty">Пользователей пока нет. Когда кто-то напишет боту, на панели появится запрос на доступ.</div></div>`;
  const items = list.map((u) => {
    const meta = [u.lang, u.premium ? "Premium" : "", u.firstSeen ? "с " + u.firstSeen : "", u.sites].filter(Boolean).join(" · ");
    const body = u.links.length
      ? `<div class="count">Ссылок: ${u.links.length} · новые сверху</div><div class="links">${u.links.map(linkRow).join("")}</div>`
      : `<div class="empty" style="padding:8px 0">Ссылок пока не присылал.</div>`;
    return `<details class="user" data-user="${u.id}" ${openUsers.has(String(u.id)) ? "open" : ""}>
      <summary>
        <span class="chev">›</span>
        <span class="who"><span class="name" title="${esc(u.name)}">${esc(u.name || "id " + u.id)}</span>${u.access ? "" : `<span class="tag">доступ убран</span>`}
          <span class="sub-line">${esc(meta)}</span></span>
        <span class="stat"><b>${u.downloads}</b>${u.failed ? ` <span class="err">/ ${u.failed} ош.</span>` : ""}</span>
        <span class="stat dim">${esc(u.bytes || "—")}</span>
        <span class="stat dim">${esc(u.lastSeen || "—")}</span>
        ${u.access
          ? `<button class="btn ghost small" data-action="remove-user" data-id="${u.id}" data-name="${esc(u.name)}">убрать доступ</button>`
          : `<button class="btn small" data-action="grant-user" data-id="${u.id}">вернуть доступ</button>`}
      </summary>
      <div class="user-body">${body}</div>
    </details>`;
  }).join("");
  return `<div class="users-head"><span></span><span>Пользователь</span><span>Загрузок</span><span>Объём</span><span>Был</span><span></span></div>${items}`;
}
function journalHTML() {
  const list = lists.journal;
  if (!list) return `<div class="empty"><span class="spinner"></span> Загружаю...</div>`;
  if (!list.length) return `<div class="card"><div class="empty">Журнал загрузок пуст.</div></div>`;
  const rows = list.map((j) => `<tr>
      <td class="nowrap dim">${esc(j.time)}</td>
      <td class="nowrap"><div class="clip" style="max-width:180px">${esc(j.who || "—")}</div></td>
      <td><div class="clip ${j.ok ? "" : "err"}">${j.ok ? "" : "ошибка: "}${j.audio ? `<span class="dim">[mp3]</span> ` : ""}${esc(j.what || j.url)}</div>
        <div class="clip"><a href="#" data-url="${esc(j.url)}" class="dim" style="font-size:12px">${esc(j.url)}</a></div></td>
      <td class="num dim">${esc(j.size || "")}</td>
    </tr>`).join("");
  return `<div class="card"><table>
    <thead><tr><th>Когда</th><th>Пользователь</th><th>Что и откуда</th><th class="num">Размер</th></tr></thead>
    <tbody>${rows}</tbody></table></div>`;
}
function botSettingsHTML(b) {
  const auto = { on: "включён", off: "выключен", other: "включён для другого расположения программы — включи заново" }[b.autostart];
  return `<div class="card">
      <h2>Токен бота</h2>
      <div class="kv"><span>Сейчас</span><span>${b.hasToken ? esc(b.token) : `<span class="warn">не указан</span>`}</span></div>
      <div class="row" style="margin-top:12px"><input class="grow" id="token-input" placeholder="новый токен от @BotFather" spellcheck="false">
        <button class="btn primary" data-action="save-token">Сохранить</button>
        ${b.hasToken ? `<button class="btn ghost" data-action="clear-token">Удалить</button>` : ""}</div>
      <div class="hint">Токен — как пароль от бота: не показывай его никому. Хранится только в settings.json рядом с программой.</div>
    </div>
    <div class="card">
      <h2>Автозапуск вместе с Windows</h2>
      <div class="kv"><span>Сейчас</span><span class="${b.autostart === "on" ? "ok" : b.autostart === "other" ? "warn" : "dim"}">${esc(auto)}</span></div>
      <div class="actions">
        <button class="btn ${b.autostart === "on" ? "" : "primary"}" data-action="autostart-on">Включить</button>
        <button class="btn ghost" data-action="autostart-off">Выключить</button>
      </div>
      <div class="hint">При входе в Windows откроется это окно и бот запустится сам.</div>
    </div>`;
}

// ---------- история ----------

$("#history-filter").addEventListener("input", () => { rendered.history = null; renderHistory(); });

function renderHistory() {
  const list = lists.history;
  const root = $("#history");
  if (!list) return setHTML(root, "history", `<div class="empty"><span class="spinner"></span> Загружаю...</div>`);
  const q = $("#history-filter").value.trim().toLowerCase();
  const items = q ? list.filter((h) => (h.title + " " + h.url).toLowerCase().includes(q)) : list;
  if (!items.length) {
    return setHTML(root, "history", `<div class="card"><div class="empty">${list.length ? "Ничего не найдено." : "История пуста — здесь появятся скачанные видео."}</div></div>`);
  }
  const rows = items.map((h) => `<tr>
      <td class="nowrap dim">${esc(h.when)}</td>
      <td><div class="clip" title="${esc(h.title)}">${esc(h.title)}</div><div class="clip dim" style="font-size:12px">${esc(h.url)}</div></td>
      <td class="num dim">${esc(h.size || "")}</td>
      <td class="num nowrap">
        ${h.exists ? `<button class="btn ghost small" data-action="reveal" data-file="${esc(h.file)}">в папке</button>` : ""}
        <button class="btn ghost small" data-action="retry" data-url="${esc(h.url)}">ещё раз</button>
      </td></tr>`).join("");
  setHTML(root, "history", `<div class="card"><table>
    <thead><tr><th>Когда</th><th>Что</th><th class="num">Размер</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table></div>`);
}

// ---------- статистика ----------

function renderStats() {
  const s = lists.stats;
  const root = $("#stats");
  if (!s) return setHTML(root, "stats", `<div class="empty"><span class="spinner"></span> Загружаю...</div>`);
  if (s.empty) return setHTML(root, "stats", `<div class="card"><div class="empty">Пока ничего не скачано.</div></div>`);
  const head = `<tr><th></th>${s.periods.map((p) => `<th class="num">${esc(p)}</th>`).join("")}</tr>`;
  const rows = s.rows.map((r) => `<tr><td>${esc(r.label)}</td>${r.counts.map((c, i) =>
    `<td class="num"><b>${c}</b><div>${esc(r.bytes[i])}</div></td>`).join("")}</tr>`).join("");
  setHTML(root, "stats", `<div class="card"><table class="stats-table"><thead>${head}</thead><tbody>${rows}</tbody></table></div>
    <div class="card"><div class="kv">
      <span>Итого</span><span>${esc(s.total)} <span class="dim">· видео ${s.video}, mp3 ${s.audio}</span></span>
      <span>Сайты</span><span>${esc((s.sites || []).join(", "))}</span>
      ${s.users && s.users.length ? `<span>Пользователи бота</span><span>${esc(s.users.join(", "))}</span>` : ""}
      <span>Считается с</span><span>${esc(s.since)}</span>
    </div>${s.unknown ? `<div class="hint">У ${s.unknown} старых загрузок размер неизвестен: файлов уже нет в папке.</div>` : ""}</div>`);
}

// ---------- настройки ----------

let proxyManualOpen = false;
function renderSettings() {
  const p = state.proxy, y = state.ytdlp;
  const root = $("#settings");
  if (root.contains(document.activeElement) && document.activeElement.tagName === "INPUT") return; // не мешаем печатать
  const showManual = p.mode === "manual" || proxyManualOpen;
  const mode = (m, label) => `<button class="${(showManual ? "manual" : p.mode) === m ? "sel" : ""}" data-action="proxy-mode" data-mode="${m}">${label}</button>`;
  const proxyNow = p.addr ? `${esc(p.addr)} <span class="${p.alive ? "dim" : "err"}">· ${esc(p.source)}${p.alive ? "" : ", не отвечает"}</span>` : esc(p.source);
  setHTML(root, "settings", `<div class="card">
      <h2>Прокси</h2>
      <div class="seg options">${mode("auto", "Авто")}${mode("off", "Без прокси")}${mode("manual", "Вручную")}</div>
      ${showManual ? `<div class="row" style="margin-top:12px">
        <input id="proxy-addr" class="grow" placeholder="127.0.0.1:10809 или socks5://127.0.0.1:10808" value="${esc(p.manual)}" spellcheck="false">
        <button class="btn primary" data-action="proxy-save">Сохранить</button></div>` : ""}
      <div class="kv" style="margin-top:14px"><span>Сейчас</span><span>${proxyNow}</span></div>
      <div class="hint">«Авто» берёт системный прокси Windows — его включают v2rayN и Happ. В режиме туннеля прокси не нужен:
        программа подключится напрямую, а трафик и так пойдёт через VPN.</div>
    </div>
    <div class="card">
      <h2>Cookies · нужны редко</h2>
      <div class="kv"><span>Сейчас</span><span>${state.cookies ? `<span class="ok">подключены</span> <span class="dim">${esc(state.cookies)}</span>` : `<span class="dim">не указаны</span>`}</span></div>
      <div class="row" style="margin-top:12px">
        <button class="btn" data-action="cookies-pick">Выбрать файл...</button>
        <input type="file" id="cookies-file" accept=".txt,.json" hidden>
        <input id="cookies-path" class="grow" placeholder="или путь: C:\\...\\cookies.txt" spellcheck="false">
        <button class="btn ghost" data-action="cookies-path">Указать</button>
        ${state.cookies ? `<button class="btn ghost" data-action="cookies-clear">Убрать</button>` : ""}
      </div>
      <div class="hint">Нужны, только если сайт требует вход: 18+, «я не бот», приватное видео. Экспортируй cookies расширением
        «Get cookies.txt LOCALLY» в браузере, где ты вошёл в YouTube. Оригинал файла программа не меняет.</div>
    </div>
    <div class="card">
      <h2>yt-dlp — движок загрузки</h2>
      <div class="kv"><span>Версия</span><span>${esc(y.version)} ${y.update ? `<span class="warn">· вышла ${esc(y.update)}</span>` : y.latest ? `<span class="dim">· последняя</span>` : ""}</span></div>
      <div class="actions">
        <button class="btn ${y.update ? "primary" : ""}" data-action="ytdlp-update" ${y.updating ? "disabled" : ""}>${y.updating ? "Обновляю..." : "Обновить"}</button>
        ${y.message ? `<span class="${y.error ? "err" : "ok"}" style="align-self:center">${esc(y.message)}</span>` : ""}
      </div>
      <div class="hint">YouTube регулярно меняет защиту. Если видео перестали качаться — обнови.</div>
    </div>
    <div class="card">
      <h2>Telegram-бот</h2>
      <p>Пользователи присылают боту ссылку — программа скачивает файл и отправляет его в чат.
        Для обычной работы не нужен, поэтому раздел скрыт.</p>
      <div class="actions">${state.bot.visible
        ? (state.bot.hasToken ? `<span class="dim" style="align-self:center">Раздел включён: бот настроен.</span>`
          : `<button class="btn ghost" data-action="showbot-off">Скрыть раздел «Бот»</button>`)
        : `<button class="btn" data-action="showbot-on">Включить раздел «Бот»</button>`}</div>
    </div>
    <div class="card">
      <h2>Программа</h2>
      <div class="kv"><span>Версия</span><span>${esc(state.version)}</span>
        <span>Загрузки</span><span><a href="#" data-action="open-downloads">${esc(state.downloadDir)}</a></span></div>
      <div class="actions">
        <button class="btn" data-action="open-log">Открыть журнал</button>
        <button class="btn danger" data-action="quit">Выйти из программы</button></div>
      <div class="hint">Журнал — что делала программа и текст ошибки, если она упала. Пригодится, чтобы найти причину проблемы.</div>
    </div>`);
}

// ---------- действия (один обработчик на всё окно) ----------

document.addEventListener("click", async (e) => {
  const goto = e.target.closest("[data-goto]");
  if (goto) return showTab(goto.dataset.goto);
  const link = e.target.closest("[data-url]");
  if (link) { e.preventDefault(); return act("openurl", { url: link.dataset.url }); }
  const el = e.target.closest("[data-action]");
  if (!el) return;
  e.preventDefault();
  const id = Number(el.dataset.id);
  switch (el.dataset.action) {
    case "pick":
      cardUI[id].option = el.dataset.key;
      rendered["dl" + id] = null;
      return renderDownloads();
    case "start": {
      const ui = cardUI[id];
      return act("download", { id, option: ui.option, start: ui.start, end: ui.end });
    }
    case "cancel": return act("cancel", { id });
    case "dismiss": return act("dismiss", { id });
    case "retry": return probe(el.dataset.url);
    case "play": return act("open", { file: el.dataset.file });
    case "reveal": return act("open", { file: el.dataset.file, reveal: true });
    case "open-downloads": return act("open", { file: "" });

    case "bot-start": return act("bot/start", {});
    case "bot-stop": return act("bot/stop", {}, "Бот останавливается — у пользователей будет статус 🔴 Offline");
    case "bot-restart": return act("bot/restart", {}, "Перезапускаю бота...");
    case "allow": return act("bot/request", { id, allow: true }, "Доступ выдан");
    case "deny": return act("bot/request", { id, allow: false }, "В доступе отказано");
    case "remove-user":
      if (!confirm(`Убрать доступ у ${el.dataset.name}?`)) return;
      await act("bot/remove", { id }, "Доступ убран");
      return loadLists(true);
    case "grant-user":
      await act("bot/grant", { id }, "Доступ выдан");
      return loadLists(true);
    case "save-token": {
      const token = $("#token-input").value.trim();
      if (!token) return toast("Вставь токен", "err");
      return act("bot/token", { token }, (r) => r.name ? `Бот @${r.name} найден, токен сохранён` : "Токен сохранён (проверить не удалось: нет связи с Telegram)");
    }
    case "clear-token":
      if (!confirm("Удалить токен бота?")) return;
      return act("bot/token", { token: "" }, "Токен удалён");
    case "autostart-on": return act("bot/autostart", { on: true }, "Автозапуск включён");
    case "autostart-off": return act("bot/autostart", { on: false }, "Автозапуск выключен");

    case "proxy-mode":
      if (el.dataset.mode === "manual") { proxyManualOpen = true; rendered.settings = null; return renderSettings(); }
      proxyManualOpen = false;
      return act("settings/proxy", { mode: el.dataset.mode }, "Прокси сохранён");
    case "proxy-save":
      return act("settings/proxy", { mode: "manual", addr: $("#proxy-addr").value }, "Прокси сохранён");
    case "cookies-pick": return $("#cookies-file").click();
    case "cookies-path": return act("settings/cookies", { path: $("#cookies-path").value }, "Cookies подключены");
    case "cookies-clear": return act("settings/cookies", { path: "" }, "Cookies больше не используются");
    case "ytdlp-update": return act("ytdlp/update", {});
    case "showbot-on":
      if (await act("settings/showbot", { on: true }, "Раздел «Бот» включён — он появился слева")) {
        state.bot.visible = true; // не ждём следующего обновления, иначе окно тут же вернёт на «Скачать»
        showTab("bot");
      }
      return;
    case "showbot-off": return act("settings/showbot", { on: false }, "Раздел «Бот» скрыт");
    case "open-log": return act("openlog", {});
    case "crash-dismiss": return act("crash/dismiss", {});
    case "quit":
      if (!confirm("Выйти из программы? Бот тоже остановится.")) return;
      await act("quit", {});
      return window.close();
  }
});

document.addEventListener("input", (e) => {
  const f = e.target.dataset.field;
  if (f) cardUI[e.target.dataset.id][f] = e.target.value;
});

document.addEventListener("change", async (e) => {
  if (e.target.id !== "cookies-file" || !e.target.files.length) return;
  const file = e.target.files[0];
  await act("settings/cookies/upload", await file.arrayBuffer(), "Cookies подключены (копия сохранена рядом с программой)");
  e.target.value = "";
});

// Спиннер — как в консоли: | / - \
const frames = ["|", "/", "-", "\\"];
let frame = 0;
setInterval(() => {
  frame = (frame + 1) % frames.length;
  $$(".spinner").forEach((s) => { s.textContent = frames[frame]; });
}, 120);

document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
// Окно закрывают — сообщаем программе, чтобы она завершилась (при обновлении страницы — не завершится).
window.addEventListener("pagehide", () => navigator.sendBeacon("/api/bye?t=" + encodeURIComponent(TOKEN)));

refresh();
loadLists(true);
