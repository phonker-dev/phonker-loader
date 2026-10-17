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

var appVersion = "1.1.0" // перезаписывается при сборке: -ldflags "-X main.appVersion=..."

var (
	appDir      string // папка, где лежит exe
	downloadDir string
)

func main() {
	setupConsole()
	defer func() {
		if r := recover(); r != nil {
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
	if tools, err = newToolset(); err != nil {
		fatal(err)
	}
	go tools.extract()

	mainMenu()
	shutdown(0)
}

func printMenu() {
	header("PHONKER LOADER  v" + appVersion)
	fmt.Println(" [1] Скачать видео или музыку")
	fmt.Println(" [2] Открыть папку загрузок")
	fmt.Println(" [3] История загрузок")
	fmt.Println(" [4] Обновить yt-dlp")
	if cookiesSource() != "" {
		fmt.Println(" [5] Cookies " + green("(подключены)"))
	} else {
		fmt.Println(" [5] Cookies " + dim("(не нужны для большинства видео)"))
	}
	fmt.Println(" [0] Выход")
	fmt.Println(rule)
	if age := ytdlpAge(tools.ytdlpVersion); age > 60 {
		fmt.Println(yellow(fmt.Sprintf(" yt-dlp %s устарел (%d дн.) — если видео не качаются, выбери [4]",
			tools.ytdlpVersion, age)))
	}
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

		choice := prompt("Введи номер или сразу вставь ссылку: ")
		if looksLikeURL(choice) {
			downloadFlow(choice)
			showMenu = true
			continue
		}
		switch choice {
		case "":
			eraseInputLine()
			continue
		case "1":
			url := prompt("\nВставь ссылку (YouTube, Twitch и др.): ")
			if url != "" {
				downloadFlow(url)
			}
		case "2":
			openFolder(downloadDir, "")
			eraseInputLine()
			continue
		case "3":
			if url := historyMenu(); url != "" {
				downloadFlow(url)
			}
		case "4":
			fmt.Println()
			if err := tools.wait(); err != nil {
				printErr("%v", err)
			} else if err := updateYtdlp(); err != nil {
				cancelled.Store(false)
				printErr("Не удалось обновить: %v", err)
			}
		case "5":
			cookiesMenu()
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
func downloadFlow(url string) {
	url = normalizeURL(url)
	cancelled.Store(false)
	defer cancelled.Store(false)

	fmt.Println()
	if err := tools.wait(); err != nil {
		printErr("%v", err)
		printHint("Возможно, антивирус удалил файлы из временной папки. Добавь программу в исключения.")
		return
	}

	var err error
	if cookieArgs, err = prepareCookies(); err != nil {
		printErr("%v", err)
		printHint("Исправь или убери файл в пункте меню [5]. Без cookies большинство видео тоже качается.")
		return
	}

	info, yerr := probe(url)
	if yerr != nil {
		reportError(yerr)
		return
	}

	if info.LiveStatus == "is_live" || info.LiveStatus == "is_upcoming" || info.IsLive {
		printErr("Это прямая трансляция — её можно скачать после окончания эфира.")
		return
	}

	opts := downloadOptions{}
	if info.isPlaylist() {
		fmt.Printf(" Плейлист : %s\n", bold(info.Title))
		fmt.Printf(" Видео    : %d\n", len(info.Entries))
		if len(info.Entries) == 0 {
			printErr("Плейлист пуст или недоступен.")
			return
		}
		if !confirm("\nСкачать весь плейлист? [Y/n]: ") {
			return
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

	var ok bool
	if opts.height, opts.audioOnly, ok = chooseQuality(info); !ok {
		return
	}
	if !info.isPlaylist() {
		if opts.start, opts.end, ok = chooseFragment(info.Duration); !ok {
			return
		}
	}

	fmt.Println()
	res, yerr := download(url, info, opts)
	if yerr != nil {
		reportError(yerr)
		return
	}

	switch {
	case len(res.files) == 0:
		printOK("Готово. Файл уже был скачан ранее — смотри в папке загрузок.")
	case len(res.files) == 1:
		printOK("Готово: %s", filepath.Base(res.files[0]))
	default:
		printOK("Готово: скачано файлов — %d", len(res.files))
	}
	if res.failed > 0 {
		fmt.Println(yellow(fmt.Sprintf("       Не удалось скачать: %d (удалены, приватные или недоступны)", res.failed)))
	}
	printHint("Папка: %s", downloadDir)

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

	if len(res.files) == 1 {
		if a := prompt(dim("\nEnter — в меню, O — показать файл в папке: ")); strings.EqualFold(a, "o") || a == "щ" || a == "Щ" {
			openFolder(downloadDir, res.files[0])
		}
	}
}

var qualitySteps = []int{2160, 1440, 1080, 720, 480, 360}

func chooseQuality(info *mediaInfo) (height int, audio, ok bool) {
	max := info.maxHeight()
	if info.isPlaylist() {
		max = 2160 // у плоского списка форматов нет — предлагаем всё
	}

	type option struct {
		label  string
		height int
		audio  bool
	}
	var opts []option
	if max > 0 {
		opts = append(opts, option{fmt.Sprintf("Максимальное (%s)", heightLabel(max)), max, false})
		for _, h := range qualitySteps {
			if h < max {
				opts = append(opts, option{heightLabel(h), h, false})
			}
		}
	} else {
		opts = append(opts, option{"Видео (лучшее доступное)", 0, false})
	}
	opts = append(opts, option{"Только звук (MP3)", 0, true})

	fmt.Println("\nКачество:")
	for i, o := range opts {
		fmt.Printf(" [%d] %s\n", i+1, o.label)
	}
	fmt.Println(dim(" [0] Отмена"))
	hinted := false
	for {
		in := prompt("Введи номер (Enter — " + opts[0].label + "): ")
		if in == "" {
			return opts[0].height, opts[0].audio, true
		}
		if in == "0" {
			return 0, false, false
		}
		if n, err := strconv.Atoi(in); err == nil && n >= 1 && n <= len(opts) {
			return opts[n-1].height, opts[n-1].audio, true
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
		fmt.Println("\nИстория пуста.")
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
	fmt.Println(rule)
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
			if sig == os.Interrupt && running.Load() {
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
		if tools != nil {
			tools.remove()
		}
	})
	os.Exit(code)
}

func fatal(err error) {
	printErr("%v", err)
	waitEnter("Нажми Enter, чтобы закрыть...")
	shutdown(1)
}
