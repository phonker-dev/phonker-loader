package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var appVersion = "dev" // при сборке подставляется из файла VERSION (build.bat)

var (
	guiMode     bool   // программа работает в окне (а не в консоли)
	appDir      string // папка, где лежит exe
	downloadDir string
)

// Режимы запуска:
//
//	PhonkerLoader.exe            — окно с вкладками (по умолчанию)
//	PhonkerLoader.exe --bot      — окно, бот запускается сразу (автозапуск с Windows)
//	PhonkerLoader.exe --console  — консольный интерфейс, как в версиях 1.x
func main() {
	guiMode = !hasFlag("--console")
	if !guiMode {
		ensureConsole()
		setupConsole()
	}
	openLog()
	defer func() {
		if r := recover(); r != nil {
			logf("АВАРИЯ: %v\n%s", r, debug.Stack())
			if guiMode {
				messageBox(fmt.Sprintf("Программа аварийно остановилась: %v\n\nПожалуйста, отправь этот текст разработчику:\n%s",
					r, truncate(string(debug.Stack()), 1500)))
				shutdown(1)
			}
			fmt.Println()
			printErr("Программа аварийно остановилась: %v", r)
			printHint("Пожалуйста, отправь этот текст разработчику:")
			fmt.Println(dim(string(debug.Stack())))
			waitEnter("Нажми Enter, чтобы закрыть...")
			shutdown(1)
		}
	}()

	jobs.init()
	handleSignals()

	var err error
	if appDir, err = executableDir(); err != nil {
		fatal(err)
	}
	if downloadDir, err = chooseDownloadDir(); err != nil {
		fatal(err)
	}
	loadSettings()
	loadBotData()
	refreshAutostart()
	checkLatestYtdlp()
	if tools, err = newToolset(); err != nil {
		fatal(err)
	}
	go tools.extract()

	if guiMode {
		runGUI()
		shutdown(0)
	}

	// PhonkerLoader.exe --console --bot — сразу запустить бота в консоли.
	if hasFlag("--bot") {
		if cfg.BotToken == "" {
			printErr("Токен бота не указан: [5] Telegram-бот → [6] Токен бота.")
		} else {
			runBot()
		}
	}
	mainMenu()
	shutdown(0)
}
func printMenu() {
	header("PHONKER LOADER  v" + appVersion)
	fmt.Println(" [1] Скачать видео или музыку")
	fmt.Println(" [2] Открыть папку загрузок")
	fmt.Println(" [3] История загрузок")
	fmt.Println(" [4] Статистика")
	if reqs := len(botDB.requests()); reqs > 0 {
		fmt.Println(" [5] Telegram-бот " + yellow(fmt.Sprintf("(ждут доступа: %d)", reqs)))
	} else {
		fmt.Println(" [5] Telegram-бот")
	}
	fmt.Println(" [6] Настройки " + dim("(прокси, cookies, обновление)"))
	fmt.Println(" [7] Как пользоваться")
	fmt.Println(" [0] Выход")
	fmt.Println(rule())
	fmt.Println(dim(" Скачано : ") + statsSummary())
	fmt.Println(dim(" Прокси  : ") + proxyStatusLine())
	if v := ytdlpUpdateAvailable(); v != "" {
		fmt.Println(yellow(" Вышла новая версия yt-dlp " + v + " — обнови: [6] → Обновить"))
	}
	fmt.Println(rule())
	fmt.Println()
}

