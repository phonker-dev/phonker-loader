package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- метаданные ----------

type mediaInfo struct {
	Type       string  `json:"_type"`
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Uploader   string  `json:"uploader"`
	Channel    string  `json:"channel"`
	Thumbnail  string  `json:"thumbnail"`
	Duration   float64 `json:"duration"`
	LiveStatus string  `json:"live_status"`
	IsLive     bool    `json:"is_live"`
	Formats    []struct {
		Height float64 `json:"height"`
		VCodec string  `json:"vcodec"`
	} `json:"formats"`
	Entries []json.RawMessage `json:"entries"`

	infoFile string // сохранённый JSON — загрузка идёт по нему без повторного запроса к сайту
}

func (m *mediaInfo) isPlaylist() bool { return m.Type == "playlist" || m.Type == "multi_video" }

func (m *mediaInfo) author() string {
	if m.Channel != "" {
		return m.Channel
	}
	return m.Uploader
}

// maxHeight — наибольшее доступное разрешение видео (0 — неизвестно).
func (m *mediaInfo) maxHeight() int {
	max := 0
	for _, f := range m.Formats {
		if f.VCodec != "none" && int(f.Height) > max {
			max = int(f.Height)
		}
	}
	return max
}

// cookieArgs — аргументы cookies для текущей загрузки (см. cookies.go).
var cookieArgs []string

// ---------- запуск yt-dlp ----------

func baseArgs() []string {
	args := []string{
		"--ignore-config",
		"--no-update",
		"--encoding", "utf-8",
		"--no-warnings",
		"--cache-dir", filepath.Join(tools.dir, "cache"),
		"--js-runtimes", "deno:" + tools.deno,
		"--ffmpeg-location", tools.ffmpeg,
		"--force-ipv4",
	}
	args = append(args, proxyArgs()...)
	return append(args, cookieArgs...)
}

type ytError struct {
	msg     string // последняя строка ERROR от yt-dlp
	stderr  string
	aborted bool
}

func (e *ytError) Error() string { return e.msg }

func newYtError(stderr string, runErr error) *ytError {
	e := &ytError{stderr: stderr}
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ERROR:") {
			e.msg = strings.TrimSpace(strings.TrimPrefix(line, "ERROR:"))
		}
	}
	if e.msg == "" {
		if s := strings.TrimSpace(stderr); s != "" {
			lines := strings.Split(s, "\n")
			e.msg = strings.TrimSpace(lines[len(lines)-1])
		} else if runErr != nil {
			e.msg = runErr.Error()
		} else {
			e.msg = "неизвестная ошибка"
		}
	}
	return e
}

// explain переводит типичные ошибки yt-dlp в понятные пользователю.
func (e *ytError) explain() (text, hint string) {
	s := strings.ToLower(e.msg)
	has := func(keys ...string) bool {
		for _, k := range keys {
			if strings.Contains(s, k) {
				return true
			}
		}
		return false
	}
	switch {
	case has("getaddrinfo", "failed to resolve", "name resolution", "network is unreachable",
		"timed out", "connection refused", "no route to host", "connection reset"):
		return "нет соединения с сайтом", networkHint()
	case has("unsupported url"):
		return "ссылка не поддерживается", "Нужна ссылка на конкретное видео, shorts, плейлист или запись трансляции."
	case has("is not a valid url", "no such file", "unable to open"):
		return "это не похоже на ссылку", "Скопируй адрес видео из браузера целиком, начиная с https://"
	case has("private video"):
		return "видео приватное", loginHint()
	case has("confirm your age", "age-restricted", "inappropriate"):
		return "видео 18+, YouTube требует вход в аккаунт", loginHint()
	case has("not a bot"):
		return "YouTube принял программу за бота и требует вход", loginHint()
	case has("members"):
		return "видео только для спонсоров канала", loginHint()
	case has("sign in", "log in", "login required", "use --cookies"):
		return "сайт требует вход в аккаунт", loginHint()
	case has("video unavailable", "has been removed", "does not exist", "404"):
		return "видео недоступно (удалено, заблокировано или ссылка с ошибкой)", ""
	case has("not available in your country", "geo restrict", "blocked it in your country"):
		return "видео заблокировано в твоём регионе", "Попробуй через VPN."
	case has("requested format is not available"):
		return "нужное качество недоступно", "Выбери другое качество."
	case has("http error 403", "forbidden"):
		return "YouTube отклонил запрос (ошибка 403), повторные попытки не помогли", forbiddenHint()
	case has("unable to extract", "signature", "nsig", "player response", "challenge", "po token"):
		return "сайт изменил защиту и отклонил запрос", "Обнови yt-dlp: Настройки → Обновить yt-dlp, и попробуй снова."
	case has("no space left", "disk full"):
		return "на диске закончилось место", ""
	case has("permission denied", "access is denied"):
		return "нет доступа к файлу или папке", "Возможно, файл открыт в плеере или папка защищена от записи."
	}
	return e.msg, ""
}

