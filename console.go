package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP         = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP               = kernel32.NewProc("SetConsoleCP")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procSetConsoleTitleW           = kernel32.NewProc("SetConsoleTitleW")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procFlushConsoleInputBuffer    = kernel32.NewProc("FlushConsoleInputBuffer")
)

const enableVirtualTerminalProcessing = 0x0004

// vt — поддерживает ли консоль ANSI-последовательности (цвета, очистка строки).
// На старых сборках Windows 10 их нет, тогда работаем без цвета.
var vt bool

// isConsole — вывод идёт в окно консоли, а не в файл или pipe (тогда спиннер не рисуем).
var isConsole bool

// stdinConsole — ввод идёт с клавиатуры (а не из файла/pipe).
var stdinConsole bool

// Ввод читает одна горутина и передаёт строки в канал. Так и обычные вопросы,
// и команды во время работы бота берут ввод из одного места и не отнимают его друг у друга.
var (
	inputOnce  sync.Once
	inputLines = make(chan string)
)

func startInput() {
	inputOnce.Do(func() {
		go func() {
			r := bufio.NewReader(os.Stdin)
			for {
				line, err := r.ReadString('\n')
				if line != "" || err == nil {
					inputLines <- strings.TrimSpace(line)
				}
				if err != nil {
					close(inputLines)
					return
				}
			}
		}()
	})
}

// readLine ждёт строку ввода; ok=false — ввод закрыт.
func readLine() (string, bool) {
	startInput()
	line, ok := <-inputLines
	return line, ok
}

func setupConsole() {
	procSetConsoleOutputCP.Call(65001)
	procSetConsoleCP.Call(65001)

	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r != 0 {
		isConsole = true
		r, _, _ = procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
		vt = r != 0
	}
	setTitle("")

	var inMode uint32
	if r, _, _ := procGetConsoleMode.Call(os.Stdin.Fd(), uintptr(unsafe.Pointer(&inMode))); r != 0 {
		stdinConsole = true
	}
}

func setTitle(prefix string) {
	title := "Phonker Loader"
	if prefix != "" {
		title = prefix + " — " + title
	}
	p, err := syscall.UTF16PtrFromString(title)
	if err == nil {
		procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
	}
}

type consoleScreenBufferInfo struct {
	sizeX, sizeY       int16
	cursorX, cursorY   int16
	attributes         uint16
	left, top          int16
	right, bottom      int16
	maxSizeX, maxSizeY int16
}

func consoleWidth() int {
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(os.Stdout.Fd(), uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 80
	}
	if w := int(info.right-info.left) + 1; w > 20 {
		return w
	}
	return 80
}

// ---------- оформление ----------

// rule — разделитель на всю ширину окна (но не шире 100 символов), чтобы текст не вылезал за него.
func rule() string {
	return strings.Repeat("=", ruleWidth())
}

func ruleWidth() int {
	return max(45, min(consoleWidth()-1, 100))
}

