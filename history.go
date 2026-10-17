package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Формат совместим с history.json прошлых версий (поле file добавлено).
type historyEntry struct {
	ID    int    `json:"id"`
	Date  string `json:"date"`
	Time  string `json:"time"`
	Title string `json:"title"`
	URL   string `json:"url"`
	File  string `json:"file,omitempty"`
}

func historyPath() string { return filepath.Join(downloadDir, "history.json") }

func readHistory() []historyEntry {
	var list []historyEntry
	data, err := os.ReadFile(historyPath())
	if err != nil {
		return nil
	}
	_ = json.Unmarshal(data, &list)
	return list
}

func appendHistory(title, url, file string) {
	list := readHistory()
	id := 1
	if len(list) > 0 {
		id = list[len(list)-1].ID + 1
	}
	now := time.Now()
	list = append(list, historyEntry{
		ID:    id,
		Date:  now.Format("2006-01-02"),
		Time:  now.Format("15:04:05"),
		Title: title,
		URL:   url,
		File:  filepath.Base(file),
	})

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(list) != nil {
		return
	}

	// Пишем во временный файл и подменяем — история не побьётся, если программу закроют посреди записи.
	// Файл скрытый, чтобы не мешаться среди видео. Скрытый файл Windows не даёт
	// перезаписать напрямую, поэтому сначала снимаем атрибут.
	path := historyPath()
	tmp := path + ".tmp"
	if os.WriteFile(tmp, buf.Bytes(), 0o644) != nil {
		os.Remove(tmp)
		return
	}
	setHidden(path, false)
	if os.Rename(tmp, path) != nil {
		os.Remove(tmp)
	}
	setHidden(path, true)
}

func setHidden(path string, hidden bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	attrs, err := syscall.GetFileAttributes(p)
	if err != nil {
		return
	}
	if hidden {
		attrs |= syscall.FILE_ATTRIBUTE_HIDDEN
	} else {
		attrs &^= syscall.FILE_ATTRIBUTE_HIDDEN
	}
	_ = syscall.SetFileAttributes(p, attrs)
}