// mainMenu печатает меню только при входе и после выполненного действия.
// Пустой или неверный ввод не перерисовывает его — приглашение остаётся на месте.
func mainMenu() {
	showMenu, hinted := true, false
	for {
		if showMenu {
			printMenu()
			showMenu, hinted = false, false
		}

		choice := prompt("Вставь ссылку или введи номер: ")
		if looksLikeURL(choice) {
			downloadLoop(choice)
			showMenu = true
			continue
		}
		switch choice {
		case "":
			eraseInputLine()
			continue
		case "1":
			header("ЗАГРУЗКА")
			fmt.Println(" Подходят YouTube, TikTok, VK, Twitch, Instagram и сотни других сайтов.")
			if url := prompt("\nВставь ссылку (Enter — назад): "); url != "" {
				downloadLoop(url)
			}
		case "2":
			openFolder(downloadDir, "")
			eraseInputLine()
			continue
		case "3":
			if url := historyMenu(); url != "" {
				downloadLoop(url)
			}
		case "4":
			statsScreen()
		case "5":
			botMenu()
		case "6":
			settingsMenu()
		case "7", "?":
			helpScreen()
		case "0", "q", "й", "exit", "выход":
			return
		default:
			if hinted {
				eraseInputLine() // подсказка уже на экране — не дублируем
			} else {
				fmt.Println(dim("Нет такого пункта. Введи цифру из меню или вставь ссылку на видео."))
				hinted = true
			}
			continue
		}
		showMenu = true
	}
}

// downloadLoop качает по ссылке и сразу предлагает вставить следующую —
// не нужно каждый раз возвращаться в меню.
func downloadLoop(url string) {
	for url != "" {
		file, finished := downloadFlow(url)
		if !finished {
			return // пользователь отменил на одном из вопросов
		}
		ask := "\nСледующая ссылка (Enter — в меню"
		if file != "" {
			ask += ", O — показать файл"
		}
		in := prompt(dim(ask + "): "))
		switch {
		case file != "" && (strings.EqualFold(in, "o") || strings.EqualFold(in, "щ")):
			openFolder(downloadDir, file)
			url = ""
		case looksLikeURL(in):
			url = in
		default:
			url = ""
		}
	}
}

// downloadFlow — одна загрузка. finished=false — пользователь отменил её на вопросе.
func downloadFlow(url string) (file string, finished bool) {
	url = normalizeURL(url)
	cancelled.Store(false)
	defer cancelled.Store(false)

	header("ЗАГРУЗКА")
	fmt.Println(dim(" " + truncate(url, consoleWidth()-2)))
	if err := tools.wait(); err != nil {
		printErr("%v", err)
		printHint("Возможно, антивирус удалил файлы из временной папки. Добавь программу в исключения.")
		return "", true
	}

	var err error
	if cookieArgs, err = prepareCookies(); err != nil {
		printErr("%v", err)
		printHint("Исправь или убери файл: Настройки → Cookies. Без cookies большинство видео тоже качается.")
		return "", true
	}

	info, yerr := probe(url)
	if yerr != nil {
		reportError(yerr)
		return "", true
	}

	if info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" || info.IsLive {
		printErr("Это прямая трансляция — её можно скачать после окончания эфира.")
		return "", true
	}

	if info.isPlaylist() {
		fmt.Printf(" Плейлист : %s\n", bold(info.Title))
		fmt.Printf(" Видео    : %d\n", len(info.Entries))
		if len(info.Entries) == 0 {
			printErr("Плейлист пуст или недоступен.")
			return "", true
		}
		if !confirm("\nСкачать весь плейлист? [Y/n]: ") {
			return "", false
		}
	} else {
		fmt.Printf(" Название : %s\n", bold(info.Title))
		if a := info.author(); a != "" {
			fmt.Printf(" Канал    : %s\n", a)
		}
		if info.Duration > 0 {
			fmt.Printf(" Длина    : %s\n", formatDuration(info.Duration))
		}
	}

	opts, ok := chooseQuality(url, info)
	if !ok {
		return "", false
	}
	if !info.isPlaylist() {
		if opts.start, opts.end, ok = chooseFragment(info.Duration); !ok {
			return "", false
		}
	}

	fmt.Println()
	op := newOperation()
	res, yerr := download(op, url, info, opts, nil)
	op.finish()
	if yerr != nil {
		reportError(yerr)
		return "", true
	}

	total := recordDownload(url, info, res)

	switch {
	case len(res.files) == 0:
		printOK("Готово. Файл уже был скачан ранее — смотри в папке загрузок.")
	case len(res.files) == 1:
		printOK("Готово: %s (%s)", filepath.Base(res.files[0]), formatBytes(float64(total)))
		file = res.files[0]
	default:
		printOK("Готово: скачано файлов — %d (%s)", len(res.files), formatBytes(float64(total)))
	}
	if res.failed > 0 {
		fmt.Println(yellow(fmt.Sprintf("       Не удалось скачать: %d (удалены, приватные или недоступны)", res.failed)))
	}
	printHint("Папка: %s", downloadDir)
	return file, true
}