func paint(code, s string) string {
	if !vt {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func red(s string) string    { return paint("91", s) }
func green(s string) string  { return paint("92", s) }
func yellow(s string) string { return paint("93", s) }
func dim(s string) string    { return paint("90", s) }
func bold(s string) string   { return paint("1", s) }

// header начинает новый экран: очищает консоль и рисует заголовок. Под ним — итог
// предыдущего действия (строки [OK]/[ОШИБКА], которые пользователь ещё не видел).
func header(title string) {
	clearScreen()
	fmt.Println(rule())
	fmt.Println(center(title, ruleWidth()))
	fmt.Println(rule())
	if len(flash) > 0 {
		for _, line := range flash {
			fmt.Println(line)
		}
		fmt.Println(rule())
		flash = nil
	}
}

func clearScreen() {
	if vt && isConsole {
		fmt.Print("\033[2J\033[3J\033[H") // экран и прокрутка
	} else {
		fmt.Println()
	}
}

// flash — сообщения о результате, которые нужно показать и на следующем экране,
// если пользователь не успел их прочитать (между ними не было вопроса).
var flash []string

func remember(line string) {
	fmt.Println(line)
	flash = append(flash, line)
}

func center(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return strings.Repeat(" ", (width-n)/2) + s
}

func printOK(format string, a ...any) {
	remember(green("[OK]") + " " + fmt.Sprintf(format, a...))
}

func printErr(format string, a ...any) {
	remember(red("[ОШИБКА]") + " " + fmt.Sprintf(format, a...))
}

func printHint(format string, a ...any) {
	remember(dim("       " + fmt.Sprintf(format, a...)))
}

func clearLine() {
	if !isConsole {
		return
	}
	if vt {
		fmt.Print("\r\033[K")
		return
	}
	fmt.Print("\r" + strings.Repeat(" ", consoleWidth()-1) + "\r")
}

// truncate обрезает строку по количеству символов (не байт).
func truncate(s string, max int) string {
	if max <= 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// ---------- ввод ----------

// prompt выводит приглашение и читает строку. При закрытом stdin завершает программу.
func prompt(text string) string {
	fmt.Print(text)
	line, ok := readLine()
	if !ok {
		shutdown(0)
	}
	flash = nil // пользователь ответил — значит, всё на экране он видел
	return line
}

// flushInput выбрасывает всё, что пользователь успел нажать, пока программа была занята
// (иначе лишние Enter во время загрузки потом «прокликают» меню).
func flushInput() {
	if !stdinConsole {
		return
	}
	procFlushConsoleInputBuffer.Call(os.Stdin.Fd())
	for {
		select {
		case _, ok := <-inputLines: // строка, которую горутина ввода уже успела прочитать
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// eraseInputLine стирает строку, на которой пользователь только что нажал Enter,
// чтобы следующее приглашение встало на её место.
func eraseInputLine() {
	if vt && isConsole && stdinConsole {
		fmt.Print("\033[1A\r\033[K")
	}
}

func waitEnter(text string) {
	prompt(dim(text))
}

// ---------- живая строка статуса (спиннер + прогресс) ----------

type liveLine struct {
	mu      sync.Mutex
	label   string
	percent float64 // < 0 — прогресс неизвестен
	detail  string
	frame   int
	stop    chan struct{}
	done    chan struct{}
}

var spinnerFrames = []string{"|", "/", "-", "\\"}

func startLive(label string) *liveLine {
	l := &liveLine{label: label, percent: -1, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			l.render()
			select {
			case <-l.stop:
				clearLine()
				return
			case <-t.C:
			}
		}
	}()
	return l
}

func (l *liveLine) Set(label string, percent float64, detail string) {
	l.mu.Lock()
	l.label, l.percent, l.detail = label, percent, detail
	l.mu.Unlock()
}

func (l *liveLine) SetLabel(label string) { l.Set(label, -1, "") }

// Println печатает постоянную строку над строкой статуса.
func (l *liveLine) Println(s string) {
	l.mu.Lock()
	clearLine()
	fmt.Println(s)
	l.mu.Unlock()
}

func (l *liveLine) Stop() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
	<-l.done
	flushInput()
}

func (l *liveLine) render() {
	if !isConsole {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	width := consoleWidth() - 1
	frame := spinnerFrames[l.frame%len(spinnerFrames)]
	l.frame++

	line := fmt.Sprintf("[%s] %s", frame, l.label)
	if l.percent >= 0 {
		pct := fmt.Sprintf("%5.1f%%", l.percent)
		rest := utf8.RuneCountInString(line) + len(pct) + 6 + utf8.RuneCountInString(l.detail)
		barW := width - rest
		if barW > 30 {
			barW = 30
		}
		if barW >= 10 {
			filled := int(l.percent / 100 * float64(barW))
			if filled > barW {
				filled = barW
			}
			line += "  [" + strings.Repeat("#", filled) + strings.Repeat("-", barW-filled) + "]"
		}
		line += " " + pct
	}
	if l.detail != "" {
		line += "  " + l.detail
	}
	clearLine()
	fmt.Print(truncate(line, width))
}

// ---------- форматирование ----------

func formatDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	s := int(seconds + 0.5)
	h, m := s/3600, (s%3600)/60
	s %= 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatBytes(n float64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f ГБ", n/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f МБ", n/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f КБ", n/(1<<10))
	default:
		return fmt.Sprintf("%.0f Б", n)
	}
}
