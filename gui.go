package main

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Оконный режим. Программа поднимает маленький веб-сервер только для этого компьютера
// (127.0.0.1, случайный порт и секретный ключ) и открывает его в окне Edge в режиме
// приложения — без адресной строки и вкладок, как обычная программа. Edge есть в каждой
// Windows 10/11, поэтому ничего ставить не нужно. Закрыл окно — программа завершилась.

//go:embed web
var webFiles embed.FS

var (
	guiToken  string
	guiPort   int
	guiEdge   *exec.Cmd
	guiEdgeMu sync.Mutex
	lastPing  time.Time
	pingMu    sync.Mutex
)

// guiInfoPath — адрес и ключ окна для второй копии программы. Служебный режим проверки
// (--no-window) пишет свой файл и не мешает настоящей запущенной программе.
func guiInfoPath() string {
	name := "gui.json"
	if hasFlag("--no-window") {
		name = "gui-test.json"
	}
	return filepath.Join(os.TempDir(), sessionRootName, name)
}

func runGUI() {
	// Вторая копия программы не запускает своего бота и сервер, а просит первую показать окно.
	if !hasFlag("--no-window") && !acquireInstance() {
		if askRunningInstanceToShow() {
			return
		}
		messageBox("Phonker Loader уже запущен, но не отвечает. Закрой его в диспетчере задач и попробуй снова.")
		return
	}

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	guiToken = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		messageBox("Не удалось запустить интерфейс: " + err.Error())
		return
	}
	guiPort = ln.Addr().(*net.TCPAddr).Port
	info, _ := json.Marshal(map[string]any{"port": guiPort, "token": guiToken})
	_ = os.WriteFile(guiInfoPath(), info, 0o600)

	srv := &http.Server{Handler: guiHandler(), ReadHeaderTimeout: 10 * time.Second,
		ErrorLog: log.New(logWriter{}, "http: ", 0)}
	logf("окно: http://127.0.0.1:%d", guiPort)
	go func() { _ = srv.Serve(ln) }()

	if hasFlag("--bot") && cfg.BotToken != "" {
		guiStartBot()
	}

	if hasFlag("--no-window") {
		select {} // служебный режим для проверки: только сервер, адрес — в gui.json
	}
	if !openWindow(true) {
		// Edge/Chrome не нашлись — открываем в браузере по умолчанию и следим за «пульсом» страницы.
		shellOpen(guiURL())
		watchHeartbeat()
	}
	select {} // выход — через shutdown (закрытие окна, кнопка «Выйти»)
}

func guiURL() string { return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", guiPort, guiToken) }

// openWindow открывает окно программы. wait — завершить программу, когда окно закроют.
func openWindow(wait bool) bool {
	browser := findBrowser()
	if browser == "" {
		return false
	}
	args := []string{
		"--app=" + guiURL(),
		"--user-data-dir=" + filepath.Join(tools.dir, "window"),
		"--no-first-run", "--no-default-browser-check", "--disable-sync",
		"--disable-features=Translate,msEdgeSidebarV2,msHubApps,msUndersideButton",
		"--window-size=1180,820",
	}
	cmd := exec.Command(browser, args...)
	if err := cmd.Start(); err != nil {
		return false
	}
	if !wait {
		go cmd.Wait()
		return true
	}
	guiEdgeMu.Lock()
	guiEdge = cmd
	guiEdgeMu.Unlock()
	go func() {
		_ = cmd.Wait()
		closedAt := time.Now()
		logf("процесс окна (Edge) завершился")
		// Обычно это значит «окно закрыли». Но Edge иногда перезапускает свой процесс,
		// и окно продолжает работать — тогда страница продолжит присылать запросы.
		time.Sleep(5 * time.Second)
		if pingedAfter(closedAt.Add(time.Second)) {
			logf("страница по-прежнему на связи — продолжаю работу")
			watchHeartbeat()
			return
		}
		logf("окно закрыто — выход")
		shutdown(0)
	}()
	return true
}

func pingedAfter(t time.Time) bool {
	pingMu.Lock()
	defer pingMu.Unlock()
	return lastPing.After(t)
}

// logWriter направляет служебные сообщения HTTP-сервера в журнал.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	logf("%s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// closeWindow закрывает окно при выходе, чтобы можно было удалить временную папку профиля.
func closeWindow() {
	guiEdgeMu.Lock()
	defer guiEdgeMu.Unlock()
	if guiEdge != nil && guiEdge.Process != nil {
		_ = guiEdge.Process.Kill()
	}
	if guiPort != 0 { // файл наш — его писала эта копия (вторая копия его не трогает)
		_ = os.Remove(guiInfoPath())
	}
}

func findBrowser() string {
	var candidates []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, `Google\Chrome\Application\chrome.exe`))
		}
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

