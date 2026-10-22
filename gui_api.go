package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// API окна. Страница раз в секунду забирает /api/state и отправляет команды POST-запросами.

func guiAPI() http.Handler {
	mux := http.NewServeMux()
	handle := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			res, err := fn(r)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			if res == nil {
				res = map[string]bool{"ok": true}
			}
			_ = json.NewEncoder(w).Encode(res)
		})
	}

	handle("/api/state", func(*http.Request) (any, error) { return guiState(), nil })
	handle("/api/show", func(*http.Request) (any, error) { openWindow(false); return nil, nil })
	handle("/api/quit", func(*http.Request) (any, error) {
		logf("выход по кнопке")
		go func() { time.Sleep(300 * time.Millisecond); shutdown(0) }()
		return nil, nil
	})
	handle("/api/openlog", func(*http.Request) (any, error) { shellOpen(logPath()); return nil, nil })
	handle("/api/crash/dismiss", func(*http.Request) (any, error) { prevCrash = ""; return nil, nil })

	// загрузки
	handle("/api/probe", func(r *http.Request) (any, error) {
		var p struct{ URL string }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		url := normalizeURL(p.URL)
		if !looksLikeURL(url) {
			return nil, errors.New("это не похоже на ссылку — скопируй адрес видео целиком")
		}
		return map[string]int64{"id": guiProbe(url)}, nil
	})
	handle("/api/download", func(r *http.Request) (any, error) {
		var p struct {
			ID         int64
			Option     string
			Start, End string
		}
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		return nil, guiDownloadStart(p.ID, p.Option, p.Start, p.End)
	})
	handle("/api/cancel", func(r *http.Request) (any, error) {
		var p struct{ ID int64 }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		if d := findDownload(p.ID); d != nil {
			d.op.cancel()
		}
		return nil, nil
	})
	handle("/api/dismiss", func(r *http.Request) (any, error) {
		var p struct{ ID int64 }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		dlMu.Lock()
		defer dlMu.Unlock()
		for i, d := range downloads {
			if d.ID == p.ID && d.finished() {
				d.op.finish()
				downloads = append(downloads[:i], downloads[i+1:]...)
				break
			}
		}
		return nil, nil
	})
	handle("/api/open", func(r *http.Request) (any, error) {
		var p struct {
			File   string
			Reveal bool
		}
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		if p.File == "" {
			openFolder(downloadDir, "")
			return nil, nil
		}
		path, err := insideDownloads(p.File)
		if err != nil {
			return nil, err
		}
		if p.Reveal {
			openFolder(downloadDir, path)
		} else {
			shellOpen(path)
		}
		return nil, nil
	})
	handle("/api/openurl", func(r *http.Request) (any, error) {
		var p struct{ URL string }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(p.URL, "https://") && !strings.HasPrefix(p.URL, "http://") {
			return nil, errors.New("неверная ссылка")
		}
		shellOpen(p.URL)
		return nil, nil
	})

	// списки
	handle("/api/history", func(*http.Request) (any, error) { return guiHistory(), nil })
	handle("/api/stats", func(*http.Request) (any, error) { return buildStats(), nil })
	handle("/api/users", func(*http.Request) (any, error) { return guiUsers(), nil })
	handle("/api/journal", func(*http.Request) (any, error) { return guiJournal(), nil })

	// бот
	handle("/api/bot/start", func(*http.Request) (any, error) { guiStartBot(); return nil, nil })
	handle("/api/bot/stop", func(*http.Request) (any, error) { go guiStopBot(true); return nil, nil })
	handle("/api/bot/restart", func(*http.Request) (any, error) {
		go func() { guiStopBot(false); loadSettings(); guiStartBot() }()
		return nil, nil
	})
	handle("/api/bot/request", func(r *http.Request) (any, error) {
		var p struct {
			ID    int64
			Allow bool
		}
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		for _, u := range botDB.requests() {
			if u.ID != p.ID {
				continue
			}
			b := currentBot()
			switch {
			case p.Allow && b != nil:
				b.allow(u)
			case p.Allow:
				return nil, grantAccess(u, true)
			case b != nil:
				b.deny(u)
			default:
				botDB.resolveRequest(u.ID, false, false)
			}
		}
		return nil, nil
	})
	handle("/api/bot/grant", func(r *http.Request) (any, error) {
		var p struct{ ID int64 }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		name := ""
		if st, ok := botDB.user(p.ID); ok {
			name = st.Name
		}
		if b := currentBot(); b != nil {
			b.allow(botUser{ID: p.ID, Name: name})
			return nil, nil
		}
		return nil, grantAccess(botUser{ID: p.ID, Name: name}, true)
	})
	handle("/api/bot/remove", func(r *http.Request) (any, error) {
		var p struct{ ID int64 }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		_, err := removeFriend(p.ID)
		return nil, err
	})
	handle("/api/bot/token", func(r *http.Request) (any, error) {
		var p struct{ Token string }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		token := strings.TrimSpace(p.Token)
		name := ""
		if token != "" {
			var err error
			if name, err = checkToken(token); err != nil {
				return nil, err
			}
		}
		cfg.BotToken = token
		return map[string]string{"name": name}, saveSettings()
	})
	handle("/api/bot/autostart", func(r *http.Request) (any, error) {
		var p struct{ On bool }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		return nil, setAutostart(p.On)
	})

	// настройки
	handle("/api/settings/proxy", func(r *http.Request) (any, error) {
		var p struct{ Mode, Addr string }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		switch p.Mode {
		case "auto":
			cfg.Proxy = ""
		case "off":
			cfg.Proxy = proxyOff
		default:
			addr := normalizeProxy(p.Addr)
			if err := validProxy(addr); err != nil {
				return nil, err
			}
			cfg.Proxy = addr
		}
		return nil, saveSettings()
	})
	handle("/api/settings/cookies", func(r *http.Request) (any, error) {
		var p struct{ Path string }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		path := strings.Trim(strings.TrimSpace(p.Path), `"`)
		if path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("не удалось открыть файл: %v", err)
			}
			if _, n, err := toNetscape(data); err != nil {
				return nil, err
			} else if n == 0 {
				return nil, errors.New("в файле нет ни одной записи cookies")
			}
		}
		cfg.Cookies = path
		return nil, saveSettings()
	})
	// Файл, выбранный в окне: браузер не сообщает путь, поэтому сохраняем копию рядом с программой.
	handle("/api/settings/cookies/upload", func(r *http.Request) (any, error) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 5<<20))
		if err != nil {
			return nil, err
		}
		if _, n, err := toNetscape(data); err != nil {
			return nil, err
		} else if n == 0 {
			return nil, errors.New("в файле нет ни одной записи cookies")
		}
		path := filepath.Join(appDir, "cookies.txt")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
		cfg.Cookies = path
		return nil, saveSettings()
	})
	handle("/api/settings/showbot", func(r *http.Request) (any, error) {
		var p struct{ On bool }
		if err := readJSON(r, &p); err != nil {
			return nil, err
		}
		cfg.ShowBot = p.On
		return nil, saveSettings()
	})
	handle("/api/ytdlp/update", func(*http.Request) (any, error) { guiUpdateYtdlp(); return nil, nil })
	return mux
}

