package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Telegram-бот: друзья присылают ссылку — программа на этом ПК скачивает видео
// (через тот же прокси) и отправляет его в чат. Сервер не нужен: бот сам опрашивает
// Telegram, поэтому не нужны ни белый IP, ни открытые порты. Работает, пока запущен.
//
// Доступ только у тех, кого владелец разрешил. Список друзей — в settings.json,
// статистика, журнал и запросы на доступ — в bot_data.json.
//
// Здесь — «движок» бота. Им управляют и консоль (botconsole.go), и окно (gui*.go).

const (
	botMaxBytes     = 49 << 20 // Telegram принимает от ботов файлы до 50 МБ
	botParallel     = 2        // одновременных загрузок
	botQueuePerUser = 5        // ссылок от одного человека в очереди
	botHistorySize  = 15       // сколько загрузок показывает «Мои загрузки»
	botEventsKept   = 50       // событий в памяти для панели
)

var (
	botActive atomic.Bool            // консольный бот запущен — Ctrl+C останавливает его, а не программу
	botOnExit atomic.Pointer[func()] // что сделать при выходе из программы во время работы бота
)

var (
	urlRe      = regexp.MustCompile(`https?://\S+`)
	tokenRe    = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{30,}$`)
	ansiRe     = regexp.MustCompile("\x1b\\[[0-9;]*m")
	botHeights = []int{1080, 720, 480, 360} // если видео не влезает в 50 МБ — пробуем качество ниже
)

// ---------- тексты для друзей ----------

const (
	profileDescription = "Скачиваю видео и музыку по ссылке.\nTikTok · YouTube · Reels · VK · SoundCloud\n\n"
	profileOnline      = "🟢 Online"
	profileOffline     = "🔴 Offline"
	profileCreator     = "\nСоздатель: " + creator
	shortAbout         = " · видео и музыка по ссылке"
	creator            = "@bananas_ananass"

	// Кнопки внизу чата (вместо команд /history и /help).
	btnHistory = "📜 Мои загрузки"
	btnHelp    = "❓ Как пользоваться"
)

func replyKeyboard() map[string]any {
	return map[string]any{
		"keyboard":                [][]map[string]string{{{"text": btnHistory}, {"text": btnHelp}}},
		"resize_keyboard":         true,
		"is_persistent":           true,
		"input_field_placeholder": "Вставь ссылку на видео",
	}
}

func welcomeText(name string) string {
	return "👋 Привет, " + html.EscapeString(name) + "!\n\n" +
		"Пришли ссылку — пришлю видео.\n" +
		"Музыку с SoundCloud и похожих сайтов — сразу в MP3.\n\n" +
		"<i>До 50 МБ на файл · работает, пока включён ПК владельца</i>\n" +
		"Создатель: " + creator
}

// Закреплённое сообщение в чате каждого друга — видно сразу, без захода в профиль.
func pinText(online bool) string {
	if online {
		return "🟢 <b>Online</b>"
	}
	return "🔴 <b>Offline</b>"
}

func progressText(label string, pct float64) string {
	if pct < 0 {
		return "⏳ " + label + "..."
	}
	const width = 12
	filled := min(int(pct/100*width), width)
	return fmt.Sprintf("⏳ %s...\n<code>%s%s</code> %d%%", label,
		strings.Repeat("▓", filled), strings.Repeat("░", width-filled), int(pct))
}

// ---------- движок ----------

type bot struct {
	tg     *tgClient
	ctx    context.Context
	cancel context.CancelFunc
	dir    string
	name   string // @username бота
	active atomic.Int32

	mu     sync.Mutex
	queued map[int64]int           // сколько ссылок пользователя в работе
	ops    map[*operation]struct{} // текущие загрузки — чтобы остановить их вместе с ботом
	jobs   sync.WaitGroup

	requests chan botUser // новые запросы на доступ (сами запросы хранятся в botDB)
	slots    chan struct{}
	seq      atomic.Int64

	sessionOK    atomic.Int64
	sessionBytes atomic.Int64

	consoleMode bool   // события дублируются в консоль, если в ней нельзя перерисовывать
	onChange    func() // перерисовать панель (консоль); окно само опрашивает состояние

	viewMu   sync.Mutex
	events   []*botEvent
	question string // текущий вопрос владельцу в консоли
}

type botEvent struct {
	time   time.Time
	text   string
	status string
}

// startBot подключается к Telegram и запускает бота.
func startBot(consoleMode bool) (*bot, error) {
	if cfg.BotToken == "" {
		return nil, errors.New("не указан токен бота")
	}
	if err := tools.wait(); err != nil {
		return nil, err
	}
	var err error
	if cookieArgs, err = prepareCookies(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	tg := newTelegram(cfg.BotToken)
	cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
	me, err := tg.getMe(cctx)
	if err == nil {
		tg.deleteCommands(cctx) // вместо меню команд — кнопки внизу чата
		_ = tg.setProfile(cctx, profileDescription+profileOnline+profileCreator, "🟢 Online"+shortAbout)
	}
	ccancel()
	if err != nil {
		cancel()
		return nil, errors.New(explainTelegram(err))
	}

	b := &bot{
		tg: tg, ctx: ctx, cancel: cancel,
		dir:         filepath.Join(tools.dir, "bot"),
		name:        me.Username,
		queued:      map[int64]int{},
		ops:         map[*operation]struct{}{},
		requests:    make(chan botUser, 64),
		slots:       make(chan struct{}, botParallel),
		consoleMode: consoleMode,
	}
	_ = os.MkdirAll(b.dir, 0o755)

	// При выходе из программы (в том числе крестиком) успеваем поставить «не в сети».
	goOffline := func() { b.setOffline() }
	botOnExit.Store(&goOffline)

	go b.updatePins(true)
	for _, id := range botDB.takeNotify() {
		name := ""
		if st, ok := botDB.user(id); ok {
			name = strings.TrimSpace(strings.TrimSuffix(st.Name, "(@"+st.Username+")"))
			b.note("%s %s: отправил «доступ открыт»", green("[+]"), st.Name)
		}
		go b.welcome(id, name)
	}
	go b.poll()
	return b, nil
}

// stop останавливает бота. offline — поставить статус «не в сети» (при перезапуске не нужно).
func (b *bot) stop(offline bool) {
	b.cancel()
	b.mu.Lock()
	for op := range b.ops {
		op.cancel()
	}
	b.mu.Unlock()
	done := make(chan struct{})
	go func() { b.jobs.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	os.RemoveAll(b.dir)
	if offline {
		b.setOffline()
	}
	botOnExit.Store(nil)
}

// setOffline — описание бота и закреплённые сообщения друзей переводятся в «не в сети».
func (b *bot) setOffline() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = b.tg.setProfile(ctx, profileDescription+profileOffline+profileCreator, "🔴 Offline"+shortAbout)
	}()
	b.pinsWith(ctx, false, &wg)
	wg.Wait()
}

func (b *bot) updatePins(online bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	b.pinsWith(ctx, online, &wg)
	wg.Wait()
}

func (b *bot) pinsWith(ctx context.Context, online bool, wg *sync.WaitGroup) {
	text := pinText(online)
	for _, u := range friendsCopy() {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			b.setPin(ctx, id, text)
		}(u.ID)
	}
}

// setPin правит закреплённое сообщение, а если его нет (или друг его удалил) — создаёт и закрепляет.
func (b *bot) setPin(ctx context.Context, userID int64, text string) {
	if id := botDB.pin(userID); id != 0 {
		err := b.tg.editMessage(ctx, userID, id, text)
		var te *tgError
		if err == nil || (errors.As(err, &te) && strings.Contains(te.Description, "not modified")) {
			return
		}
		if !errors.As(err, &te) { // нет связи — не плодим новые сообщения
			return
		}
	}
	id, err := b.tg.sendSilent(ctx, userID, text)
	if err != nil {
		return
	}
	botDB.setPin(userID, id)
	_ = b.tg.pinMessage(ctx, userID, id)
}

func (b *bot) welcome(id int64, name string) {
	b.send(id, "✅ Доступ открыт!\n\n"+welcomeText(name), 0, replyKeyboard())
	botDB.setKeyboard(id)
	b.setPin(b.ctx, id, pinText(true))
}

// ---------- события для панели ----------

func (b *bot) changed() {
	if b.onChange != nil {
		b.onChange()
	}
}

// note добавляет событие на панель и возвращает его, чтобы потом обновить статус.
func (b *bot) note(format string, a ...any) *botEvent {
	text := strings.ReplaceAll(fmt.Sprintf(format, a...), "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	ev := &botEvent{time: time.Now(), text: text}
	b.viewMu.Lock()
	b.events = append(b.events, ev)
	if len(b.events) > botEventsKept {
		b.events = b.events[len(b.events)-botEventsKept:]
	}
	b.viewMu.Unlock()
	if b.consoleMode && (!vt || !isConsole) {
		fmt.Println(dim(ev.time.Format("15:04")) + " " + text)
	}
	b.changed()
	return ev
}

func (b *bot) setEvent(ev *botEvent, status string, final bool) {
	b.viewMu.Lock()
	ev.status = "  " + status
	b.viewMu.Unlock()
	if b.consoleMode && (!vt || !isConsole) && final {
		fmt.Println(dim(ev.time.Format("15:04")) + " " + ev.text + ev.status)
	}
	b.changed()
}

type eventView struct {
	Time   string `json:"time"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Kind   string `json:"kind"` // ok, err, work, info
}