// forbidden — сайт ответил 403. У YouTube это обычно временно и лечится свежими ссылками.
func (e *ytError) forbidden() bool {
	return strings.Contains(e.msg, "403") || strings.Contains(e.stderr, "HTTP Error 403")
}

func forbiddenHint() string {
	h := "Подожди пару минут и попробуй снова. Если пользуешься VPN — смени сервер."
	if len(cookieArgs) > 0 {
		h += "\n       С cookies YouTube отдаёт 403 чаще — попробуй отключить их (Настройки → Cookies)."
	}
	return h
}

func networkHint() string {
	addr, source := currentProxy()
	if addr == "" {
		return "Проверь интернет. Если сайт открывается только через VPN или прокси — включи его."
	}
	if !proxyAlive(addr) {
		return "Используется прокси " + addr + " (" + source + "), но он не отвечает.\n" +
			"       Запусти v2rayN/Happ или выключи прокси: Настройки → Прокси."
	}
	return "Используется прокси " + addr + " (" + source + "). Попробуй сменить сервер в v2rayN/Happ."
}

// loginHint — подсказка для видео, требующих вход в аккаунт.
func loginHint() string {
	if len(cookieArgs) > 0 {
		return "Cookies указаны, но сайт их не принял — скорее всего, они устарели.\n" +
			"       Экспортируй их заново из браузера (Настройки → Cookies)."
	}
	return "Укажи файл со своими cookies: Настройки → Cookies, и повтори загрузку."
}

// run запускает yt-dlp, передавая каждую строку stdout/stderr в onLine.
// Строки делятся и по \n, и по \r — так yt-dlp выводит прогресс.
func run(op *operation, args []string, onStdout func(string), stdoutTo io.Writer) *ytError {
	cmd := tools.command(args...)
	var stderr bytes.Buffer
	stderrPipe, _ := cmd.StderrPipe()
	var stdoutPipe io.ReadCloser
	if stdoutTo != nil {
		cmd.Stdout = stdoutTo
	} else {
		stdoutPipe, _ = cmd.StdoutPipe()
	}
	if err := start(cmd, op); err != nil {
		return &ytError{msg: "не удалось запустить yt-dlp: " + err.Error()}
	}
	running.Add(1)
	defer running.Add(-1)

	var wg sync.WaitGroup
	var stderrMu sync.Mutex
	scan := func(r io.Reader, fn func(string)) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		sc.Split(scanLinesCR)
		for sc.Scan() {
			fn(sc.Text())
		}
	}
	wg.Add(1)
	go scan(stderrPipe, func(s string) {
		if strings.HasPrefix(s, "@@") && onStdout != nil {
			onStdout(s) // шаблоны постобработки yt-dlp печатает в stderr
			return
		}
		stderrMu.Lock()
		stderr.WriteString(s + "\n")
		stderrMu.Unlock()
	})
	if stdoutPipe != nil {
		wg.Add(1)
		go scan(stdoutPipe, func(s string) {
			if onStdout != nil {
				onStdout(s)
			}
		})
	}
	wg.Wait()
	err := cmd.Wait()
	if op.aborted() {
		return &ytError{msg: "отменено", aborted: true}
	}
	if err != nil {
		return newYtError(stderr.String(), err)
	}
	return nil
}