func readJSON(r *http.Request, v any) error {
	if r.Method != http.MethodPost {
		return errors.New("нужен POST")
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil && err != io.EOF {
		return errors.New("неверный запрос")
	}
	return nil
}

// insideDownloads разрешает открывать только файлы из папки загрузок.
func insideDownloads(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(downloadDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("файл не из папки загрузок")
	}
	if !fileExists(abs) {
		return "", errors.New("файла уже нет — возможно, его удалили или переместили")
	}
	return abs, nil
}

// ---------- состояние ----------

type guiStateT struct {
	Version     string        `json:"version"`
	PrevCrash   string        `json:"prevCrash"`
	LogPath     string        `json:"logPath"`
	YtDlp       ytdlpState    `json:"ytdlp"`
	Proxy       proxyState    `json:"proxy"`
	Cookies     string        `json:"cookies"`
	Summary     string        `json:"summary"`
	DownloadDir string        `json:"downloadDir"`
	Downloads   []downloadDTO `json:"downloads"`
	Bot         botState      `json:"bot"`
}

type ytdlpState struct {
	Version  string `json:"version"`
	Latest   string `json:"latest"`
	Update   string `json:"update"`
	Updating bool   `json:"updating"`
	Message  string `json:"message"`
	Error    bool   `json:"error"`
}

type proxyState struct {
	Mode   string `json:"mode"` // auto, off, manual
	Addr   string `json:"addr"`
	Source string `json:"source"`
	Alive  bool   `json:"alive"`
	Manual string `json:"manual"`
}

type botState struct {
	Visible   bool          `json:"visible"`
	HasToken  bool          `json:"hasToken"`
	Token     string        `json:"token"`
	Running   bool          `json:"running"`
	Starting  bool          `json:"starting"`
	Stopping  bool          `json:"stopping"`
	Error     string        `json:"error"`
	Name      string        `json:"name"`
	Users     int           `json:"users"`
	Sent      int64         `json:"sent"`
	Bytes     string        `json:"bytes"`
	Active    int32         `json:"active"`
	Events    []eventView   `json:"events"`
	Requests  []requestView `json:"requests"`
	Autostart string        `json:"autostart"`
}

type requestView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	When string `json:"when"`
}