var qualitySteps = []int{2160, 1440, 1080, 720, 480, 360}

// chooseQuality спрашивает формат. Для музыкальных платформ (SoundCloud и т.п.) видео нет —
// там сразу MP3 или MP4 со статичной обложкой.
func chooseQuality(link string, info *mediaInfo) (downloadOptions, bool) {
	type option struct {
		label string
		opts  downloadOptions
	}
	var opts []option
	title := "Качество:"
	if audioOnly(link, info) {
		title = "Это музыка — видео здесь нет. Формат:"
		opts = []option{
			{"MP3", downloadOptions{audioOnly: true}},
			{"MP4 с обложкой трека " + dim("(для соцсетей и Telegram)"), downloadOptions{cover: true}},
		}
	} else {
		max := info.maxHeight()
		if info.isPlaylist() {
			max = 2160 // у плоского списка форматов нет — предлагаем всё
		}
		if max > 0 {
			opts = append(opts, option{fmt.Sprintf("Максимальное (%s)", heightLabel(max)), downloadOptions{height: max}})
			for _, h := range qualitySteps {
				if h < max {
					opts = append(opts, option{heightLabel(h), downloadOptions{height: h}})
				}
			}
		} else {
			opts = append(opts, option{"Видео (лучшее доступное)", downloadOptions{}})
		}
		opts = append(opts, option{"Только звук (MP3)", downloadOptions{audioOnly: true}})
	}

	fmt.Println("\n" + title)
	for i, o := range opts {
		fmt.Printf(" [%d] %s\n", i+1, o.label)
	}
	fmt.Println(dim(" [0] Отмена"))
	hinted := false
	for {
		in := prompt("Введи номер (Enter — [1]): ")
		if in == "" {
			return opts[0].opts, true
		}
		if in == "0" {
			return downloadOptions{}, false
		}
		if n, err := strconv.Atoi(in); err == nil && n >= 1 && n <= len(opts) {
			return opts[n-1].opts, true
		}
		if hinted {
			eraseInputLine()
		} else {
			fmt.Println(dim("Нет такого варианта."))
			hinted = true
		}
	}
}
func heightLabel(h int) string {
	switch {
	case h >= 2160:
		return fmt.Sprintf("%dp 4K", h)
	case h >= 1440:
		return fmt.Sprintf("%dp 2K", h)
	}
	return fmt.Sprintf("%dp", h)
}

func chooseFragment(duration float64) (start, end float64, ok bool) {
	fmt.Println("\nФрагмент:")
	fmt.Println(dim(" Enter — скачать целиком"))
	fmt.Println(dim(" 1:15-2:40 — с 1:15 до 2:40;  1:15 — с 1:15 до конца;  -2:40 — с начала до 2:40"))
	for {
		in := prompt("Время: ")
		if in == "" {
			return 0, 0, true
		}
		s, e, err := parseRange(in, duration)
		if err != nil {
			fmt.Println(red(" " + err.Error()))
			continue
		}
		fmt.Printf(" Будет скачан фрагмент %s – %s (%s)\n", formatDuration(s), formatDuration(e), formatDuration(e-s))
		if duration > 0 && s == 0 && e >= duration {
			return 0, 0, true // это и есть всё видео
		}
		return s, e, true
	}
}