func scanLinesCR(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, bytes.TrimRight(data[:i], "\r"), nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

var (
	running   atomic.Int32 // сколько yt-dlp сейчас работает — Ctrl+C отменяет их, а не закрывает программу
	cancelled atomic.Bool
	infoSeq   atomic.Int64
)

// probe получает информацию о видео (консольный режим — со спиннером).
func probe(url string) (*mediaInfo, *ytError) {
	l := startLive("Получаю информацию о видео...")
	defer l.Stop()
	return probeInfo(nil, url)
}

// probeInfo получает информацию о видео; при 403 повторяет один раз.
func probeInfo(op *operation, url string) (*mediaInfo, *ytError) {
	info, yerr := probeOnce(op, url)
	if yerr != nil && yerr.forbidden() && !op.aborted() {
		time.Sleep(2 * time.Second)
		info, yerr = probeOnce(op, url)
	}
	return info, yerr
}

func probeOnce(op *operation, url string) (*mediaInfo, *ytError) {
	path := filepath.Join(tools.dir, fmt.Sprintf("info-%d.json", infoSeq.Add(1)))
	f, err := os.Create(path)
	if err != nil {
		return nil, &ytError{msg: err.Error()}
	}
	args := append(baseArgs(), "-J", "--flat-playlist", "--no-playlist", "--", url)
	yerr := run(op, args, nil, f)
	f.Close()
	if yerr != nil {
		os.Remove(path)
		return nil, yerr
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ytError{msg: err.Error()}
	}
	var info mediaInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, &ytError{msg: "yt-dlp вернул некорректные данные"}
	}
	info.infoFile = path
	if info.Title == "" {
		info.Title = info.ID
	}
	return &info, nil
}

// ---------- загрузка ----------

type downloadOptions struct {
	height    int  // 0 — лучшее доступное (разрешение неизвестно)
	audioOnly bool // MP3
	cover     bool // MP4 со статичной обложкой — для музыкальных платформ
	start     float64
	end       float64 // 0 — без нарезки
}

type downloadResult struct {
	files  []string
	failed int
}

const progressTemplate = "download:@@P %(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|" +
	"%(progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s|%(info.vcodec)s|" +
	"%(info.playlist_index)s|%(info.n_entries)s|%(info.title)s"

func formatArgs(o downloadOptions) []string {
	if o.cover {
		return coverArgs()
	}
	if o.audioOnly {
		return []string{
			"-f", "ba/b",
			"-x", "--audio-format", "mp3", "--audio-quality", "0",
			"--embed-metadata", "--embed-thumbnail", "--convert-thumbnails", "jpg",
		}
	}
	// До 1080p включительно предпочитаем H.264 + AAC — такой mp4 откроется
	// в любом плеере и на любом устройстве. Выше 1080p H.264 у YouTube нет,
	// там берём VP9 (стандартный плеер Windows 10/11 его воспроизводит), а не AV1.
	codec := "vcodec:h264"
	if o.height == 0 || o.height > 1080 {
		codec = "vcodec:vp9"
	}
	res := "res"
	if o.height > 0 {
		res = "res:" + strconv.Itoa(o.height)
	}
	return []string{
		"-f", "bv*+ba/b",
		"-S", res + ",fps," + codec + ",acodec:aac",
		"--merge-output-format", "mp4",
	}
}

const maxAttempts = 3

// progressSink — куда показывать прогресс: строка в консоли или карточка в окне.
type progressSink interface {
	Set(label string, percent float64, detail string)
	SetLabel(label string)
	Println(s string)
}