var (
	summaryMu    sync.Mutex
	summaryCache string
	summaryAt    time.Time
	proxyAliveAt time.Time
	proxyAlive_  bool
)

func guiState() guiStateT {
	st := guiStateT{Version: appVersion, DownloadDir: downloadDir, Cookies: cookiesSource(),
		PrevCrash: prevCrash, LogPath: logPath()}

	latest, _ := latestYtdlp.Load().(string)
	ytMu.Lock()
	st.YtDlp = ytdlpState{Version: tools.ytdlpVersion, Latest: latest, Update: ytdlpUpdateAvailable(),
		Updating: ytUpdating, Message: ytMessage, Error: ytError_}
	ytMu.Unlock()

	addr, source := currentProxy()
	st.Proxy = proxyState{Mode: "auto", Addr: addr, Source: source}
	switch cfg.Proxy {
	case "":
	case proxyOff:
		st.Proxy.Mode = "off"
	default:
		st.Proxy.Mode, st.Proxy.Manual = "manual", cfg.Proxy
	}
	// Проверка прокси и сводка считаются не на каждый запрос, а раз в несколько секунд.
	summaryMu.Lock()
	if time.Since(summaryAt) > 5*time.Second {
		summaryCache, summaryAt = statsSummary(), time.Now()
	}
	if addr != "" && time.Since(proxyAliveAt) > 5*time.Second {
		proxyAlive_, proxyAliveAt = proxyAlive(addr), time.Now()
	}
	st.Summary, st.Proxy.Alive = summaryCache, addr == "" || proxyAlive_
	summaryMu.Unlock()

	dlMu.Lock()
	for i := len(downloads) - 1; i >= 0; i-- {
		st.Downloads = append(st.Downloads, downloads[i].dto())
	}
	dlMu.Unlock()

	st.Bot = guiBotState()
	return st
}

// ---------- загрузки в окне ----------

type guiDownload struct {
	mu   sync.Mutex
	ID   int64
	URL  string
	info *mediaInfo
	op   *operation

	status   string // probing, ready, downloading, done, error, cancelled
	title    string
	author   string
	duration float64
	thumb    string
	playlist bool
	count    int
	options  []optionDTO
	label    string
	percent  float64
	detail   string
	lines    []string
	err      string
	hint     string
	files    []string
	size     int64
}

type optionDTO struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type downloadDTO struct {
	ID       int64       `json:"id"`
	URL      string      `json:"url"`
	Status   string      `json:"status"`
	Title    string      `json:"title"`
	Author   string      `json:"author"`
	Duration string      `json:"duration"`
	Thumb    string      `json:"thumb"`
	Playlist bool        `json:"playlist"`
	Count    int         `json:"count"`
	Options  []optionDTO `json:"options"`
	Music    bool        `json:"music"`
	Label    string      `json:"label"`
	Percent  float64     `json:"percent"`
	Detail   string      `json:"detail"`
	Lines    []string    `json:"lines"`
	Error    string      `json:"error"`
	Hint     string      `json:"hint"`
	Files    []string    `json:"files"`
	Size     string      `json:"size"`
}

var (
	dlMu      sync.Mutex
	downloads []*guiDownload
	dlSeq     int64
)

func (d *guiDownload) finished() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status != "probing" && d.status != "downloading"
}