func historyMenu() string {
	list := readHistory()
	if len(list) == 0 {
		remember(" История пуста — здесь появятся скачанные видео.")
		return ""
	}
	const show = 20
	header("ИСТОРИЯ ЗАГРУЗОК")
	from := 0
	if len(list) > show {
		from = len(list) - show
	}
	width := consoleWidth() - 1
	for i := len(list) - 1; i >= from; i-- {
		e := list[i]
		date := e.Date
		if e.Time != "" {
			date += " " + e.Time[:min(5, len(e.Time))]
		}
		fmt.Println(truncate(fmt.Sprintf(" [%d] %s | %s", e.ID, date, e.Title), width))
		fmt.Println(dim(truncate("      "+e.URL, width)))
	}
	fmt.Println(rule())
	if from > 0 {
		fmt.Println(dim(fmt.Sprintf(" Показаны последние %d из %d.", show, len(list))))
	}
	in := prompt("Номер — скачать снова, Enter — назад: ")
	if id, err := strconv.Atoi(in); err == nil {
		for _, e := range list {
			if e.ID == id {
				return e.URL
			}
		}
		fmt.Println(dim("Нет записи с таким номером."))
	}
	return ""
}

func reportError(yerr *ytError) {
	if yerr.aborted {
		fmt.Println(yellow("Отменено."))
		return
	}
	text, hint := yerr.explain()
	printErr("%s", text)
	if hint != "" {
		printHint("%s", hint)
	}
	if text != yerr.msg {
		printHint("Подробно: %s", truncate(yerr.msg, 300))
	}
}

func confirm(text string) bool {
	a := strings.ToLower(prompt(text))
	return a == "" || a == "y" || a == "yes" || a == "д" || a == "да" || a == "н"
}

func looksLikeURL(s string) bool {
	if strings.ContainsAny(s, " \t") || len(s) < 6 {
		return false
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return true
	}
	return strings.Contains(low, ".") && strings.Contains(low, "/")
}

func normalizeURL(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"'<>`)
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") && strings.Contains(s, ".") {
		s = "https://" + s
	}
	return s
}

// recordDownload записывает загрузку в историю и возвращает общий размер файлов.
func recordDownload(url string, info *mediaInfo, res *downloadResult) int64 {
	switch {
	case info.isPlaylist():
		for _, f := range res.files {
			appendHistory(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)), url, f)
		}
	case len(res.files) > 0:
		appendHistory(info.Title, url, res.files[0])
	default:
		appendHistory(info.Title, url, "")
	}
	var total int64
	for _, f := range res.files {
		if st, err := os.Stat(f); err == nil {
			total += st.Size()
		}
	}
	return total
}

// ---------- папки ----------

func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// chooseDownloadDir: папка Downloads рядом с программой, а если туда нельзя
// писать (например, exe лежит в Program Files) — Видео\Phonker Loader.
func chooseDownloadDir() (string, error) {
	candidates := []string{filepath.Join(appDir, "Downloads")}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "Videos", "Phonker Loader"))
	}
	for _, dir := range candidates {
		if writable(dir) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("нет папки, доступной для записи: %s", strings.Join(candidates, ", "))
}

func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

func openFolder(dir, selectFile string) {
	_ = os.MkdirAll(dir, 0o755)
	cmd := exec.Command("explorer.exe")
	if selectFile != "" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + selectFile + `"`}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe "` + dir + `"`}
	}
	_ = cmd.Start()
	go cmd.Wait()
}

// ---------- завершение ----------

func handleSignals() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		for sig := range ch {
			// Ctrl+C во время загрузки — отменить загрузку (yt-dlp получает Ctrl+C сам).
			if sig == os.Interrupt && (running.Load() > 0 || botActive.Load()) {
				cancelled.Store(true)
				continue
			}
			// Ctrl+C в меню или закрытие окна — выйти, убрав за собой временные файлы.
			// На закрытие окна Windows даёт около 5 секунд.
			fmt.Println()
			shutdown(0)
		}
	}()
}

var shutdownOnce sync.Once

func shutdown(code int) {
	shutdownOnce.Do(func() {
		logf("выход, код %d", code)
		if f := botOnExit.Load(); f != nil {
			(*f)() // бот работал — ставим статус «не в сети»
		}
		if guiMode {
			closeWindow()
		}
		if tools != nil {
			tools.remove()
		}
	})
	os.Exit(code)
}

func hasFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	return false
}

func fatal(err error) {
	if guiMode {
		messageBox("Не удалось запустить программу: " + err.Error())
		shutdown(1)
	}
	printErr("%v", err)
	waitEnter("Нажми Enter, чтобы закрыть...")
	shutdown(1)
}