// download качает по ссылке. op — операция (отмена, временная папка), sink — прогресс;
// без sink прогресс рисуется строкой в консоли.
func download(op *operation, url string, info *mediaInfo, o downloadOptions, sink progressSink) (*downloadResult, *ytError) {
	work := op.work
	// Для MP4 с обложкой yt-dlp кладёт звук и картинку во временную папку,
	// а готовое видео собирается уже в папку загрузок.
	home := downloadDir
	coverDir := filepath.Join(work, "cover")
	if o.cover {
		home = coverDir
	}
	args := baseArgs()
	args = append(args, formatArgs(o)...)
	args = append(args,
		"-P", home,
		"-P", "temp:"+work,
		"--no-mtime",
		"--concurrent-fragments", "4",
		"--quiet", "--progress", "--newline",
		"--progress-template", progressTemplate,
		"--progress-template", "postprocess:@@PP %(progress.postprocessor)s|%(progress.status)s",
		"--print", "after_move:@@F %(filepath)s",
		"--no-simulate",
	)

	name := "%(title).150s"
	if o.end > 0 {
		args = append(args, "--download-sections", fmt.Sprintf("*%.3f-%.3f", o.start, o.end))
		name += " [" + fileTimecode(o.start) + "-" + fileTimecode(o.end) + "]"
	}
	if info.isPlaylist() {
		args = append(args, "--yes-playlist", "--ignore-errors",
			"-o", "%(playlist_title).80s/%(playlist_index)03d. "+name+".%(ext)s", "--", url)
	} else {
		args = append(args, "-o", name+".%(ext)s")
	}

	res := &downloadResult{}
	seen := map[string]bool{}
	var l progressSink = sink
	var live *liveLine
	if sink == nil {
		live = startLive("Подключение...")
		l = live
	}
	p := &progressView{live: l, audioOnly: o.audioOnly || o.cover, playlist: info.isPlaylist()}
	onLine := func(line string) {
		if strings.HasPrefix(line, "@@F ") {
			if f := strings.TrimSpace(line[4:]); !seen[f] {
				seen[f] = true
				res.files = append(res.files, f)
			}
			return
		}
		p.handle(line)
	}

	var yerr *ytError
	for attempt := 1; ; attempt++ {
		a := args
		if !info.isPlaylist() {
			a = append(a[:len(a):len(a)], "--load-info-json", info.infoFile)
		}
		yerr = run(op, a, onLine, nil)
		if yerr == nil || yerr.aborted || !yerr.forbidden() || attempt == maxAttempts {
			break
		}

		// YouTube время от времени отвечает 403 на ссылки видеопотока (особенно на
		// длинных загрузках). Лечится получением свежих ссылок; недокачанные части
		// остаются в папке work, и yt-dlp продолжает с того же места.
		l.Println(yellow(fmt.Sprintf(" YouTube отклонил запрос (403). Получаю новые ссылки, попытка %d из %d...",
			attempt+1, maxAttempts)))
		l.SetLabel("Получаю новые ссылки...")
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
		if op.aborted() {
			yerr = &ytError{msg: "отменено", aborted: true}
			break
		}
		if !info.isPlaylist() {
			fresh, perr := probeOnce(op, url)
			if perr != nil {
				yerr = perr
				if perr.aborted || !perr.forbidden() {
					break
				}
				continue
			}
			info.infoFile = fresh.infoFile
		}
	}
	if o.cover && len(res.files) > 0 && (yerr == nil || (info.isPlaylist() && !yerr.aborted)) {
		for i, f := range res.files {
			l.Set("Собираю видео с обложкой", -1, fmt.Sprintf("%d из %d", i+1, len(res.files)))
			rel, _ := filepath.Rel(coverDir, filepath.Dir(f))
			outDir := filepath.Join(downloadDir, rel)
			_ = os.MkdirAll(outDir, 0o755)
			out, err := makeCoverVideo(op, f, outDir)
			if err != nil {
				yerr = &ytError{msg: err.Error(), aborted: op.aborted()}
				res.files = res.files[:i]
				break
			}
			res.files[i] = out
		}
	}
	if live != nil {
		live.Stop()
		setTitle("")
	}
	os.RemoveAll(work) // недокачанные части, если были
	os.MkdirAll(work, 0o755)

	if yerr != nil && info.isPlaylist() && len(res.files) > 0 && !yerr.aborted {
		res.failed = countErrors(yerr.stderr)
		return res, nil
	}
	return res, yerr
}
func countErrors(stderr string) int {
	n := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ERROR:") {
			n++
		}
	}
	return n
}