func (d *guiDownload) dto() downloadDTO {
	d.mu.Lock()
	defer d.mu.Unlock()
	dur := ""
	if d.duration > 0 {
		dur = formatDuration(d.duration)
	}
	size := ""
	if d.size > 0 {
		size = formatBytes(float64(d.size))
	}
	return downloadDTO{ID: d.ID, URL: d.URL, Status: d.status, Title: d.title, Author: d.author,
		Duration: dur, Thumb: d.thumb, Playlist: d.playlist, Count: d.count, Options: d.options,
		Music: len(d.options) > 0 && d.options[0].Key == "mp3", Label: d.label, Percent: d.percent,
		Detail: d.detail, Lines: append([]string(nil), d.lines...), Error: d.err, Hint: d.hint,
		Files: append([]string(nil), d.files...), Size: size}
}

// progressSink для карточки загрузки.
func (d *guiDownload) Set(label string, percent float64, detail string) {
	d.mu.Lock()
	d.label, d.percent, d.detail = strings.TrimSuffix(label, "..."), percent, detail
	d.mu.Unlock()
}

func (d *guiDownload) SetLabel(label string) { d.Set(label, -1, "") }

func (d *guiDownload) Println(s string) {
	d.mu.Lock()
	d.lines = append(d.lines, strings.TrimSpace(ansiRe.ReplaceAllString(s, "")))
	if len(d.lines) > 3 {
		d.lines = d.lines[len(d.lines)-3:]
	}
	d.mu.Unlock()
}