// watchHeartbeat — запасной режим без Edge: страница раз в несколько секунд сообщает,
// что открыта. Если её закрыли (тишина 3 минуты), программа завершается.
func watchHeartbeat() {
	go func() {
		for range time.Tick(10 * time.Second) {
			pingMu.Lock()
			last := lastPing
			pingMu.Unlock()
			if !last.IsZero() && time.Since(last) > 3*time.Minute {
				logf("страница не отвечает 3 минуты — выход")
				shutdown(0)
			}
		}
	}()
}

// ---------- одна копия программы ----------

var procCreateMutexW = kernel32.NewProc("CreateMutexW")

const errorAlreadyExists = 183

func acquireInstance() bool {
	name, _ := syscall.UTF16PtrFromString(`Local\PhonkerLoaderWindow`)
	h, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true // не получилось проверить — запускаемся как обычно
	}
	return err != syscall.Errno(errorAlreadyExists)
}

func askRunningInstanceToShow() bool {
	data, err := os.ReadFile(guiInfoPath())
	if err != nil {
		return false
	}
	var info struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &info) != nil || info.Port == 0 {
		return false
	}
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/show", info.Port), bytes.NewReader(nil))
	req.Header.Set("X-Token", info.Token)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ---------- HTTP ----------

func guiHandler() http.Handler {
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	api := guiAPI()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Защита от чужих страниц в браузере: запросы только на наш адрес и только с ключом.
		if r.Host != fmt.Sprintf("127.0.0.1:%d", guiPort) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		// Страница закрывается (navigator.sendBeacon не умеет заголовки — ключ в адресе).
		// Если за 3 секунды страница не появилась снова (это было обновление), выходим.
		if r.URL.Path == "/api/bye" && r.URL.Query().Get("t") == guiToken {
			at := time.Now()
			go func() {
				time.Sleep(3 * time.Second)
				if !pingedAfter(at) {
					logf("окно закрыто (страница попрощалась) — выход")
					shutdown(0)
				}
			}()
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			if r.Header.Get("X-Token") != guiToken {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			pingMu.Lock()
			lastPing = time.Now()
			pingMu.Unlock()
			api.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/" && r.URL.Query().Get("t") != guiToken {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// ---------- окна Windows ----------

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	shell32         = syscall.NewLazyDLL("shell32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
	procShellExecW  = shell32.NewProc("ShellExecuteW")
	procAllocCons   = kernel32.NewProc("AllocConsole")
	procGetConsWin  = kernel32.NewProc("GetConsoleWindow")
)

func messageBox(text string) {
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString("Phonker Loader")
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x40) // MB_ICONINFORMATION
}

// shellOpen открывает файл, папку или ссылку программой по умолчанию.
func shellOpen(target string) {
	verb, _ := syscall.UTF16PtrFromString("open")
	file, _ := syscall.UTF16PtrFromString(target)
	procShellExecW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, 1)
}

// ensureConsole: exe собран как оконное приложение, поэтому для консольного режима
// (--console) окно консоли создаём сами. Если вывод уже перенаправлен (в файл, в другую
// программу), консоль не нужна.
func ensureConsole() {
	if h, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE); err == nil && h != 0 && h != syscall.InvalidHandle {
		if t, _ := syscall.GetFileType(h); t == syscall.FILE_TYPE_DISK || t == syscall.FILE_TYPE_PIPE {
			return
		}
	}
	if w, _, _ := procGetConsWin.Call(); w != 0 {
		return
	}
	procAllocCons.Call()
	reopen := func(id int, name string, mode int) *os.File {
		h, err := syscall.GetStdHandle(id)
		if err != nil || h == 0 {
			return nil
		}
		return os.NewFile(uintptr(h), name)
	}
	if f := reopen(syscall.STD_INPUT_HANDLE, "/dev/stdin", 0); f != nil {
		os.Stdin = f
	}
	if f := reopen(syscall.STD_OUTPUT_HANDLE, "/dev/stdout", 0); f != nil {
		os.Stdout = f
	}
	if f := reopen(syscall.STD_ERROR_HANDLE, "/dev/stderr", 0); f != nil {
		os.Stderr = f
	}
}
