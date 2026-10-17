package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// parseTimecode понимает "75", "1:15", "01:02:03", "1:15.5".
func parseTimecode(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("пустое время")
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("не понимаю время %q", s)
	}
	total := 0.0
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("не понимаю время %q", s)
		}
		// минуты и секунды после двоеточия не могут быть больше 59
		if i > 0 && v >= 60 {
			return 0, fmt.Errorf("не понимаю время %q", s)
		}
		total = total*60 + v
	}
	return total, nil
}

// parseRange разбирает "1:15-2:40", "1:15" (до конца) или "-2:40" (с начала).
func parseRange(s string, duration float64) (start, end float64, err error) {
	s = strings.NewReplacer("—", "-", "–", "-", " ", "").Replace(s)
	from, to, hasDash := strings.Cut(s, "-")

	if from != "" {
		if start, err = parseTimecode(from); err != nil {
			return 0, 0, err
		}
	}
	switch {
	case hasDash && to != "":
		if end, err = parseTimecode(to); err != nil {
			return 0, 0, err
		}
	case duration > 0:
		end = duration
	default:
		return 0, 0, errors.New("длина видео неизвестна — укажи и конец, например 1:15-2:40")
	}

	if duration > 0 {
		if start >= duration {
			return 0, 0, fmt.Errorf("начало %s позже конца видео (%s)", formatDuration(start), formatDuration(duration))
		}
		if end > duration {
			end = duration
		}
	}
	if end <= start {
		return 0, 0, errors.New("конец фрагмента должен быть позже начала")
	}
	return start, end, nil
}

// fileTimecode — время для имени файла (двоеточия в именах Windows запрещены).
func fileTimecode(sec float64) string {
	return strings.ReplaceAll(formatDuration(sec), ":", ".")
}
