package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// settings.json лежит рядом с exe и появляется, только если пользователь что-то настроил.
// В нём может быть токен бота, поэтому он в .gitignore и никуда не отправляется.
type settings struct {
	Cookies  string    `json:"cookies,omitempty"`
	Proxy    string    `json:"proxy,omitempty"` // "" — авто, "off" — без прокси, иначе адрес
	BotToken string    `json:"bot_token,omitempty"`
	BotUsers []botUser `json:"bot_users,omitempty"`
	ShowBot  bool      `json:"show_bot,omitempty"` // раздел «Бот» в окне включён вручную (без токена)
}

type botUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

var cfg settings

// cfgMu защищает список пользователей бота: в оконном режиме его одновременно читает бот и меняет окно.
var cfgMu sync.Mutex

func isFriend(id int64) bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	for _, u := range cfg.BotUsers {
		if u.ID == id {
			return true
		}
	}
	return false
}

func friendsCopy() []botUser {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return append([]botUser(nil), cfg.BotUsers...)
}

// removeFriend убирает доступ и сохраняет настройки.
func removeFriend(id int64) (botUser, error) {
	cfgMu.Lock()
	var removed botUser
	for i, u := range cfg.BotUsers {
		if u.ID == id {
			removed = u
			cfg.BotUsers = append(cfg.BotUsers[:i], cfg.BotUsers[i+1:]...)
			break
		}
	}
	cfgMu.Unlock()
	return removed, saveSettings()
}

// grantAccess выдаёт доступ и сохраняет списки. notifyLater — бот выключен,
// «доступ открыт» человек получит при следующем запуске.
func grantAccess(u botUser, notifyLater bool) error {
	cfgMu.Lock()
	known := false
	for _, f := range cfg.BotUsers {
		if f.ID == u.ID {
			known = true
		}
	}
	if !known {
		cfg.BotUsers = append(cfg.BotUsers, u)
	}
	cfgMu.Unlock()
	botDB.resolveRequest(u.ID, true, notifyLater)
	return saveSettings()
}

// botVisible — показывать ли раздел «Бот» в окне. Обычному пользователю бот не нужен,
// поэтому раздел скрыт, пока его не включат в Настройках или не укажут токен.
func botVisible() bool { return cfg.BotToken != "" || cfg.ShowBot }

func settingsPath() string { return filepath.Join(appDir, "settings.json") }

func loadSettings() {
	data, err := os.ReadFile(settingsPath())
	if err == nil {
		// Блокнот иногда сохраняет файл с невидимой меткой BOM — без её удаления JSON не читается.
		_ = json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &cfg)
	}
	for i := range cfg.BotUsers {
		cfg.BotUsers[i].Name = cleanStoredName(cfg.BotUsers[i].Name)
	}
}

// saveSettings пишет settings.json, а если сохранять нечего — удаляет его.
func saveSettings() error {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.Cookies == "" && cfg.Proxy == "" && cfg.BotToken == "" && len(cfg.BotUsers) == 0 && !cfg.ShowBot {
		err := os.Remove(settingsPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := settingsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath())
}

func settingsMenu() {
	for {
		header("НАСТРОЙКИ")
		fmt.Println(" [1] Прокси   " + dim(proxyStatusLine()))
		if src := cookiesSource(); src != "" {
			fmt.Println(" [2] Cookies  " + green("подключены"))
		} else {
			fmt.Println(" [2] Cookies  " + dim("не указаны (нужны редко)"))
		}
		ver := "yt-dlp " + tools.ytdlpVersion
		if v := ytdlpUpdateAvailable(); v != "" {
			ver += yellow(" — есть новая " + v)
		} else if latest, _ := latestYtdlp.Load().(string); latest != "" {
			ver += dim(" (последняя)")
		}
		fmt.Println(" [3] Обновить " + ver)
		fmt.Println(dim(" [0] Назад"))
		fmt.Println(rule())

		switch prompt("Введи номер: ") {
		case "1":
			proxyMenu()
		case "2":
			cookiesMenu()
		case "3":
			fmt.Println()
			if err := tools.wait(); err != nil {
				printErr("%v", err)
			} else if err := updateYtdlp(); err != nil {
				cancelled.Store(false)
				printErr("Не удалось обновить: %v", err)
			}
		default:
			return
		}
	}
}