// eventsView — события для окна: новые сверху, без цветовых кодов консоли.
func (b *bot) eventsView() []eventView {
	b.viewMu.Lock()
	defer b.viewMu.Unlock()
	out := make([]eventView, 0, len(b.events))
	for i := len(b.events) - 1; i >= 0; i-- {
		ev := b.events[i]
		status := strings.TrimSpace(ansiRe.ReplaceAllString(ev.status, ""))
		kind := "info"
		switch {
		case strings.HasPrefix(status, "OK"):
			kind = "ok"
		case strings.HasPrefix(status, "ошибка"):
			kind = "err"
		case status != "":
			kind = "work"
		}
		out = append(out, eventView{Time: ev.time.Format("15:04"), Text: ansiRe.ReplaceAllString(ev.text, ""),
			Status: status, Kind: kind})
	}
	return out
}

// ---------- доступ ----------

func (b *bot) allow(u botUser) {
	if err := grantAccess(u, false); err != nil {
		b.note("%s не удалось сохранить список друзей: %v", red("[!]"), err)
	}
	b.note("%s %s получил доступ", green("[+]"), u.Name)
	name := u.Name
	if st, ok := botDB.user(u.ID); ok && st.Username != "" {
		name = strings.TrimSpace(strings.TrimSuffix(name, "(@"+st.Username+")"))
	}
	go b.welcome(u.ID, name)
}

