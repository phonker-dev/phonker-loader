package main

import (
	"fmt"
	"sort"
	"time"
)

// Статистика: сколько скачано и сколько это весит — своими загрузками и через бота.

type statItem struct {
	when  time.Time
	size  int64
	audio bool
	site  string
	user  int64 // 0 — своя загрузка
}

type counter struct {
	n     int
	bytes int64
}

func (c *counter) add(it statItem) { c.n++; c.bytes += it.size }

func collectStats() (own, viaBot []statItem) {
	for _, e := range readHistory() {
		own = append(own, statItem{when: e.when(), size: e.size(), audio: e.audio(), site: siteName(e.URL)})
	}
	for _, e := range botDB.history(0, botLogLimit, true) {
		viaBot = append(viaBot, statItem{when: e.Time, size: e.Size, audio: e.Audio, site: siteName(e.URL), user: e.UserID})
	}
	return own, viaBot
}

// statsReport — посчитанная статистика: её печатает консоль и показывает окно.
type statsReport struct {
	Empty   bool       `json:"empty"`
	Periods []string   `json:"periods"`
	Rows    []statsRow `json:"rows"`
	Total   string     `json:"total"`
	Video   int        `json:"video"`
	Audio   int        `json:"audio"`
	Sites   []string   `json:"sites"`
	Friends []string   `json:"friends"`
	Since   string     `json:"since"`
	Unknown int        `json:"unknown"` // старые загрузки без известного размера
}

type statsRow struct {
	Label  string   `json:"label"`
	Counts []int    `json:"counts"`
	Bytes  []string `json:"bytes"`
}

func buildStats() statsReport {
	own, viaBot := collectStats()
	all := append(append([]statItem(nil), own...), viaBot...)
	if len(all) == 0 {
		return statsReport{Empty: true}
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	names := []string{"Всего", "Сегодня", "7 дней", "30 дней"}
	since := []time.Time{{}, today, today.AddDate(0, 0, -6), today.AddDate(0, 0, -29)}
	row := func(label string, items []statItem) statsRow {
		r := statsRow{Label: label, Counts: make([]int, len(since)), Bytes: make([]string, len(since))}
		sums := make([]int64, len(since))
		for _, it := range items {
			for i, s := range since {
				if !it.when.Before(s) {
					r.Counts[i]++
					sums[i] += it.size
				}
			}
		}
		for i := range sums {
			r.Bytes[i] = formatBytes(float64(sums[i]))
		}
		return r
	}

	rep := statsReport{Periods: names, Rows: []statsRow{row("Мои загрузки", own)}}
	if len(viaBot) > 0 {
		rep.Rows = append(rep.Rows, row("Через бота", viaBot))
	}

	var total counter
	sites := map[string]*counter{}
	first := now
	for _, it := range all {
		total.add(it)
		if it.audio {
			rep.Audio++
		}
		if sites[it.site] == nil {
			sites[it.site] = &counter{}
		}
		sites[it.site].add(it)
		if !it.when.IsZero() && it.when.Before(first) {
			first = it.when
		}
		if it.size == 0 {
			rep.Unknown++
		}
	}
	rep.Video = total.n - rep.Audio
	rep.Total = fmt.Sprintf("%d %s · %s", total.n, plural(total.n, "файл", "файла", "файлов"), formatBytes(float64(total.bytes)))
	rep.Sites = topCounters(sites, 5, func(k string) string { return k })
	rep.Since = first.Format("02.01.2006")

	if len(viaBot) > 0 {
		friends := map[string]*counter{}
		for _, it := range viaBot {
			key := fmt.Sprint(it.user)
			if friends[key] == nil {
				friends[key] = &counter{}
			}
			friends[key].add(it)
		}
		rep.Friends = topCounters(friends, 5, func(k string) string {
			var id int64
			fmt.Sscan(k, &id)
			if st, ok := botDB.user(id); ok {
				if st.Username != "" {
					return "@" + st.Username
				}
				return st.Name
			}
			return "id " + k
		})
	}
	return rep
}

// statsSummary — одна строка для главного меню.
func statsSummary() string {
	rep := buildStats()
	if rep.Empty {
		return "пока ничего не скачано"
	}
	return rep.Total
}

func statsScreen() {
	header("СТАТИСТИКА")
	rep := buildStats()
	if rep.Empty {
		fmt.Println(" Пока ничего не скачано.")
		fmt.Println(rule())
		waitEnter("Enter — назад")
		return
	}
	head := fmt.Sprintf(" %-15s", "")
	for _, p := range rep.Periods {
		head += fmt.Sprintf("%-11s", p)
	}
	fmt.Println(bold(head))
	for _, r := range rep.Rows {
		line1, line2 := fmt.Sprintf(" %-15s", r.Label), fmt.Sprintf(" %-15s", "")
		for i := range r.Counts {
			line1 += fmt.Sprintf("%-11d", r.Counts[i])
			line2 += fmt.Sprintf("%-11s", r.Bytes[i])
		}
		fmt.Println(line1)
		fmt.Println(dim(line2))
	}
	fmt.Println(rule())
	fmt.Printf(" Итого   : %s (видео %d, mp3 %d)\n", rep.Total, rep.Video, rep.Audio)
	fmt.Println(" Сайты   : " + joinComma(rep.Sites))
	if len(rep.Friends) > 0 {
		fmt.Println(" Друзья  : " + joinComma(rep.Friends))
	}
	fmt.Println(dim(" Считается с " + rep.Since + "."))
	if rep.Unknown > 0 {
		fmt.Println(dim(fmt.Sprintf(" У %d старых загрузок размер неизвестен: файлов уже нет в папке.", rep.Unknown)))
	}
	fmt.Println(rule())
	waitEnter("Enter — назад")
}

func joinComma(list []string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func topCounters(m map[string]*counter, n int, name func(string) string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]].n > m[keys[j]].n })
	var parts []string
	for i := 0; i < len(keys) && i < n; i++ {
		c := m[keys[i]]
		parts = append(parts, fmt.Sprintf("%s %d (%s)", name(keys[i]), c.n, formatBytes(float64(c.bytes))))
	}
	return parts
}

// plural выбирает форму слова: 1 файл, 2 файла, 5 файлов.
func plural(n int, one, few, many string) string {
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}
