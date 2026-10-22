package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// bot_data.json рядом с exe: статистика по друзьям и журнал загрузок бота.
// Друзья видят только свою часть (/history), владелец — всё (меню бота).

const botLogLimit = 2000 // сколько последних загрузок хранить

type userStats struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Username  string    `json:"username,omitempty"`
	Lang      string    `json:"lang,omitempty"`
	Premium   bool      `json:"premium,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Downloads int       `json:"downloads"`
	Failed    int       `json:"failed"`
	Bytes     int64     `json:"bytes"`
	Keyboard  bool      `json:"keyboard,omitempty"` // кнопки внизу чата уже отправлены
}

type logEntry struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	UserID int64     `json:"user_id"`
	URL    string    `json:"url"`
	Title  string    `json:"title,omitempty"`
	Audio  bool      `json:"audio,omitempty"`
	Size   int64     `json:"size,omitempty"`
	OK     bool      `json:"ok"`
	Error  string    `json:"error,omitempty"`
}

type botStore struct {
	mu     sync.Mutex
	Users  map[int64]*userStats `json:"users"`
	Log    []logEntry           `json:"log"`
	NextID int64                `json:"next_id"`

	// Запросы на доступ переживают перезапуск: на них можно ответить и при выключенном боте.
	Requests []botUser `json:"requests,omitempty"`
	Denied   []int64   `json:"denied,omitempty"` // отказано — повторные запросы не беспокоят
	Notify   []int64   `json:"notify,omitempty"` // одобрены без бота — сообщить при запуске

	// Закреплённое сообщение «бот в сети / не в сети» в чате каждого друга: id пользователя → id сообщения.
	Pins map[int64]int64 `json:"pins,omitempty"`
}

var botDB = &botStore{Users: map[int64]*userStats{}}

func botDataPath() string { return filepath.Join(appDir, "bot_data.json") }

func loadBotData() {
	data, err := os.ReadFile(botDataPath())
	if err != nil {
		return
	}
	botDB.mu.Lock()
	defer botDB.mu.Unlock()
	_ = json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), botDB)
	if botDB.Users == nil {
		botDB.Users = map[int64]*userStats{}
	}
	// Имена, сохранённые до появления cleanName, могут содержать «Zalgo» и невидимые символы.
	for _, st := range botDB.Users {
		st.Name = cleanStoredName(st.Name)
	}
	for i := range botDB.Requests {
		botDB.Requests[i].Name = cleanStoredName(botDB.Requests[i].Name)
	}
}

// save вызывается под botDB.mu.
func (s *botStore) save() {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	tmp := botDataPath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, botDataPath())
	}
}

// seen обновляет данные о человеке при каждом его сообщении боту.
func (s *botStore) seen(u *tgUser) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Users[u.ID]
	now := time.Now()
	if st == nil {
		st = &userStats{ID: u.ID, FirstSeen: now}
		s.Users[u.ID] = st
	}
	st.Name = u.displayName()
	st.Username = u.Username
	st.Lang = u.LanguageCode
	st.Premium = u.IsPremium
	st.LastSeen = now
	s.save()
}

// record добавляет загрузку в журнал и возвращает её номер.
func (s *botStore) record(e logEntry) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.NextID++
	e.ID = s.NextID
	e.Time = time.Now()
	s.Log = append(s.Log, e)
	if len(s.Log) > botLogLimit {
		s.Log = s.Log[len(s.Log)-botLogLimit:]
	}
	if st := s.Users[e.UserID]; st != nil {
		if e.OK {
			st.Downloads++
			st.Bytes += e.Size
		} else {
			st.Failed++
		}
	}
	s.save()
	return e.ID
}

// markFailed помечает загрузку неудачной (например, файл скачался, но Telegram его не принял).
func (s *botStore) markFailed(id int64, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Log) - 1; i >= 0; i-- {
		e := &s.Log[i]
		if e.ID != id || !e.OK {
			continue
		}
		e.OK, e.Error = false, msg
		if st := s.Users[e.UserID]; st != nil {
			st.Downloads--
			st.Bytes -= e.Size
			st.Failed++
		}
		s.save()
		return
	}
}

func (s *botStore) hasKeyboard(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Users[id]
	return st != nil && st.Keyboard
}

func (s *botStore) setKeyboard(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.Users[id]; st != nil && !st.Keyboard {
		st.Keyboard = true
		s.save()
	}
}

// addRequest сохраняет запрос на доступ; false — такой запрос уже есть.
func (s *botStore) addRequest(u botUser) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Requests {
		if r.ID == u.ID {
			return false
		}
	}
	s.Requests = append(s.Requests, u)
	s.save()
	return true
}

func (s *botStore) requests() []botUser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]botUser(nil), s.Requests...)
}

// resolveRequest убирает запрос; при отказе запоминает человека, при одобрении
// без запущенного бота — ставит в очередь на приветствие.
func (s *botStore) resolveRequest(id int64, allowed, notifyLater bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.Requests {
		if r.ID == id {
			s.Requests = append(s.Requests[:i], s.Requests[i+1:]...)
			break
		}
	}
	if !allowed {
		s.Denied = append(s.Denied, id)
	} else if notifyLater {
		s.Notify = append(s.Notify, id)
	}
	s.save()
}

func (s *botStore) isDenied(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.Denied {
		if d == id {
			return true
		}
	}
	return false
}

// takeNotify возвращает и очищает список одобренных без бота.
func (s *botStore) takeNotify() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.Notify
	if len(list) > 0 {
		s.Notify = nil
		s.save()
	}
	return list
}

func (s *botStore) pin(userID int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Pins[userID]
}

func (s *botStore) setPin(userID, msgID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Pins == nil {
		s.Pins = map[int64]int64{}
	}
	s.Pins[userID] = msgID
	s.save()
}

// cleanStoredName чистит имя вида «Имя Фамилия (@username)».
func cleanStoredName(s string) string {
	if i := strings.LastIndex(s, " (@"); i >= 0 && strings.HasSuffix(s, ")") {
		return cleanName(s[:i]) + s[i:]
	}
	return cleanName(s)
}

func (s *botStore) entry(id int64) (logEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Log) - 1; i >= 0; i-- {
		if s.Log[i].ID == id {
			return s.Log[i], true
		}
	}
	return logEntry{}, false
}

// history — последние успешные загрузки человека (или всех, если userID == 0), новые сверху.
func (s *botStore) history(userID int64, n int, onlyOK bool) []logEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []logEntry
	for i := len(s.Log) - 1; i >= 0 && len(out) < n; i-- {
		e := s.Log[i]
		if (userID == 0 || e.UserID == userID) && (!onlyOK || e.OK) {
			out = append(out, e)
		}
	}
	return out
}

func (s *botStore) user(id int64) (userStats, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.Users[id]; st != nil {
		return *st, true
	}
	return userStats{}, false
}

// topSites — самые частые сайты в загрузках человека.
func (s *botStore) topSites(userID int64, n int) string {
	s.mu.Lock()
	counts := map[string]int{}
	for _, e := range s.Log {
		if e.UserID == userID && e.OK {
			counts[siteName(e.URL)]++
		}
	}
	s.mu.Unlock()

	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	var parts []string
	for i := 0; i < len(list) && i < n; i++ {
		parts = append(parts, list[i].k+" "+strconv.Itoa(list[i].v))
	}
	return strings.Join(parts, ", ")
}

func siteName(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return "?"
	}
	h := strings.ToLower(u.Hostname())
	for _, p := range []string{"www.", "m.", "vm.", "vt.", "music."} {
		h = strings.TrimPrefix(h, p)
	}
	if h == "youtu.be" {
		h = "youtube.com"
	}
	return h
}