func (b *bot) deny(u botUser) {
	botDB.resolveRequest(u.ID, false, false)
	b.note("%s %s: доступ не дан", red("[-]"), u.Name)
	go b.send(u.ID, "Владелец бота не разрешил доступ.", 0, nil)
}

func (b *bot) send(chat int64, html string, replyTo int64, markup any) int64 {
	id, err := b.tg.sendMessage(b.ctx, chat, html, replyTo, markup)
	if err != nil && b.ctx.Err() == nil {
		b.note("%s не удалось ответить: %s", yellow("[!]"), explainTelegram(err))
	}
	return id
}

// ---------- приём сообщений ----------

// poll — длинный опрос Telegram. При обрывах связи переподключается сам.
func (b *bot) poll() {
	var offset int64
	backoff := time.Second
	lastErr := ""
	for b.ctx.Err() == nil {
		ups, err := b.tg.getUpdates(b.ctx, offset)
		if err != nil {
			if b.ctx.Err() != nil {
				return
			}
			var te *tgError
			if errors.As(err, &te) && (te.Code == 401 || te.Code == 404 || te.Code == 409) {
				b.note("%s %s", red("[!]"), explainTelegram(err))
			} else if msg := explainTelegram(err); msg != lastErr {
				b.note("%s %s — переподключаюсь...", yellow("[!]"), msg)
				lastErr = msg
			}
			if errors.As(err, &te) && te.RetryAfter > 0 {
				backoff = time.Duration(te.RetryAfter) * time.Second
			}
			select {
			case <-b.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		if lastErr != "" {
			b.note("%s связь с Telegram восстановлена", green("[OK]"))
			lastErr = ""
		}
		backoff = time.Second
		for _, u := range ups {
			offset = u.UpdateID + 1
			switch {
			case u.Message != nil:
				b.handle(u.Message)
			case u.CallbackQuery != nil:
				b.handleButton(u.CallbackQuery)
			}
		}
	}
}

func (b *bot) handle(m *tgMessage) {
	if m.From == nil || m.From.IsBot || m.Chat.Type != "private" {
		return
	}
	botDB.seen(m.From)
	user := botUser{ID: m.From.ID, Name: m.From.displayName()}
	chat := m.Chat.ID
	text := strings.TrimSpace(m.Text + " " + m.Caption)

	if !isFriend(user.ID) {
		switch {
		case botDB.isDenied(user.ID):
			// владелец уже отказал — не беспокоим его снова
		case botDB.addRequest(user):
			b.send(chat, "🔒 Это личный бот. Я спросил владельца — напишу, когда он разрешит.", 0, nil)
			b.note("%s %s просит доступ", yellow("[?]"), user.Name)
			select {
			case b.requests <- user:
			default:
			}
		default:
			b.send(chat, "⏳ Запрос уже у владельца, подожди.", 0, nil)
		}
		return
	}

	low := strings.ToLower(text)
	switch {
	case strings.HasPrefix(low, "/start"), strings.HasPrefix(low, "/help"), text == btnHelp:
		b.send(chat, welcomeText(m.From.FirstName), 0, replyKeyboard())
		botDB.setKeyboard(user.ID)
		if botDB.pin(user.ID) == 0 {
			go b.setPin(b.ctx, user.ID, pinText(true))
		}
		return
	case strings.HasPrefix(low, "/history"), text == btnHistory:
		b.send(chat, historyText(user.ID), 0, replyKeyboard())
		botDB.setKeyboard(user.ID)
		return
	}

	// Друзья, которые пришли до появления кнопок, получат их один раз.
	if !botDB.hasKeyboard(user.ID) {
		b.send(chat, "Внизу появились кнопки «"+btnHistory+"» и «"+btnHelp+"».", 0, replyKeyboard())
		botDB.setKeyboard(user.ID)
	}

	link := urlRe.FindString(text)
	if link == "" {
		b.send(chat, "Пришли ссылку на видео или трек.", m.MessageID, nil)
		return
	}
	// Музыкальные платформы (SoundCloud и т.п.) — сразу MP3, без попытки скачать «видео».
	mode := modeVideo
	if isAudioSite(link) || strings.Contains(low, "mp3") || strings.Contains(low, "звук") || strings.Contains(low, "audio") {
		mode = modeAudio
	}
	b.enqueue(user, chat, m.MessageID, link, mode)
}

// botMode — что отправить: видео, MP3 или MP4 со статичной обложкой (для музыки).
type botMode int

const (
	modeVideo botMode = iota
	modeAudio
	modeCover
)

// handleButton — нажатие кнопки под отправленным файлом («Только звук», «Видео», «MP4 с обложкой»).
func (b *bot) handleButton(q *tgCallback) {
	if !isFriend(q.From.ID) || q.Message == nil {
		b.tg.answerCallback(b.ctx, q.ID, "Нет доступа")
		return
	}
	kind, idStr, _ := strings.Cut(q.Data, ":")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	e, ok := botDB.entry(id)
	if !ok {
		b.tg.answerCallback(b.ctx, q.ID, "Ссылка устарела — пришли её ещё раз")
		return
	}
	mode, answer := modeVideo, "Скачиваю видео..."
	switch {
	case kind == "a":
		mode, answer = modeAudio, "Скачиваю MP3..."
	case kind == "c" || isAudioSite(e.URL):
		mode, answer = modeCover, "Собираю MP4 с обложкой..."
	}
	b.tg.answerCallback(b.ctx, q.ID, answer)
	botDB.seen(&q.From)
	b.enqueue(botUser{ID: q.From.ID, Name: q.From.displayName()}, q.Message.Chat.ID, q.Message.MessageID, e.URL, mode)
}

func (b *bot) enqueue(user botUser, chat, replyTo int64, link string, mode botMode) {
	b.mu.Lock()
	if b.queued[user.ID] >= botQueuePerUser {
		b.mu.Unlock()
		b.send(chat, "Слишком много ссылок сразу — подожди, пока я отправлю предыдущие.", replyTo, nil)
		return
	}
	b.queued[user.ID]++
	b.mu.Unlock()

	b.jobs.Add(1)
	go func() {
		defer b.jobs.Done()
		defer func() {
			b.mu.Lock()
			if b.queued[user.ID]--; b.queued[user.ID] <= 0 {
				delete(b.queued, user.ID)
			}
			b.mu.Unlock()
		}()
		b.job(user, chat, replyTo, link, mode)
	}()
}

// ---------- одна загрузка ----------

func (b *bot) job(user botUser, chat, replyTo int64, link string, mode botMode) {
	started := time.Now()
	label, kind := "Скачиваю видео", ""
	switch mode {
	case modeAudio:
		label, kind = "Скачиваю MP3", " [mp3]"
	case modeCover:
		label, kind = "Скачиваю трек", " [mp4+обложка]"
	}
	status := b.send(chat, progressText(label, -1), replyTo, nil)
	setStatus := func(text string) {
		if status != 0 {
			_ = b.tg.editMessage(b.ctx, chat, status, text)
		}
	}
	who := user.Name
	if st, ok := botDB.user(user.ID); ok && st.Username != "" {
		who = "@" + st.Username
	}
	ev := b.note("%s · %s%s", truncate(who, 20), siteName(link), kind)

	// Одновременно качается не больше botParallel видео, остальные ждут в очереди.
	select {
	case b.slots <- struct{}{}:
	default:
		b.setEvent(ev, dim("в очереди"), false)
		setStatus("🕐 В очереди — сейчас качаю для других.")
		select {
		case b.slots <- struct{}{}:
			setStatus(progressText(label, -1))
		case <-b.ctx.Done():
			return
		}
	}
	defer func() { <-b.slots }()
	b.active.Add(1)
	defer func() { b.active.Add(-1); b.changed() }()
	b.setEvent(ev, dim("качаю..."), false)

	op := newOperation()
	b.mu.Lock()
	b.ops[op] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.ops, op)
		b.mu.Unlock()
		op.finish()
	}()

	// Прогресс в сообщении обновляем не чаще раза в 3 секунды — у Telegram есть лимиты.
	var lastEdit time.Time
	lastPct := -1.0
	onProgress := func(pct float64) {
		if pct < 0 { // этап без процентов (сборка видео с обложкой)
			setStatus("⏳ Собираю MP4 с обложкой...")
			b.setEvent(ev, dim("собираю видео..."), false)
			return
		}
		if time.Since(lastEdit) < 3*time.Second || pct-lastPct < 5 {
			return
		}
		lastEdit, lastPct = time.Now(), pct
		setStatus(progressText(label, pct))
		b.setEvent(ev, dim(fmt.Sprintf("качаю %d%%", int(pct))), false)
	}

	dir := filepath.Join(b.dir, strconv.FormatInt(b.seq.Add(1), 10))
	defer os.RemoveAll(dir)
	res, err := botDownload(op, dir, link, mode, onProgress)
	if b.ctx.Err() != nil {
		return
	}
	audio := mode == modeAudio
	if err != nil {
		botDB.record(logEntry{UserID: user.ID, URL: link, Audio: audio, Error: err.Error()})
		setStatus("❌ Не получилось: " + html.EscapeString(err.Error()))
		b.setEvent(ev, red("ошибка: ")+err.Error(), true)
		return
	}

	entryID := botDB.record(logEntry{UserID: user.ID, URL: link, Title: res.title, Audio: audio,
		Size: res.size, OK: true})
	setStatus("📤 Отправляю...")
	b.setEvent(ev, dim("отправляю..."), false)

	caption := "<b>" + html.EscapeString(truncate(res.title, 200)) + "</b>\n" +
		`<a href="` + html.EscapeString(link) + `">` + html.EscapeString(siteName(link)) + "</a> · " +
		formatBytes(float64(res.size))
	extra := map[string]string{
		"caption":          caption,
		"parse_mode":       "HTML",
		"reply_parameters": fmt.Sprintf(`{"message_id":%d,"allow_sending_without_reply":true}`, replyTo),
	}
	id := strconv.FormatInt(entryID, 10)
	action, method, field := "upload_video", "sendVideo", "video"
	var other tgButton
	switch {
	case mode == modeAudio:
		action, method, field = "upload_voice", "sendAudio", "audio"
		extra["title"], extra["performer"], extra["duration"] = res.title, res.uploader, res.duration
		if isAudioSite(link) {
			other = tgButton{"🖼 MP4 с обложкой", "c:" + id}
		} else {
			other = tgButton{"🎬 Видео", "v:" + id}
		}
	case mode == modeCover:
		other = tgButton{"🎵 MP3", "a:" + id}
	default:
		other = tgButton{"🎵 Только звук", "a:" + id}
	}
	if method == "sendVideo" {
		extra["supports_streaming"] = "true"
		extra["width"], extra["height"], extra["duration"] = res.width, res.height, res.duration
	}
	markup, _ := json.Marshal(keyboard(other))
	extra["reply_markup"] = string(markup)

	b.tg.sendChatAction(b.ctx, chat, action)
	if err := b.tg.sendFile(b.ctx, method, field, chat, res.path, extra); err != nil {
		if b.ctx.Err() != nil {
			return
		}
		botDB.markFailed(entryID, "отправка: "+explainTelegram(err))
		setStatus("❌ Не получилось отправить файл: " + html.EscapeString(explainTelegram(err)))
		b.setEvent(ev, red("ошибка отправки: ")+explainTelegram(err), true)
		return
	}
	if status != 0 {
		_ = b.tg.deleteMessage(b.ctx, chat, status)
	}
	b.sessionOK.Add(1)
	b.sessionBytes.Add(res.size)
	b.setEvent(ev, green("OK")+dim(fmt.Sprintf(" %s · %s", formatBytes(float64(res.size)),
		formatDuration(time.Since(started).Seconds()))), true)
}