func findDownload(id int64) *guiDownload {
	dlMu.Lock()
	defer dlMu.Unlock()
	for _, d := range downloads {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (d *guiDownload) fail(yerr *ytError) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if yerr.aborted {
		d.status = "cancelled"
		return
	}
	d.status = "error"
	d.err, d.hint = yerr.explain()
	d.hint = strings.ReplaceAll(d.hint, "\n       ", " ")
	if d.err != yerr.msg {
		d.hint = strings.TrimSpace(d.hint + " Подробно: " + truncate(yerr.msg, 300))
	}
}

func guiProbe(url string) int64 {
	dlMu.Lock()
	dlSeq++
	d := &guiDownload{ID: dlSeq, URL: url, status: "probing", label: "Получаю информацию", op: newOperation()}
	downloads = append(downloads, d)
	dlMu.Unlock()

	go func() {
		if err := tools.wait(); err != nil {
			d.fail(&ytError{msg: err.Error()})
			return
		}
		var err error
		if cookieArgs, err = prepareCookies(); err != nil {
			d.fail(&ytError{msg: err.Error()})
			return
		}
		info, yerr := probeInfo(d.op, url)
		if yerr != nil {
			d.fail(yerr)
			d.op.finish()
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" || info.IsLive {
			d.status, d.err = "error", "Это прямая трансляция — её можно скачать после окончания эфира."
			return
		}
		d.info = info
		d.title, d.author, d.duration = info.Title, info.author(), info.Duration
		d.thumb = info.Thumbnail
		d.playlist, d.count = info.isPlaylist(), len(info.Entries)
		d.options = optionsFor(url, info)
		d.status = "ready"
	}()
	return d.ID
}

// optionsFor — варианты формата, как в консольном выборе качества.
func optionsFor(url string, info *mediaInfo) []optionDTO {
	if audioOnly(url, info) {
		return []optionDTO{{"mp3", "MP3"}, {"cover", "MP4 с обложкой"}}
	}
	max := info.maxHeight()
	if info.isPlaylist() {
		max = 2160
	}
	var opts []optionDTO
	if max > 0 {
		opts = append(opts, optionDTO{"h:" + strconv.Itoa(max), "Макс. " + heightLabel(max)})
		for _, h := range qualitySteps {
			if h < max {
				opts = append(opts, optionDTO{"h:" + strconv.Itoa(h), heightLabel(h)})
			}
		}
	} else {
		opts = append(opts, optionDTO{"best", "Видео"})
	}
	return append(opts, optionDTO{"mp3", "Только звук MP3"})
}

func guiDownloadStart(id int64, option, start, end string) error {
	d := findDownload(id)
	if d == nil {
		return errors.New("загрузка не найдена")
	}
	d.mu.Lock()
	if d.status != "ready" {
		d.mu.Unlock()
		return errors.New("загрузка уже идёт")
	}
	var o downloadOptions
	switch {
	case option == "mp3":
		o.audioOnly = true
	case option == "cover":
		o.cover = true
	case strings.HasPrefix(option, "h:"):
		o.height, _ = strconv.Atoi(option[2:])
	}
	if strings.TrimSpace(start+end) != "" && !d.playlist {
		s, e, err := parseRange(strings.TrimSpace(start)+"-"+strings.TrimSpace(end), d.duration)
		if err != nil {
			d.mu.Unlock()
			return err
		}
		if !(d.duration > 0 && s == 0 && e >= d.duration) {
			o.start, o.end = s, e
		}
	}
	info := d.info
	d.status, d.label, d.percent, d.detail = "downloading", "Подключение", -1, ""
	d.mu.Unlock()

	go func() {
		res, yerr := download(d.op, d.URL, info, o, d)
		defer d.op.finish()
		if yerr != nil {
			d.fail(yerr)
			return
		}
		total := recordDownload(d.URL, info, res)
		summaryMu.Lock()
		summaryAt = time.Time{}
		summaryMu.Unlock()
		d.mu.Lock()
		d.status, d.files, d.size = "done", res.files, total
		if res.failed > 0 {
			d.hint = fmt.Sprintf("Не удалось скачать: %d (удалены, приватные или недоступны).", res.failed)
		}
		d.mu.Unlock()
	}()
	return nil
}

// ---------- бот в окне ----------

var (
	botMu       sync.Mutex
	guiBotInst  *bot
	botStarting bool
	botStopping bool
	botErr      string
)

func currentBot() *bot {
	botMu.Lock()
	defer botMu.Unlock()
	return guiBotInst
}

func guiStartBot() {
	botMu.Lock()
	if guiBotInst != nil || botStarting || cfg.BotToken == "" {
		botMu.Unlock()
		return
	}
	botStarting, botErr = true, ""
	botMu.Unlock()
	go func() {
		b, err := startBot(false)
		botMu.Lock()
		defer botMu.Unlock()
		botStarting = false
		if err != nil {
			botErr = err.Error()
			return
		}
		guiBotInst = b
	}()
}

func guiStopBot(offline bool) {
	botMu.Lock()
	b := guiBotInst
	guiBotInst = nil
	if b != nil {
		botStopping = true
	}
	botMu.Unlock()
	if b != nil {
		b.stop(offline)
		botMu.Lock()
		botStopping = false
		botMu.Unlock()
	}
}

func guiBotState() botState {
	st := botState{Visible: botVisible(), HasToken: cfg.BotToken != "", Users: len(friendsCopy()), Autostart: autostartStatus()}
	if cfg.BotToken != "" {
		st.Token = maskToken(cfg.BotToken)
	}
	botMu.Lock()
	b := guiBotInst
	st.Starting, st.Stopping, st.Error = botStarting, botStopping, botErr
	botMu.Unlock()
	if b != nil {
		st.Running, st.Name = true, b.name
		st.Sent, st.Bytes = b.sessionOK.Load(), formatBytes(float64(b.sessionBytes.Load()))
		st.Active = b.active.Load()
	}
	st.Events = eventsView(b, 300) // журнал событий за всё время, а не только за этот запуск
	for _, u := range botDB.requests() {
		when := ""
		if us, ok := botDB.user(u.ID); ok {
			when = us.LastSeen.Format("02.01 15:04")
		}
		st.Requests = append(st.Requests, requestView{ID: u.ID, Name: u.Name, When: when})
	}
	return st
}

type userView struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Access    bool       `json:"access"` // false — доступ убран, но загрузки остались в журнале
	Lang      string     `json:"lang"`
	Premium   bool       `json:"premium"`
	FirstSeen string     `json:"firstSeen"`
	LastSeen  string     `json:"lastSeen"`
	Downloads int        `json:"downloads"`
	Failed    int        `json:"failed"`
	Bytes     string     `json:"bytes"`
	Sites     string     `json:"sites"`
	Links     []linkView `json:"links"` // все ссылки, которые присылал пользователь, новые первыми
}

