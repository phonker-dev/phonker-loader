package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Журнал программы: %TEMP%\PhonkerLoader\phonker.log. В оконном режиме нет консоли,
// поэтому сюда же пишется текст аварии (если программа упадёт), — его можно
// открыть из настроек и отправить разработчику.

const logLimit = 512 << 10 // больше — начинаем журнал заново

var (
	logMu     sync.Mutex
	logFile   *os.File
	prevCrash string // авария в прошлый раз (последние строки), "" — всё было хорошо
)

var procSetStdHandle = kernel32.NewProc("SetStdHandle")

func logPath() string { return filepath.Join(os.TempDir(), sessionRootName, "phonker.log") }

func openLog() {
	path := logPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	prevCrash = findPrevCrash(path)
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > logLimit {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return
	}
	logFile = f
	if guiMode {
		// Аварии Go печатает в stderr — направляем его в журнал.
		stderrID := syscall.STD_ERROR_HANDLE // отрицательная константа: переводим в uintptr через переменную
		procSetStdHandle.Call(uintptr(stderrID), f.Fd())
		os.Stderr = f
	}
	mode := "окно"
	if !guiMode {
		mode = "консоль"
	}
	logf("==== запуск v%s (%s), %s", appVersion, mode, strings.Join(os.Args[1:], " "))
}

func logf(format string, a ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile == nil {
		return
	}
	fmt.Fprintf(logFile, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

// findPrevCrash смотрит, не закончился ли прошлый запуск аварией (panic / fatal error).
func findPrevCrash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(data)
	if i := strings.LastIndex(text, "==== запуск"); i >= 0 {
		text = text[i:]
	}
	for _, marker := range []string{"panic:", "fatal error:"} {
		if i := strings.Index(text, marker); i >= 0 {
			crash := text[i:]
			if len(crash) > 1500 {
				crash = crash[:1500]
			}
			return crash
		}
	}
	return ""
}