// progressView превращает строки шаблона прогресса yt-dlp в строку статуса.
type progressView struct {
	live      progressSink
	audioOnly bool
	playlist  bool
	lastItem  string
}

func (p *progressView) handle(line string) {
	switch {
	case strings.HasPrefix(line, "@@PP "):
		name, status, _ := strings.Cut(line[5:], "|")
		if status == "started" {
			p.live.SetLabel(postprocessorLabel(name))
			setTitle("обработка")
		}
	case strings.HasPrefix(line, "@@P "):
		f := strings.SplitN(line[4:], "|", 10)
		if len(f) < 10 {
			return
		}
		if p.playlist && f[7] != "NA" && f[7] != p.lastItem {
			p.lastItem = f[7]
			p.live.Println(fmt.Sprintf(" [%s/%s] %s", f[7], f[8], truncate(f[9], consoleWidth()-14)))
		}
		done, total := num(f[1]), num(f[2])
		if total <= 0 {
			total = num(f[3])
		}
		speed, eta := num(f[4]), num(f[5])

		label := "Загрузка"
		switch {
		case p.audioOnly || f[6] == "none":
			label = "Аудио"
		case strings.Contains(f[6], "+") || f[6] == "NA":
			label = "Загрузка"
		default:
			label = "Видео"
		}

		pct := -1.0
		detail := ""
		if total > 0 && done >= 0 {
			pct = done / total * 100
			detail = formatBytes(done) + " / " + formatBytes(total)
		} else if done > 0 {
			detail = formatBytes(done)
		}
		if speed > 0 {
			detail += "  " + formatBytes(speed) + "/с"
		}
		if eta > 0 {
			detail += "  ~" + formatDuration(eta)
		}
		p.live.Set(label, pct, detail)
		if pct >= 0 {
			setTitle(fmt.Sprintf("%.0f%%", pct))
		}
	}
}

func postprocessorLabel(name string) string {
	switch name {
	case "Merger":
		return "Склейка видео и звука..."
	case "ExtractAudio":
		return "Конвертация в MP3..."
	case "ThumbnailsConvertor":
		return "Подготовка обложки..."
	case "EmbedThumbnail":
		return "Добавление обложки..."
	case "FFmpegMetadata", "Metadata":
		return "Запись метаданных..."
	case "MoveFiles":
		return "Сохранение файла..."
	}
	return "Обработка..."
}

func num(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return v
}

// ---------- обновление yt-dlp ----------

// updateYtdlp — консольный вариант: спиннер и сообщение о результате.
func updateYtdlp() error {
	l := startLive("Проверка обновлений yt-dlp...")
	msg, err := updateYtdlpCore(nil)
	l.Stop()
	if err == nil {
		printOK("%s", msg)
	}
	return err
}

// updateYtdlpCore обновляет yt-dlp и возвращает сообщение для пользователя.
func updateYtdlpCore(op *operation) (string, error) {
	var out bytes.Buffer
	cmd := tools.command("--ignore-config", "--encoding", "utf-8", "-U")
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := start(cmd, op)
	if err == nil {
		running.Add(1)
		err = cmd.Wait()
		running.Add(-1)
	}
	if op.aborted() {
		return "", errors.New("отменено")
	}
	if err != nil {
		yerr := newYtError(out.String(), err)
		text, _ := yerr.explain()
		return "", errors.New(text)
	}

	verOut, err := tools.command("--version").Output()
	if err != nil {
		return "", fmt.Errorf("не удалось проверить версию: %w", err)
	}
	version := strings.TrimSpace(string(verOut))
	latestYtdlp.Store(version) // только что спросили у GitHub — это последняя
	if version == tools.ytdlpVersion {
		return "Установлена последняя версия yt-dlp (" + version + ").", nil
	}

	dir := updateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := copyFile(tools.ytdlp, filepath.Join(dir, "yt-dlp.exe")); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "yt-dlp.version"), []byte(version), 0o644); err != nil {
		return "", err
	}
	msg := "yt-dlp обновлён: " + tools.ytdlpVersion + " → " + version
	tools.ytdlpVersion = version
	return msg, nil
}
