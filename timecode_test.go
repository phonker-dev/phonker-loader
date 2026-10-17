package main

import "testing"

func TestParseRange(t *testing.T) {
	cases := []struct {
		in         string
		dur        float64
		start, end float64
		ok         bool
	}{
		{"1:15-2:40", 300, 75, 160, true},
		{"75-160", 300, 75, 160, true},
		{"1:15", 300, 75, 300, true},
		{"-2:40", 300, 0, 160, true},
		{"1:00:00-1:00:30", 7200, 3600, 3630, true},
		{"1:15 — 2:40", 300, 75, 160, true},
		{"0:10-9:99", 300, 0, 0, false},
		{"2:40-1:15", 300, 0, 0, false},
		{"6:00", 300, 0, 0, false},
		{"1:00-10:00", 300, 60, 300, true}, // конец обрезается по длине видео
		{"abc", 300, 0, 0, false},
		{"1:15", 0, 0, 0, false}, // длина неизвестна — нужен конец
	}
	for _, c := range cases {
		s, e, err := parseRange(c.in, c.dur)
		if (err == nil) != c.ok || (c.ok && (s != c.start || e != c.end)) {
			t.Errorf("parseRange(%q, %v) = %v, %v, %v", c.in, c.dur, s, e, err)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	for in, want := range map[float64]string{0: "00:00", 75: "01:15", 3725: "01:02:05", 59.6: "01:00"} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}
