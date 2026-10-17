package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Cookies нужны только для видео, где сайт требует вход (18+, «подтвердите, что вы не бот»,
// контент для спонсоров). Пользователь сам указывает путь к своему файлу cookies,
// программа читает его копию и никогда не изменяет оригинал.
//
// Где ищется файл (по порядку):
//  1. путь, сохранённый через пункт меню «Cookies» (settings.json рядом с exe);
//  2. cookies.txt рядом с exe.
// Без файла программа просто работает без входа в аккаунт.

type settings struct {
	Cookies string `json:"cookies,omitempty"`
}

var cfg settings

func settingsPath() string { return filepath.Join(appDir, "settings.json") }

func loadSettings() {
	data, err := os.ReadFile(settingsPath())
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
}

// saveSettings пишет settings.json, а если сохранять нечего — удаляет его.
func saveSettings() error {
	if cfg == (settings{}) {
		err := os.Remove(settingsPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(settingsPath(), data, 0o644)
}

// cookiesSource возвращает путь к файлу cookies пользователя или "", если его нет.
func cookiesSource() string {
	if cfg.Cookies != "" {
		return cfg.Cookies
	}
	if p := filepath.Join(appDir, "cookies.txt"); fileExists(p) {
		return p
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// prepareCookies копирует cookies пользователя в папку сессии в формате, который
// понимает yt-dlp (yt-dlp дописывает в файл cookies, поэтому оригинал ему не отдаём).
// Возвращает аргументы для yt-dlp; nil — работать без cookies.
func prepareCookies() ([]string, error) {
	src := cookiesSource()
	if src == "" {
		return nil, nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать файл cookies %s: %w", src, err)
	}
	netscape, n, err := toNetscape(data)
	if err != nil {
		return nil, fmt.Errorf("файл cookies %s: %w", src, err)
	}
	if n == 0 {
		return nil, fmt.Errorf("в файле cookies %s нет ни одной записи", src)
	}
	dst := filepath.Join(tools.dir, "cookies.txt")
	if err := os.WriteFile(dst, netscape, 0o600); err != nil {
		return nil, err
	}
	return []string{"--cookies", dst}, nil
}

// toNetscape принимает cookies в формате Netscape (cookies.txt) или JSON
// (так экспортируют многие расширения) и возвращает формат Netscape и число записей.
func toNetscape(data []byte) ([]byte, int, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // BOM
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
		return jsonToNetscape(trimmed)
	}

	var out bytes.Buffer
	out.WriteString("# Netscape HTTP Cookie File\n")
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		// Строки "#HttpOnly_.youtube.com ..." — это cookie, а не комментарий.
		if line == "" || (strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly_")) {
			continue
		}
		if len(strings.Split(line, "\t")) != 7 {
			return nil, 0, errors.New("неизвестный формат — нужен cookies.txt (Netscape) или JSON")
		}
		out.WriteString(line + "\n")
		n++
	}
	return out.Bytes(), n, nil
}

type jsonCookie struct {
	Domain         string  `json:"domain"`
	Path           string  `json:"path"`
	Secure         bool    `json:"secure"`
	HostOnly       bool    `json:"hostOnly"`
	ExpirationDate float64 `json:"expirationDate"`
	Name           string  `json:"name"`
	Value          string  `json:"value"`
}

func jsonToNetscape(data []byte) ([]byte, int, error) {
	var list []jsonCookie
	if err := json.Unmarshal(data, &list); err != nil {
		// Некоторые расширения заворачивают массив в объект {"cookies": [...]}.
		var wrapped struct {
			Cookies []jsonCookie `json:"cookies"`
		}
		if json.Unmarshal(data, &wrapped) != nil {
			return nil, 0, errors.New("не удалось разобрать JSON с cookies")
		}
		list = wrapped.Cookies
	}

	var out bytes.Buffer
	out.WriteString("# Netscape HTTP Cookie File\n")
	n := 0
	for _, c := range list {
		if c.Domain == "" || c.Name == "" {
			continue
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		includeSub := "TRUE"
		if c.HostOnly && !strings.HasPrefix(c.Domain, ".") {
			includeSub = "FALSE"
		}
		fmt.Fprintf(&out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Domain, includeSub, path, strings.ToUpper(strconv.FormatBool(c.Secure)),
			strconv.FormatInt(int64(c.ExpirationDate), 10), c.Name, c.Value)
		n++
	}
	return out.Bytes(), n, nil
}

// cookiesMenu — пункт меню для указания файла cookies.
func cookiesMenu() {
	header("COOKIES")
	fmt.Println(" Нужны только для видео, где сайт требует вход: 18+, «подтвердите,")
	fmt.Println(" что вы не бот», видео для спонсоров. Без них программа тоже работает.")
	fmt.Println()
	fmt.Println(dim(" Как получить: в браузере, где ты вошёл в YouTube, установи расширение"))
	fmt.Println(dim(" «Get cookies.txt LOCALLY», открой youtube.com и нажми Export."))
	fmt.Println(dim(" Файл никуда не отправляется — его читает только yt-dlp на этом ПК."))
	fmt.Println(rule)

	if src := cookiesSource(); src != "" {
		state := green("используется")
		if !fileExists(src) {
			state = red("файл не найден")
		}
		fmt.Printf(" Сейчас: %s (%s)\n", src, state)
	} else {
		fmt.Println(" Сейчас: " + dim("не указаны"))
	}

	in := prompt("\nПуть к файлу (можно перетащить файл в окно), - убрать, Enter — назад: ")
	in = strings.Trim(in, `"' `)
	switch in {
	case "":
		return
	case "-":
		cfg.Cookies = ""
		if err := saveSettings(); err != nil {
			printErr("Не удалось сохранить настройки: %v", err)
			return
		}
		printOK("Cookies больше не используются.")
		if p := filepath.Join(appDir, "cookies.txt"); fileExists(p) {
			printHint("Рядом с программой лежит cookies.txt — он будет подхвачен. Удали его, если не нужен.")
		}
		return
	}

	abs, err := filepath.Abs(in)
	if err == nil {
		in = abs
	}
	data, err := os.ReadFile(in)
	if err != nil {
		printErr("Не удалось открыть файл: %v", err)
		return
	}
	if _, n, err := toNetscape(data); err != nil {
		printErr("%v", err)
		return
	} else if n == 0 {
		printErr("В файле нет ни одной записи cookies.")
		return
	}
	cfg.Cookies = in
	if err := saveSettings(); err != nil {
		printErr("Не удалось сохранить настройки: %v", err)
		return
	}
	printOK("Cookies будут использоваться для всех загрузок.")
	printHint("Путь сохранён в %s", settingsPath())
}
