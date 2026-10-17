package main

import (
	"strings"
	"testing"
)

func TestToNetscape(t *testing.T) {
	netscape := "\xef\xbb\xbf# Netscape HTTP Cookie File\r\n" +
		".youtube.com\tTRUE\t/\tTRUE\t1792252089\tGPS\t1\r\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1792252089\tSID\tabc\r\n\r\n"
	out, n, err := toNetscape([]byte(netscape))
	if err != nil || n != 2 || !strings.Contains(string(out), "#HttpOnly_.youtube.com") {
		t.Fatalf("netscape: n=%d err=%v\n%s", n, err, out)
	}

	js := `[{"domain":".youtube.com","path":"/","secure":true,"hostOnly":false,"expirationDate":1792252089.5,"name":"GPS","value":"1"},
	        {"domain":"www.youtube.com","hostOnly":true,"name":"X","value":"y"}]`
	out, n, err = toNetscape([]byte(js))
	want := ".youtube.com\tTRUE\t/\tTRUE\t1792252089\tGPS\t1\nwww.youtube.com\tFALSE\t/\tFALSE\t0\tX\ty\n"
	if err != nil || n != 2 || !strings.HasSuffix(string(out), want) {
		t.Fatalf("json: n=%d err=%v\n%q", n, err, out)
	}

	if _, _, err := toNetscape([]byte("просто текст")); err == nil {
		t.Fatal("ожидалась ошибка формата")
	}
}
