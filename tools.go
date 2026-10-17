package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Утилиты лежат внутри exe в сжатом виде (assets/*.gz готовит cmd/pack).
// При запуске они распаковываются во временную папку сессии и удаляются при выходе.

//go:embed assets/yt-dlp.exe.gz
var ytdlpGz []byte

//go:embed assets/ffmpeg.exe.gz
var ffmpegGz []byte

//go:embed assets/deno.exe.gz
var denoGz []byte

//go:embed assets/yt-dlp.version
var embeddedYtdlpVersion string

const (
	sessionRootName = "PhonkerLoader"
	legacyTempName  = "phonker_loader" // папка старых версий (до 1.1), её чистим
	lockFileName    = ".lock"
)

type toolset struct {
	dir    string // папка сессии в %TEMP%
	bin    string
	work   string // промежуточные файлы загрузок
	ytdlp  string
	ffmpeg string
	deno   string

	ytdlpVersion string
	updatedYtdlp string // путь к обновлённому yt-dlp, если он новее встроенного

	lock  *os.File
	ready chan struct{}
	err   error
}

var tools *toolset

func newToolset() (*toolset, error) {
	root := filepath.Join(os.TempDir(), sessionRootName)
	sweepStaleSessions(root)

	dir := filepath.Join(root, fmt.Sprintf("s%d-%d", os.Getpid(), time.Now().UnixNano()%1e6))
	t := &toolset{
		dir:   dir,
		bin:   filepath.Join(dir, "bin"),
		work:  filepath.Join(dir, "work"),
		ready: make(chan struct{}),
	}
	t.ytdlp = filepath.Join(t.bin, "yt-dlp.exe")
	t.ffmpeg = filepath.Join(t.bin, "ffmpeg.exe")
	t.deno = filepath.Join(t.bin, "deno.exe")

	for _, d := range []string{t.bin, t.work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("не удалось создать временную папку %s: %w", dir, err)
		}
	}
	// Открытый файл блокировки не даёт другим копиям программы удалить нашу папку.
	lock, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать временную папку %s: %w", dir, err)
	}
	t.lock = lock

	// Если пользователь обновлял yt-dlp и обновлённая версия новее встроенной — берём её.
	t.ytdlpVersion = strings.TrimSpace(embeddedYtdlpVersion)
	if p, v := updatedYtdlp(); p != "" && v > t.ytdlpVersion {
		t.updatedYtdlp, t.ytdlpVersion = p, v
	}
	return t, nil
}

// extract распаковывает утилиты в фоне, пока пользователь вводит ссылку.
func (t *toolset) extract() {
	defer close(t.ready)

	items := []struct {
		path string
		data []byte
	}{{t.ffmpeg, ffmpegGz}, {t.deno, denoGz}}

	if t.updatedYtdlp != "" {
		if err := copyFile(t.updatedYtdlp, t.ytdlp); err != nil {
			t.updatedYtdlp = ""
			t.ytdlpVersion = strings.TrimSpace(embeddedYtdlpVersion)
		}
	}
	if t.updatedYtdlp == "" {
		items = append(items, struct {
			path string
			data []byte
		}{t.ytdlp, ytdlpGz})
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, it := range items {
		wg.Add(1)
		go func(path string, data []byte) {
			defer wg.Done()
			if err := unpack(path, data); err != nil {
				mu.Lock()
				if t.err == nil {
					t.err = fmt.Errorf("не удалось распаковать %s: %w", filepath.Base(path), err)
				}
				mu.Unlock()
			}
		}(it.path, it.data)
	}
	wg.Wait()
}

func unpack(path string, gz []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// gzip проверяет контрольную сумму в конце потока — битый файл не пройдёт.
	if _, err = io.Copy(f, zr); err == nil {
		err = zr.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// wait блокируется до окончания распаковки, показывая спиннер, если ждать приходится.
func (t *toolset) wait() error {
	select {
	case <-t.ready:
		return t.err
	default:
	}
	l := startLive("Подготовка компонентов...")
	<-t.ready
	l.Stop()
	return t.err
}

// env — окружение для дочерних процессов: всё временное (распаковка yt-dlp,
// кэш deno и т.п.) складывается в папку сессии, а не разбрасывается по системе.
func (t *toolset) env() []string {
	tmp := filepath.Join(t.dir, "tmp")
	_ = os.MkdirAll(tmp, 0o755)
	override := map[string]string{
		"TEMP":                 tmp,
		"TMP":                  tmp,
		"DENO_DIR":             filepath.Join(t.dir, "deno"),
		"DENO_NO_UPDATE_CHECK": "1",
		"DENO_NO_PROMPT":       "1",
		"PYTHONUTF8":           "1",
		"PYTHONIOENCODING":     "utf-8",
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := override[strings.ToUpper(k)]; !ok {
			env = append(env, kv)
		}
	}
	for k, v := range override {
		env = append(env, k+"="+v)
	}
	return env
}

func (t *toolset) command(args ...string) *exec.Cmd {
	cmd := exec.Command(t.ytdlp, args...)
	cmd.Env = t.env()
	cmd.Dir = t.dir
	return cmd
}

// start запускает процесс и привязывает его к job-объекту, чтобы при закрытии
// программы (в том числе крестиком) yt-dlp, ffmpeg и deno гарантированно завершились.
func start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	jobs.assign(cmd.Process.Pid)
	return nil
}

func (t *toolset) remove() {
	jobs.terminate()
	if t.lock != nil {
		t.lock.Close()
	}
	// Процессы освобождают файлы не мгновенно — несколько попыток.
	for i := 0; i < 15; i++ {
		if err := os.RemoveAll(t.dir); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if root := filepath.Dir(t.dir); isEmptyDir(root) {
		os.Remove(root)
	}
}

// sweepStaleSessions удаляет папки, оставшиеся после аварийного завершения
// (например, выключили ПК во время загрузки), и мусор старых версий.
func sweepStaleSessions(root string) {
	_ = os.RemoveAll(filepath.Join(os.TempDir(), legacyTempName))

	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		// Файл блокировки занят — эта копия программы ещё работает.
		if err := os.Remove(filepath.Join(dir, lockFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}

func isEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// ---------- обновлённый yt-dlp ----------
// YouTube регулярно ломает загрузчики, поэтому yt-dlp можно обновить из меню.
// Обновление — единственное, что программа хранит вне своей папки:
// %LOCALAPPDATA%\PhonkerLoader\yt-dlp.exe (~18 МБ).

func updateDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "PhonkerLoader")
}

func updatedYtdlp() (path, version string) {
	dir := updateDir()
	path = filepath.Join(dir, "yt-dlp.exe")
	v, err := os.ReadFile(filepath.Join(dir, "yt-dlp.version"))
	if err != nil {
		return "", ""
	}
	if _, err := os.Stat(path); err != nil {
		return "", ""
	}
	return path, strings.TrimSpace(string(v))
}

// ytdlpAge возвращает возраст версии yt-dlp в днях (версии имеют вид 2026.03.17).
func ytdlpAge(version string) int {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return 0
	}
	y, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	d, _ := strconv.Atoi(parts[2])
	if y == 0 {
		return 0
	}
	released := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.Local)
	return int(time.Since(released).Hours() / 24)
}

// ---------- job object ----------

var (
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount, WriteOperationCount, OtherOperationCount uint64
	ReadTransferCount, WriteTransferCount, OtherTransferCount    uint64
}

type jobObjectExtendedLimitInformationT struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type jobObject struct{ h uintptr }

var jobs jobObject

func (j *jobObject) init() {
	h, _, _ := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return
	}
	var info jobObjectExtendedLimitInformationT
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	r, _, _ := procSetInformationJobObject.Call(h, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if r == 0 {
		syscall.CloseHandle(syscall.Handle(h))
		return
	}
	j.h = h
}

func (j *jobObject) assign(pid int) {
	if j.h == 0 {
		return
	}
	ph, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(pid))
	if err != nil {
		return
	}
	procAssignProcessToJobObject.Call(j.h, uintptr(ph))
	syscall.CloseHandle(ph)
}

func (j *jobObject) terminate() {
	if j.h != 0 {
		procTerminateJobObject.Call(j.h, 1)
	}
}