func historyText(userID int64) string {
	list := botDB.history(userID, botHistorySize, true)
	if len(list) == 0 {
		return "История пока пуста — пришли ссылку на видео."
	}
	var sb strings.Builder
	sb.WriteString("<b>Твои последние загрузки</b>\n\n")
	for i, e := range list {
		icon := "🎬"
		if e.Audio {
			icon = "🎵"
		}
		title := e.Title
		if title == "" {
			title = siteName(e.URL)
		}
		fmt.Fprintf(&sb, "%d. %s <a href=\"%s\">%s</a>\n<i>%s · %s</i>\n", i+1, icon,
			html.EscapeString(e.URL), html.EscapeString(truncate(title, 60)),
			e.Time.Format("02.01 15:04"), formatBytes(float64(e.Size)))
	}
	sb.WriteString("\nЧтобы скачать снова — перешли мне ссылку.")
	return sb.String()
}

type botFile struct {
	path, title, uploader   string
	width, height, duration string
	size                    int64
}

// botDownload качает файл в dir так, чтобы он влез в лимит Telegram:
// видео начинает с 1080p и при необходимости снижает качество.
func botDownload(op *operation, dir, link string, mode botMode, onProgress func(pct float64)) (*botFile, error) {
	common := append(baseArgs(),
		"--no-playlist",
		"--match-filters", "!is_live",
		"--max-filesize", strconv.Itoa(botMaxBytes),
		"-P", dir, "-P", "temp:"+filepath.Join(dir, "tmp"),
		"-o", "%(title).80s [%(id)s].%(ext)s",
		"--quiet", "--no-simulate", "--progress", "--newline",
		"--progress-template", "download:@@P %(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s",
		"--print", "video:@@I %(id)s",
		"--print", "after_move:@@F %(filepath)s|%(width)s|%(height)s|%(duration)s|%(uploader)s|%(title)s",
	)

	var variants [][]string
	switch mode {
	case modeAudio:
		variants = [][]string{{"-f", "ba/b", "-x", "--audio-format", "mp3", "--audio-quality", "5",
			"--embed-metadata", "--embed-thumbnail", "--convert-thumbnails", "jpg"}}
	case modeCover:
		variants = [][]string{coverArgs()}
	default:
		for _, h := range botHeights {
			variants = append(variants, formatArgs(downloadOptions{height: h}))
		}
	}

	for _, v := range variants {
		var file *botFile
		started := false
		yerr := run(op, append(append(common[:len(common):len(common)], v...), "--", link), func(line string) {
			switch {
			case strings.HasPrefix(line, "@@P "):
				f := strings.Split(line[4:], "|")
				if len(f) == 3 && onProgress != nil {
					done, total := num(f[0]), num(f[1])
					if total <= 0 {
						total = num(f[2])
					}
					if total > 0 && done >= 0 {
						onProgress(min(done/total*100, 100))
					}
				}
			case strings.HasPrefix(line, "@@I "):
				started = true
			case strings.HasPrefix(line, "@@F "):
				f := strings.SplitN(line[4:], "|", 6)
				if len(f) == 6 {
					file = &botFile{path: f[0], width: na(f[1]), height: na(f[2]), duration: seconds(f[3]),
						uploader: na(f[4]), title: f[5]}
				}
			}
		}, nil)
		if yerr != nil {
			if yerr.aborted {
				return nil, errors.New("отменено")
			}
			text, _ := yerr.explain()
			return nil, errors.New(text)
		}
		if !started {
			return nil, errors.New("это прямой эфир или пустая страница — пришли ссылку на запись")
		}
		if file == nil {
			continue // не влезло в лимит ещё до скачивания — пробуем качество ниже
		}
		if mode == modeCover {
			if onProgress != nil {
				onProgress(-1)
			}
			out, err := makeCoverVideo(op, file.path, dir)
			if err != nil {
				return nil, err
			}
			os.Remove(file.path)
			file.path, file.width, file.height = out, "", ""
		}
		st, err := os.Stat(file.path)
		if err != nil {
			return nil, err
		}
		if st.Size() > botMaxBytes {
			os.Remove(file.path)
			continue
		}
		file.size = st.Size()
		return file, nil
	}
	switch mode {
	case modeAudio:
		return nil, errors.New("даже звук не влезает в лимит Telegram 50 МБ")
	case modeCover:
		return nil, errors.New("трек слишком длинный — MP4 не влезает в лимит Telegram 50 МБ. Попробуй MP3")
	}
	return nil, errors.New("видео слишком длинное — даже в 360p больше 50 МБ (лимит Telegram). " +
		"Нажми «Только звук» или пришли ссылку со словом mp3")
}

func na(s string) string {
	if s == "NA" {
		return ""
	}
	return s
}

func seconds(s string) string {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.Itoa(int(v + 0.5))
	}
	return ""
}
