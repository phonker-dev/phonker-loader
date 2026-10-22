package main

import "testing"

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{1: "файл", 2: "файла", 5: "файлов", 11: "файлов", 21: "файл", 104: "файла", 112: "файлов"} {
		if got := plural(n, "файл", "файла", "файлов"); got != want {
			t.Errorf("plural(%d) = %q, want %q", n, got, want)
		}
	}
}
