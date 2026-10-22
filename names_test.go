package main

import "testing"

func TestCleanName(t *testing.T) {
	zalgo := "Z͓͔͑͒à́̂l̐g̑o"
	cases := map[string]string{
		"Вася Пупкин":     "Вася Пупкин",
		"Йожик ёлка":      "Йожик ёлка",
		zalgo:             "Z͑àl̐g̑o",
		"evil‮eman":       "evileman", // переворот текста
		"a​b  \t c":       "ab c",     // невидимый пробел и лишние пробелы
		"ꜰʟᴀsʜ 'ɴᴅ sᴘᴀᴄᴇ": "ꜰʟᴀsʜ 'ɴᴅ sᴘᴀᴄᴇ",
		"1234567890123456789012345678901234567890xyz": "123456789012345678901234567890123456789…",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