type linkView struct {
	Time  string `json:"time"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Size  string `json:"size"`
	OK    bool   `json:"ok"`
	Audio bool   `json:"audio"`
	Error string `json:"error"`
}

// guiUsers — пользователи бота: с доступом и те, у кого доступ убран, но остались загрузки.
func guiUsers() []userView {
	out := []userView{} // пустой список, а не null — иначе окно ждёт вечно
	access := map[int64]bool{}
	ids := []int64{}
	for _, u := range friendsCopy() {
		access[u.ID] = true
		ids = append(ids, u.ID)
	}
	for _, st := range botDB.allUsers() {
		if !access[st.ID] && st.Downloads+st.Failed > 0 {
			ids = append(ids, st.ID)
		}
	}
	for _, id := range ids {
		u := userView{ID: id, Access: access[id], Links: []linkView{}}
		if st, ok := botDB.user(id); ok {
			u.Name, u.Lang, u.Premium = st.Name, st.Lang, st.Premium
			u.FirstSeen, u.LastSeen = st.FirstSeen.Format("02.01.2006"), st.LastSeen.Format("02.01 15:04")
			u.Downloads, u.Failed, u.Bytes = st.Downloads, st.Failed, formatBytes(float64(st.Bytes))
			u.Sites = botDB.topSites(id, 3)
		}
		if u.Name == "" {
			for _, f := range friendsCopy() {
				if f.ID == id {
					u.Name = f.Name
				}
			}
		}
		for _, e := range botDB.history(id, botLogLimit, false) {
			l := linkView{Time: e.Time.Format("02.01.2006 15:04"), URL: e.URL, Title: e.Title, OK: e.OK,
				Audio: e.Audio, Error: e.Error}
			if e.Size > 0 {
				l.Size = formatBytes(float64(e.Size))
			}
			u.Links = append(u.Links, l)
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Access != out[j].Access {
			return out[i].Access
		}
		return out[i].Downloads > out[j].Downloads
	})
	return out
}

type journalView struct {
	Time  string `json:"time"`
	Who   string `json:"who"`
	What  string `json:"what"`
	URL   string `json:"url"`
	Size  string `json:"size"`
	OK    bool   `json:"ok"`
	Audio bool   `json:"audio"`
}

func guiJournal() []journalView {
	out := []journalView{}
	for _, e := range botDB.history(0, 200, false) {
		j := journalView{Time: e.Time.Format("02.01 15:04"), What: e.Title, URL: e.URL, OK: e.OK, Audio: e.Audio}
		if !e.OK {
			j.What = e.Error
		}
		if e.Size > 0 {
			j.Size = formatBytes(float64(e.Size))
		}
		if st, ok := botDB.user(e.UserID); ok {
			j.Who = st.Name
			if st.Username != "" {
				j.Who = "@" + st.Username
			}
		}
		out = append(out, j)
	}
	return out
}

type historyView struct {
	ID     int    `json:"id"`
	When   string `json:"when"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	File   string `json:"file"`
	Size   string `json:"size"`
	Exists bool   `json:"exists"`
}

func guiHistory() []historyView {
	list := readHistory()
	out := []historyView{}
	for i := len(list) - 1; i >= 0 && len(out) < 500; i-- {
		e := list[i]
		h := historyView{ID: e.ID, When: e.Date, Title: e.Title, URL: e.URL}
		if e.Time != "" {
			h.When += " " + e.Time[:min(5, len(e.Time))]
		}
		if e.File != "" {
			p := filepath.Join(downloadDir, e.File)
			h.Exists = fileExists(p)
			if h.Exists {
				h.File = p
			}
		}
		if s := e.size(); s > 0 {
			h.Size = formatBytes(float64(s))
		}
		out = append(out, h)
	}
	return out
}

// ---------- обновление yt-dlp ----------

var (
	ytMu       sync.Mutex
	ytUpdating bool
	ytMessage  string
	ytError_   bool
)

func guiUpdateYtdlp() {
	ytMu.Lock()
	if ytUpdating {
		ytMu.Unlock()
		return
	}
	ytUpdating, ytMessage, ytError_ = true, "", false
	ytMu.Unlock()
	go func() {
		msg, err := "", tools.wait()
		if err == nil {
			msg, err = updateYtdlpCore(nil)
		}
		ytMu.Lock()
		defer ytMu.Unlock()
		ytUpdating = false
		if err != nil {
			ytMessage, ytError_ = "Не удалось обновить: "+err.Error(), true
		} else {
			ytMessage = msg
		}
	}()
}
