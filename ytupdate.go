package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Проверка новой версии yt-dlp: при запуске в фоне спрашиваем GitHub, какая версия последняя.
// Напоминание показывается, только если вышла версия новее установленной, — а не просто
// потому, что установленной версии много дней (новых может и не быть).

var latestYtdlp atomic.Value // string — последняя версия на GitHub, "" — неизвестно

func checkLatestYtdlp() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest", nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", "PhonkerLoader/"+appVersion)
		resp, err := proxiedClient().Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		var r struct {
			Tag string `json:"tag_name"`
		}
		if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&r) == nil {
			latestYtdlp.Store(strings.TrimSpace(r.Tag))
		}
	}()
}

// ytdlpUpdateAvailable возвращает новую версию, если она вышла, иначе "".
func ytdlpUpdateAvailable() string {
	latest, _ := latestYtdlp.Load().(string)
	if latest != "" && newerVersion(latest, tools.ytdlpVersion) {
		return latest
	}
	return ""
}

// newerVersion сравнивает версии вида 2026.08.19 (и 2026.08.19.1).
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if len(x) != len(y) {
			return len(x) > len(y)
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// proxiedClient — HTTP-клиент, который ходит через текущий прокси (см. proxy.go).
func proxiedClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		if p, _ := currentProxy(); p != "" {
			return url.Parse(p)
		}
		return nil, nil
	}
	return &http.Client{Transport: transport}
}
