package main

import (
	"strings"
	"unicode"
)

// cleanName приводит имя пользователя Telegram к читаемому виду.
// Некоторые ставят в ник «Zalgo» — десятки надстрочных значков на каждой букве — или
// невидимые символы, переворачивающие текст. Они ломают и консоль, и окно.
// Оставляем максимум один значок на букву (й, ё, é остаются), убираем невидимые и
// управляющие символы, лишние пробелы и слишком длинные имена.
func cleanName(s string) string {
	var b strings.Builder
	marks := 0
	for _, r := range s {
		switch {
		case unicode.In(r, unicode.Mn, unicode.Me):
			if marks >= 1 {
				continue
			}
			marks++
		case unicode.In(r, unicode.Cf, unicode.Cc, unicode.Co, unicode.Cs):
			continue // невидимые, управляющие и «личные» символы
		default:
			marks = 0
		}
		if unicode.IsSpace(r) {
			r = ' '
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	return truncate(out, 40)
}
